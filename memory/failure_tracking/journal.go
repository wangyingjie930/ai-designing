package failuretracking

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// FailureJournal 是 append-only failure log，负责记录经验并从 SQLite 召回相似失败。
type FailureJournal struct {
	db             *SQLiteStore
	embedder       Embedder
	generator      LessonGenerator
	scoreThreshold float64
	now            func() time.Time
	mu             sync.Mutex
}

// NewFailureJournal 初始化 SQLite + embedding 版 failure journal。
func NewFailureJournal(ctx context.Context, config Config) (*FailureJournal, error) {
	dbPath := strings.TrimSpace(config.DBPath)
	if dbPath == "" {
		return nil, errors.New("db path is required")
	}
	generator := config.Generator
	if generator == nil {
		generator = LocalLessonGenerator{}
	}
	embedder := config.Embedder
	if embedder == nil {
		embedder = NewHashEmbedder(defaultEmbeddingDimension)
	}
	threshold := config.ScoreThreshold
	if threshold <= 0 {
		threshold = defaultScoreThreshold
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	journal := &FailureJournal{
		embedder:       embedder,
		generator:      generator,
		scoreThreshold: threshold,
		now:            now,
	}
	store, err := openSQLiteStore(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	journal.db = store
	return journal, nil
}

// Record 记录一次失败，并自动生成 error_type、heuristic 和 tags。
func (j *FailureJournal) Record(ctx context.Context, req RecordRequest) (*RecordResponse, error) {
	if j == nil {
		return nil, errors.New("failure journal is nil")
	}
	req.Context = strings.TrimSpace(req.Context)
	req.Error = strings.TrimSpace(req.Error)
	req.Fix = strings.TrimSpace(req.Fix)
	if req.Context == "" {
		return nil, errors.New("context is required")
	}
	if req.Error == "" {
		return nil, errors.New("error is required")
	}
	if req.Fix == "" {
		return nil, errors.New("fix is required")
	}

	heuristic, err := j.generator.GenerateHeuristic(ctx, req.Context, req.Error, req.Fix)
	if err != nil {
		return nil, err
	}
	tags, err := j.generator.GenerateTags(ctx, req.Context, req.Error)
	if err != nil {
		return nil, err
	}
	entry := FailureEntry{
		Context:      req.Context,
		ErrorType:    classifyError(req.Error),
		ErrorMessage: req.Error,
		Fix:          req.Fix,
		Heuristic:    strings.TrimSpace(heuristic),
		Tags:         normalizeTags(tags),
		Timestamp:    unixSeconds(j.now()),
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	searchText := entrySearchText(entry)
	vector, err := j.embedder.Embed(ctx, searchText)
	if err != nil {
		return nil, err
	}
	if err := j.db.Append(ctx, entry, searchText, vector); err != nil {
		return nil, err
	}
	return &RecordResponse{Entry: entry}, nil
}

// Consult 在行动前查询相似失败经验，并过滤掉低于阈值的弱相关候选。
func (j *FailureJournal) Consult(ctx context.Context, req ConsultRequest) (*ConsultResponse, error) {
	if j == nil {
		return nil, errors.New("failure journal is nil")
	}
	req.CurrentContext = strings.TrimSpace(req.CurrentContext)
	if req.CurrentContext == "" {
		return nil, errors.New("current_context is required")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	queryVector, err := j.embedder.Embed(ctx, req.CurrentContext)
	if err != nil {
		return nil, err
	}
	candidates, err := j.db.Search(ctx, req.CurrentContext, queryVector, req.TopK)
	if err != nil {
		return nil, err
	}
	matches := make([]FailureMatch, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Score >= j.scoreThreshold {
			matches = append(matches, candidate)
		}
	}
	return &ConsultResponse{Matches: matches}, nil
}

// Path 返回当前 SQLite 路径，便于 demo 输出和排查。
func (j *FailureJournal) Path() string {
	if j == nil || j.db == nil {
		return ""
	}
	return j.db.Path()
}

// Close 释放底层数据库连接。
func (j *FailureJournal) Close() error {
	if j == nil || j.db == nil {
		return nil
	}
	return j.db.Close()
}

// Count 返回当前已沉淀的失败经验数量。
func (j *FailureJournal) Count(ctx context.Context) (int, error) {
	if j == nil {
		return 0, errors.New("failure journal is nil")
	}
	if j.db == nil {
		return 0, errors.New("sqlite store is nil")
	}
	return j.db.Count(ctx)
}

// classifyError 复刻 Python _classify 的前缀匹配逻辑。
func classifyError(errorMessage string) string {
	for _, prefix := range []string{
		"TypeError",
		"ValueError",
		"ImportError",
		"ModuleNotFoundError",
		"KeyError",
		"FileNotFoundError",
		"TimeoutError",
	} {
		if strings.Contains(errorMessage, prefix) {
			return prefix
		}
	}
	return "UnclassifiedError"
}

// normalizeTags 清理标签空白、去重，并限制在 5 个以内。
func normalizeTags(tags []string) []string {
	var out []string
	for _, tag := range tags {
		out = appendUnique(out, tag)
		if len(out) >= 5 {
			break
		}
	}
	return out
}

// unixSeconds 用 float64 秒对齐 Python time.time() 的 timestamp 形态。
func unixSeconds(t time.Time) float64 {
	return float64(t.UnixNano()) / float64(time.Second)
}
