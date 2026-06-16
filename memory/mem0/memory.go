package mem0

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

const (
	defaultSearchTopK      = 20
	defaultSearchThreshold = 0.1
)

// Config 汇总 mem0 本地复刻版需要的 LLM、SQLite 和 embedding 配置。
type Config struct {
	DBPath             string
	Model              model.BaseChatModel
	Embedder           Embedder
	CustomInstructions string
	Now                func() time.Time
}

// Memory 复刻 mem0 OSS 的 add/search 主干，并把存储固定到 SQLite。
type Memory struct {
	store              *SQLiteStore
	model              model.BaseChatModel
	embedder           Embedder
	customInstructions string
	now                func() time.Time
}

// NewMemory 初始化 SQLite 存储、假 embedding 和可选真实 LLM 抽取器。
func NewMemory(ctx context.Context, config Config) (*Memory, error) {
	store, err := openSQLiteStore(ctx, config.DBPath)
	if err != nil {
		return nil, err
	}
	embedder := config.Embedder
	if embedder == nil {
		embedder = NewFakeEmbedder(defaultEmbeddingDimension)
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Memory{
		store:              store,
		model:              config.Model,
		embedder:           embedder,
		customInstructions: config.CustomInstructions,
		now:                now,
	}, nil
}

// Close 关闭底层 SQLite 连接。
func (m *Memory) Close() error {
	if m == nil || m.store == nil {
		return nil
	}
	return m.store.Close()
}

// Add 执行 mem0 add 主流程：作用域校验、LLM 抽取、hash 去重、SQLite 持久化。
func (m *Memory) Add(ctx context.Context, req AddRequest) (*AddResponse, error) {
	messages, err := normalizeMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	metadata, filters, err := buildFiltersAndMetadata(req.UserID, req.AgentID, req.RunID, req.Metadata)
	if err != nil {
		return nil, err
	}
	if req.Infer != nil && !*req.Infer {
		results, err := m.addRawMessages(ctx, messages, metadata)
		if err != nil {
			return nil, err
		}
		return &AddResponse{Results: results}, nil
	}
	results, err := m.addInferredMemories(ctx, messages, metadata, filters, req.Prompt)
	if err != nil {
		return nil, err
	}
	return &AddResponse{Results: results}, nil
}

// Search 执行 mem0 search 主流程：query 校验、scope 过滤、假向量召回和混排。
func (m *Memory) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, errors.New("query cannot be empty")
	}
	if req.TopK < 0 {
		return nil, fmt.Errorf("top_k must be non-negative")
	}
	topK := req.TopK
	if topK == 0 {
		topK = defaultSearchTopK
	}
	threshold := defaultSearchThreshold
	if req.Threshold != nil {
		threshold = *req.Threshold
	}
	if threshold < 0 || threshold > 1 {
		return nil, fmt.Errorf("threshold must be between 0 and 1")
	}
	filters, err := validateSearchFilters(req.Filters)
	if err != nil {
		return nil, err
	}
	results, err := m.searchInternal(ctx, query, filters, topK, threshold, req.Explain)
	if err != nil {
		return nil, err
	}
	return &SearchResponse{Results: results}, nil
}

// addRawMessages 保存 infer=false 的原始对话，不做 LLM 抽取和冲突去重。
func (m *Memory) addRawMessages(ctx context.Context, messages []Message, metadata map[string]any) ([]AddResult, error) {
	var results []AddResult
	now := m.now()
	for _, message := range messages {
		if strings.EqualFold(message.Role, "system") {
			continue
		}
		perMessageMetadata := cloneMetadata(metadata)
		perMessageMetadata["role"] = message.Role
		if strings.TrimSpace(message.Name) != "" {
			perMessageMetadata["actor_id"] = strings.TrimSpace(message.Name)
		}
		memory, err := m.buildStoredMemory(ctx, message.Content, perMessageMetadata, now)
		if err != nil {
			return nil, err
		}
		if err := m.store.insertMemory(ctx, memory); err != nil {
			return nil, err
		}
		if err := m.store.addHistory(ctx, memory.ID, nil, memory.Memory, "ADD", memory.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, AddResult{
			ID:      memory.ID,
			Memory:  memory.Memory,
			Event:   "ADD",
			ActorID: memory.ActorID,
			Role:    memory.Role,
		})
	}
	return results, nil
}

// addInferredMemories 复刻 mem0 v3 phased pipeline 的核心写入路径。
func (m *Memory) addInferredMemories(ctx context.Context, messages []Message, metadata map[string]any, filters map[string]any, prompt string) ([]AddResult, error) {
	if m.model == nil {
		return nil, errors.New("llm model is required when infer=true")
	}
	sessionScope := buildSessionScope(filters)
	lastMessages, err := m.store.lastMessages(ctx, sessionScope, 10)
	if err != nil {
		return nil, err
	}
	parsedMessages := parseMessages(messages)
	existing, err := m.searchInternal(ctx, parsedMessages, filters, 10, 0, false)
	if err != nil {
		return nil, err
	}
	existingMemories := make([]map[string]string, 0, len(existing))
	for idx, memory := range existing {
		existingMemories = append(existingMemories, map[string]string{
			"id":   fmt.Sprintf("%d", idx),
			"text": memory.Memory,
		})
	}
	customInstructions := strings.TrimSpace(prompt)
	if customInstructions == "" {
		customInstructions = m.customInstructions
	}
	modelResponse, err := m.model.Generate(ctx, []*schema.Message{
		schema.SystemMessage(additiveExtractionSystemPrompt()),
		schema.UserMessage(buildAdditiveExtractionPrompt(existingMemories, lastMessages, messages, customInstructions)),
	})
	if err != nil {
		return nil, fmt.Errorf("extract memories with llm: %w", err)
	}
	extracted, err := parseExtractionPayload(modelResponse.Content)
	if err != nil {
		return nil, fmt.Errorf("parse extraction response: %w", err)
	}
	if len(extracted) == 0 {
		if err := m.store.saveMessages(ctx, sessionScope, messages, m.now()); err != nil {
			return nil, err
		}
		return nil, nil
	}

	texts := make([]string, 0, len(extracted))
	for _, item := range extracted {
		texts = append(texts, item.Text)
	}
	vectors, err := m.embedder.EmbedBatch(ctx, texts, "add")
	if err != nil {
		return nil, err
	}
	existingHashes, err := m.store.existingHashes(ctx, filters)
	if err != nil {
		return nil, err
	}

	now := m.now()
	seenHashes := map[string]bool{}
	var results []AddResult
	for idx, item := range extracted {
		textHash := memoryHash(item.Text)
		if existingHashes[textHash] || seenHashes[textHash] {
			results = append(results, AddResult{
				Memory:   item.Text,
				Event:    "ADD",
				Skipped:  true,
				SkipNote: "duplicate hash in current scope",
			})
			continue
		}
		seenHashes[textHash] = true

		perMemoryMetadata := cloneMetadata(metadata)
		if item.AttributedTo != "" {
			perMemoryMetadata["attributed_to"] = item.AttributedTo
		}
		memory, err := m.buildStoredMemoryWithVector(item.Text, vectors[idx], perMemoryMetadata, now)
		if err != nil {
			return nil, err
		}
		if err := m.store.insertMemory(ctx, memory); err != nil {
			return nil, err
		}
		if err := m.store.addHistory(ctx, memory.ID, nil, memory.Memory, "ADD", memory.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, AddResult{
			ID:     memory.ID,
			Memory: memory.Memory,
			Event:  "ADD",
		})
	}
	if err := m.store.saveMessages(ctx, sessionScope, messages, now); err != nil {
		return nil, err
	}
	return results, nil
}

// searchInternal 在 SQLite 候选上执行假向量相似度、关键词分和最终排序。
func (m *Memory) searchInternal(ctx context.Context, query string, filters map[string]any, topK int, threshold float64, explain bool) ([]SearchResult, error) {
	queryVector, err := m.embedder.Embed(ctx, query, "search")
	if err != nil {
		return nil, err
	}
	candidates, err := m.store.listMemories(ctx, filters)
	if err != nil {
		return nil, err
	}
	var scored []SearchResult
	for _, candidate := range candidates {
		if !matchesMetadataFilters(candidate, filters) {
			continue
		}
		semantic := cosineSimilarity(queryVector, candidate.Embedding)
		if semantic < threshold {
			continue
		}
		keyword := keywordScore(query, candidate.Memory)
		maxPossible := 1.0
		rawScore := semantic
		if keyword > 0 {
			maxPossible += 1.0
			rawScore += keyword
		}
		finalScore := rawScore / maxPossible
		if finalScore > 1 {
			finalScore = 1
		}
		result := formatSearchResult(candidate, finalScore)
		if explain {
			result.ScoreDetails = &ScoreDetails{
				SemanticScore:    semantic,
				KeywordScore:     keyword,
				RawScore:         rawScore,
				MaxPossibleScore: maxPossible,
				FinalScore:       finalScore,
				Threshold:        threshold,
			}
		}
		scored = append(scored, result)
	}
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})
	if topK < len(scored) {
		scored = scored[:topK]
	}
	return scored, nil
}

// buildStoredMemory 生成一条完整 SQLite memory，其中 embedding 由当前 Embedder 负责。
func (m *Memory) buildStoredMemory(ctx context.Context, text string, metadata map[string]any, now time.Time) (storedMemory, error) {
	vector, err := m.embedder.Embed(ctx, text, "add")
	if err != nil {
		return storedMemory{}, err
	}
	return m.buildStoredMemoryWithVector(text, vector, metadata, now)
}

// buildStoredMemoryWithVector 把文本、向量和 metadata 组装成 SQLite 写入实体。
func (m *Memory) buildStoredMemoryWithVector(text string, vector []float64, metadata map[string]any, now time.Time) (storedMemory, error) {
	createdAt := now.UTC().Format(time.RFC3339)
	if value := asString(metadata["created_at"]); strings.TrimSpace(value) != "" {
		createdAt = value
	} else {
		metadata["created_at"] = createdAt
	}
	metadata["updated_at"] = createdAt
	metadata["data"] = text
	metadata["hash"] = memoryHash(text)
	metadata["text_lemmatized"] = lemmatizeForKeyword(text)
	return storedMemory{
		ID:             uuid.NewString(),
		Memory:         text,
		Hash:           asString(metadata["hash"]),
		Embedding:      vector,
		Metadata:       metadata,
		UserID:         asString(metadata["user_id"]),
		AgentID:        asString(metadata["agent_id"]),
		RunID:          asString(metadata["run_id"]),
		ActorID:        asString(metadata["actor_id"]),
		Role:           asString(metadata["role"]),
		TextLemmatized: asString(metadata["text_lemmatized"]),
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
	}, nil
}

// normalizeMessages 修正 add 输入的 role/content，保证后续 LLM 和落库都有稳定格式。
func normalizeMessages(messages []Message) ([]Message, error) {
	if len(messages) == 0 {
		return nil, errors.New("messages cannot be empty")
	}
	normalized := make([]Message, 0, len(messages))
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "user"
		}
		content := strings.TrimSpace(message.Content)
		if content == "" {
			return nil, errors.New("message content cannot be empty")
		}
		normalized = append(normalized, Message{
			Role:    role,
			Content: content,
			Name:    strings.TrimSpace(message.Name),
		})
	}
	return normalized, nil
}

// parseMessages 将多轮消息压成抽取和搜索都能使用的稳定文本。
func parseMessages(messages []Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "user"
		}
		parts = append(parts, role+": "+strings.TrimSpace(message.Content))
	}
	return strings.Join(parts, "\n")
}

// buildFiltersAndMetadata 同步生成存储 metadata 和查询 filters，并强制至少一个实体 ID。
func buildFiltersAndMetadata(userID, agentID, runID string, input map[string]any) (map[string]any, map[string]any, error) {
	metadata := cloneMetadata(input)
	filters := map[string]any{}
	userID = strings.TrimSpace(userID)
	agentID = strings.TrimSpace(agentID)
	runID = strings.TrimSpace(runID)
	if err := validateEntityID(userID, "user_id"); err != nil {
		return nil, nil, err
	}
	if err := validateEntityID(agentID, "agent_id"); err != nil {
		return nil, nil, err
	}
	if err := validateEntityID(runID, "run_id"); err != nil {
		return nil, nil, err
	}
	if userID != "" {
		metadata["user_id"] = userID
		filters["user_id"] = userID
	}
	if agentID != "" {
		metadata["agent_id"] = agentID
		filters["agent_id"] = agentID
	}
	if runID != "" {
		metadata["run_id"] = runID
		filters["run_id"] = runID
	}
	if len(filters) == 0 {
		return nil, nil, errors.New("at least one of user_id, agent_id, or run_id must be provided")
	}
	return metadata, filters, nil
}

// validateSearchFilters 校验 search 必须通过 filters 传入实体 scope。
func validateSearchFilters(input map[string]any) (map[string]any, error) {
	filters := cloneMetadata(input)
	for _, key := range []string{"user_id", "agent_id", "run_id"} {
		value := strings.TrimSpace(asString(filters[key]))
		if err := validateEntityID(value, key); err != nil {
			return nil, err
		}
		if value != "" {
			filters[key] = value
		}
	}
	if asString(filters["user_id"]) == "" && asString(filters["agent_id"]) == "" && asString(filters["run_id"]) == "" {
		return nil, errors.New("filters must contain at least one of: user_id, agent_id, run_id")
	}
	return filters, nil
}

// validateEntityID 拒绝空白包裹和内部空白，保持 scope key 可安全索引。
func validateEntityID(value string, name string) error {
	if value == "" {
		return nil
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("invalid %s: cannot contain leading or trailing whitespace", name)
	}
	if strings.ContainsFunc(value, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
		return fmt.Errorf("invalid %s: cannot contain whitespace", name)
	}
	return nil
}

// buildSessionScope 生成 last messages 使用的确定性 session scope。
func buildSessionScope(filters map[string]any) string {
	var parts []string
	for _, key := range []string{"agent_id", "run_id", "user_id"} {
		value := strings.TrimSpace(asString(filters[key]))
		if value != "" {
			parts = append(parts, key+"="+value)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

// matchesMetadataFilters 对非 scope metadata 做轻量精确/contains 过滤。
func matchesMetadataFilters(memory storedMemory, filters map[string]any) bool {
	for key, expected := range filters {
		if key == "user_id" || key == "agent_id" || key == "run_id" {
			continue
		}
		actual := promotedOrMetadataValue(memory, key)
		if !filterValueMatches(actual, expected) {
			return false
		}
	}
	return true
}

// promotedOrMetadataValue 优先读取独立列，再回退到 metadata JSON。
func promotedOrMetadataValue(memory storedMemory, key string) any {
	switch key {
	case "actor_id":
		return memory.ActorID
	case "role":
		return memory.Role
	default:
		return memory.Metadata[key]
	}
}

// filterValueMatches 支持 mem0 常用的等值、contains、in 和通配过滤。
func filterValueMatches(actual any, expected any) bool {
	if asString(expected) == "*" {
		return true
	}
	expectedMap, ok := expected.(map[string]any)
	if !ok {
		return asString(actual) == asString(expected)
	}
	for op, value := range expectedMap {
		switch op {
		case "eq":
			if asString(actual) != asString(value) {
				return false
			}
		case "ne":
			if asString(actual) == asString(value) {
				return false
			}
		case "contains", "icontains":
			actualText := asString(actual)
			valueText := asString(value)
			if op == "icontains" {
				actualText = strings.ToLower(actualText)
				valueText = strings.ToLower(valueText)
			}
			if !strings.Contains(actualText, valueText) {
				return false
			}
		case "in":
			if !containsAnyString(value, asString(actual)) {
				return false
			}
		case "nin":
			if containsAnyString(value, asString(actual)) {
				return false
			}
		case "gt", "gte", "lt", "lte":
			if !compareFilterNumber(actual, value, op) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// containsAnyString 判断 filter in 列表是否包含目标字符串。
func containsAnyString(values any, target string) bool {
	switch typed := values.(type) {
	case []any:
		for _, item := range typed {
			if asString(item) == target {
				return true
			}
		}
	case []string:
		for _, item := range typed {
			if item == target {
				return true
			}
		}
	}
	return false
}

// compareFilterNumber 执行 gt/gte/lt/lte 元数据过滤，无法转成数字时直接不匹配。
func compareFilterNumber(actual any, expected any, op string) bool {
	actualNumber, ok := asFloat64(actual)
	if !ok {
		return false
	}
	expectedNumber, ok := asFloat64(expected)
	if !ok {
		return false
	}
	switch op {
	case "gt":
		return actualNumber > expectedNumber
	case "gte":
		return actualNumber >= expectedNumber
	case "lt":
		return actualNumber < expectedNumber
	case "lte":
		return actualNumber <= expectedNumber
	default:
		return false
	}
}

// asFloat64 将 JSON/工具传入的数字类型归一化，服务 search 元数据比较。
func asFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		parsed, err := strconv.ParseFloat(asString(typed), 64)
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

// formatSearchResult 将 SQLite 内部 payload 转成 mem0 search 的外部返回形态。
func formatSearchResult(memory storedMemory, score float64) SearchResult {
	metadata := cloneMetadata(memory.Metadata)
	for _, key := range []string{
		"data",
		"hash",
		"created_at",
		"updated_at",
		"id",
		"text_lemmatized",
		"user_id",
		"agent_id",
		"run_id",
		"actor_id",
		"role",
	} {
		delete(metadata, key)
	}
	if len(metadata) == 0 {
		metadata = nil
	}
	return SearchResult{
		ID:        memory.ID,
		Memory:    memory.Memory,
		Hash:      memory.Hash,
		UserID:    memory.UserID,
		AgentID:   memory.AgentID,
		RunID:     memory.RunID,
		ActorID:   memory.ActorID,
		Role:      memory.Role,
		CreatedAt: memory.CreatedAt,
		UpdatedAt: memory.UpdatedAt,
		Score:     score,
		Metadata:  metadata,
	}
}

// memoryHash 复刻 mem0 源码中用 md5 对 memory text 做去重的策略。
func memoryHash(text string) string {
	sum := md5.Sum([]byte(text))
	return hex.EncodeToString(sum[:])
}

// cloneMetadata 复制 metadata，避免 add/search 内部补字段时污染调用方 map。
func cloneMetadata(input map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range input {
		out[key] = value
	}
	return out
}

// asString 将 tool/JSON 传入的弱类型值安全归一成字符串。
func asString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}
