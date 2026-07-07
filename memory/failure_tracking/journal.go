package failuretracking

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// FailureJournal 负责记录、审查和召回结构化失败经验。
type FailureJournal struct {
	db             *SQLiteStore
	embedder       Embedder
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
	store, err := openSQLiteStore(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	return &FailureJournal{
		db:             store,
		embedder:       embedder,
		scoreThreshold: threshold,
		now:            now,
	}, nil
}

// Record 写入一条失败日记。未审查条目会保留为 draft，不会进入召回。
func (j *FailureJournal) Record(ctx context.Context, req RecordRequest) (*RecordResponse, error) {
	if j == nil {
		return nil, errors.New("failure journal is nil")
	}
	entry, err := j.normalizeAndValidateEntry(req.Entry)
	if err != nil {
		return nil, err
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

// RecallBeforeTool 在任务规划或高风险工具调用前召回 approved 失败经验。
func (j *FailureJournal) RecallBeforeTool(ctx context.Context, req RecallRequest) (*RecallResponse, error) {
	if j == nil {
		return nil, errors.New("failure journal is nil")
	}
	req = normalizeRecallRequest(req)
	if req.TaskFamily == "" && req.Tool == "" && len(req.MechanicalKeys) == 0 && len(req.Categories) == 0 && req.Query == "" {
		return nil, errors.New("recall trigger is required")
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	queryText := recallQueryText(req)
	queryVector, err := j.embedder.Embed(ctx, queryText)
	if err != nil {
		return nil, err
	}
	matches, err := j.db.SearchRecall(ctx, req, queryText, queryVector, req.TopK)
	if err != nil {
		return nil, err
	}
	filtered := make([]FailureMatch, 0, len(matches))
	for _, match := range matches {
		if match.StructuredScore > 0 || match.Score >= j.scoreThreshold {
			filtered = append(filtered, match)
		}
	}
	for idx := range filtered {
		updated, err := j.db.IncrementRecalledCount(ctx, filtered[idx].Entry.FailureID)
		if err != nil {
			return nil, err
		}
		filtered[idx].Entry.RecalledCount = updated
	}
	return &RecallResponse{Matches: filtered}, nil
}

// Review 更新失败日记的审查状态。approved 条目必须带 reviewer，防止未确认经验直接污染召回库。
func (j *FailureJournal) Review(ctx context.Context, req ReviewRequest) (*ReviewResponse, error) {
	if j == nil {
		return nil, errors.New("failure journal is nil")
	}
	req.FailureID = strings.TrimSpace(req.FailureID)
	if req.FailureID == "" {
		return nil, errors.New("failure_id is required")
	}
	if !validStatus(req.Status) || req.Status == StatusDraft {
		return nil, fmt.Errorf("review status must be one of %s, %s, %s", StatusNeedsReview, StatusApproved, StatusArchived)
	}
	if req.Status == StatusApproved && strings.TrimSpace(req.ReviewedBy) == "" {
		return nil, errors.New("reviewed_by is required when status is approved")
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	entry, err := j.db.Get(ctx, req.FailureID)
	if err != nil {
		return nil, err
	}
	entry.Status = req.Status
	if value := strings.TrimSpace(req.ReviewedBy); value != "" {
		entry.ReviewedBy = value
	}
	if value := strings.TrimSpace(req.ReviewNote); value != "" {
		entry.ReviewNote = value
	}
	if value := strings.TrimSpace(req.RetentionTier); value != "" {
		entry.RetentionTier = value
	}
	entry.UpdatedAt = unixSeconds(j.now())

	searchText := entrySearchText(entry)
	vector, err := j.embedder.Embed(ctx, searchText)
	if err != nil {
		return nil, err
	}
	if err := j.db.Update(ctx, entry, searchText, vector); err != nil {
		return nil, err
	}
	return &ReviewResponse{Entry: entry}, nil
}

// Get 按 failure_id 读取结构化失败日记，便于测试、审计和排查。
func (j *FailureJournal) Get(ctx context.Context, failureID string) (FailureEntry, error) {
	if j == nil {
		return FailureEntry{}, errors.New("failure journal is nil")
	}
	failureID = strings.TrimSpace(failureID)
	if failureID == "" {
		return FailureEntry{}, errors.New("failure_id is required")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.db.Get(ctx, failureID)
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

func (j *FailureJournal) normalizeAndValidateEntry(entry FailureEntry) (FailureEntry, error) {
	entry.FailureID = strings.TrimSpace(entry.FailureID)
	if entry.FailureID == "" {
		entry.FailureID = "fj-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	}
	entry.TaskFamily = strings.TrimSpace(entry.TaskFamily)
	entry.Category = strings.TrimSpace(entry.Category)
	entry.Severity = strings.TrimSpace(entry.Severity)
	entry.TenantScope = strings.TrimSpace(entry.TenantScope)
	entry.Source = strings.TrimSpace(entry.Source)
	entry.Symptom = strings.TrimSpace(entry.Symptom)
	entry.RootCause = strings.TrimSpace(entry.RootCause)
	entry.RetentionTier = strings.TrimSpace(entry.RetentionTier)
	entry.ReviewedBy = strings.TrimSpace(entry.ReviewedBy)
	entry.ReviewNote = strings.TrimSpace(entry.ReviewNote)
	if entry.Status == "" {
		entry.Status = StatusDraft
	}
	if entry.Severity == "" {
		entry.Severity = "medium"
	}
	if entry.Source == "" {
		entry.Source = "agent_generated_draft"
	}
	if entry.RetentionTier == "" {
		entry.RetentionTier = "hot"
	}
	now := unixSeconds(j.now())
	if entry.CreatedAt == 0 {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now
	entry.Repair = normalizeStringList(entry.Repair)
	entry.Lesson = normalizeStringList(entry.Lesson)
	entry.DoNot = normalizeStringList(entry.DoNot)
	entry.Evidence = normalizeEvidence(entry.Evidence)
	entry.Recall = normalizeRecallTrigger(entry.Recall)

	if err := validateFailureEntry(entry); err != nil {
		return FailureEntry{}, err
	}
	return entry, nil
}

func validateFailureEntry(entry FailureEntry) error {
	if entry.TaskFamily == "" {
		return errors.New("task_family is required")
	}
	if !validBoundary(entry.Boundary) {
		return fmt.Errorf("boundary must be one of %s, %s, %s, %s", BoundaryHard, BoundaryGate, BoundarySemantic, BoundarySafety)
	}
	if entry.Category == "" {
		return errors.New("category is required")
	}
	if !validStatus(entry.Status) {
		return fmt.Errorf("status must be one of %s, %s, %s, %s", StatusDraft, StatusNeedsReview, StatusApproved, StatusArchived)
	}
	if entry.Status == StatusApproved && entry.ReviewedBy == "" {
		return errors.New("reviewed_by is required when status is approved")
	}
	if entry.Symptom == "" {
		return errors.New("symptom is required")
	}
	if entry.RootCause == "" {
		return errors.New("root_cause is required")
	}
	if len(entry.Repair) == 0 {
		return errors.New("repair is required")
	}
	if len(entry.Lesson) == 0 {
		return errors.New("lesson is required")
	}
	if len(entry.DoNot) == 0 {
		return errors.New("do_not is required")
	}
	if !hasEvidence(entry.Evidence) {
		return errors.New("evidence bundle is required")
	}
	if !hasRecallTrigger(entry.Recall) {
		return errors.New("recall trigger is required")
	}
	if err := validateRecallGrounding(entry); err != nil {
		return err
	}
	return nil
}

func validBoundary(boundary Boundary) bool {
	switch boundary {
	case BoundaryHard, BoundaryGate, BoundarySemantic, BoundarySafety:
		return true
	default:
		return false
	}
}

func validStatus(status Status) bool {
	switch status {
	case StatusDraft, StatusNeedsReview, StatusApproved, StatusArchived:
		return true
	default:
		return false
	}
}

func normalizeEvidence(evidence EvidenceBundle) EvidenceBundle {
	return EvidenceBundle{
		WorkspaceRefs:   normalizeStringList(evidence.WorkspaceRefs),
		NarrativeRefs:   normalizeStringList(evidence.NarrativeRefs),
		StateRefs:       normalizeStringList(evidence.StateRefs),
		ObservationRefs: normalizeStringList(evidence.ObservationRefs),
	}
}

func normalizeRecallTrigger(trigger RecallTrigger) RecallTrigger {
	return RecallTrigger{
		TaskFamilies:   normalizeStringList(trigger.TaskFamilies),
		Tools:          normalizeStringList(trigger.Tools),
		MechanicalKeys: normalizeStringList(trigger.MechanicalKeys),
		Categories:     normalizeStringList(trigger.Categories),
	}
}

func normalizeRecallRequest(req RecallRequest) RecallRequest {
	req.TaskFamily = strings.TrimSpace(req.TaskFamily)
	req.Tool = strings.TrimSpace(req.Tool)
	req.Query = strings.TrimSpace(req.Query)
	req.MechanicalKeys = normalizeStringList(req.MechanicalKeys)
	req.Categories = normalizeStringList(req.Categories)
	if req.TopK <= 0 {
		req.TopK = defaultTopK
	}
	return req
}

func normalizeStringList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = appendUnique(out, value)
	}
	return out
}

func appendUnique(values []string, next string) []string {
	next = strings.TrimSpace(next)
	if next == "" {
		return values
	}
	for _, value := range values {
		if strings.EqualFold(value, next) {
			return values
		}
	}
	return append(values, next)
}

func hasEvidence(evidence EvidenceBundle) bool {
	return len(evidence.WorkspaceRefs)+len(evidence.NarrativeRefs)+len(evidence.StateRefs)+len(evidence.ObservationRefs) > 0
}

func hasRecallTrigger(trigger RecallTrigger) bool {
	return len(trigger.TaskFamilies)+len(trigger.Tools)+len(trigger.MechanicalKeys)+len(trigger.Categories) > 0
}

func validateRecallGrounding(entry FailureEntry) error {
	if len(entry.Recall.MechanicalKeys) > 0 && !hasGroundedRecallValue(entry.Recall.MechanicalKeys, entry.Evidence.StateRefs) {
		return errors.New("mechanical key recall trigger must be grounded by evidence.state_refs")
	}
	if len(entry.Recall.Tools) > 0 {
		toolEvidence := append([]string{}, entry.Evidence.WorkspaceRefs...)
		toolEvidence = append(toolEvidence, entry.Evidence.ObservationRefs...)
		if !hasGroundedRecallValue(entry.Recall.Tools, toolEvidence) {
			return errors.New("tool recall trigger must be grounded by workspace or observation evidence")
		}
	}
	return nil
}

func hasGroundedRecallValue(values []string, evidenceRefs []string) bool {
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		for _, evidenceRef := range evidenceRefs {
			if strings.Contains(strings.ToLower(evidenceRef), value) {
				return true
			}
		}
	}
	return false
}

func recallQueryText(req RecallRequest) string {
	if req.Query != "" {
		return req.Query
	}
	return strings.Join([]string{
		req.TaskFamily,
		req.Tool,
		strings.Join(req.MechanicalKeys, " "),
		strings.Join(req.Categories, " "),
	}, " | ")
}

// unixSeconds 用 float64 秒对齐已有 SQLite 时间戳形态。
func unixSeconds(t time.Time) float64 {
	return float64(t.UnixNano()) / float64(time.Second)
}
