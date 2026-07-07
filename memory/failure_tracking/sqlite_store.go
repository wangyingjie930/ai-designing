package failuretracking

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// SQLiteStore 用 SQLite 持久化 failure entry 和对应 embedding 向量。
type SQLiteStore struct {
	db   *sql.DB
	path string
}

// openSQLiteStore 打开 SQLite 数据库，并初始化 failure tracking 需要的表。
func openSQLiteStore(ctx context.Context, path string) (*SQLiteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("sqlite db path is empty")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	store := &SQLiteStore{db: db, path: path}
	if err := store.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// init 建表并建立常用索引；entry_json 保存完整六层结构，辅助列用于过滤和排序。
func (s *SQLiteStore) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS failure_entries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		failure_id TEXT DEFAULT '',
		task_family TEXT DEFAULT '',
		boundary TEXT DEFAULT '',
		category TEXT DEFAULT '',
		severity TEXT DEFAULT '',
		status TEXT DEFAULT '',
		tenant_scope TEXT DEFAULT '',
		source TEXT DEFAULT '',
		retention_tier TEXT DEFAULT '',
		entry_json TEXT DEFAULT '',
		recall_text TEXT DEFAULT '',
		embedding_json TEXT DEFAULT '',
		recalled_count INTEGER NOT NULL DEFAULT 0,
		created_at REAL NOT NULL DEFAULT 0,
		updated_at REAL NOT NULL DEFAULT 0
	)`); err != nil {
		return err
	}
	if err := s.ensureColumns(ctx); err != nil {
		return err
	}
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_failure_entries_failure_id ON failure_entries(failure_id) WHERE failure_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_failure_entries_status ON failure_entries(status)`,
		`CREATE INDEX IF NOT EXISTS idx_failure_entries_task_family ON failure_entries(task_family)`,
		`CREATE INDEX IF NOT EXISTS idx_failure_entries_category ON failure_entries(category)`,
		`CREATE INDEX IF NOT EXISTS idx_failure_entries_updated_at ON failure_entries(updated_at)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) ensureColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(failure_entries)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	exists := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		exists[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	missingColumns := []struct {
		name string
		sql  string
	}{
		{"failure_id", `ALTER TABLE failure_entries ADD COLUMN failure_id TEXT DEFAULT ''`},
		{"task_family", `ALTER TABLE failure_entries ADD COLUMN task_family TEXT DEFAULT ''`},
		{"boundary", `ALTER TABLE failure_entries ADD COLUMN boundary TEXT DEFAULT ''`},
		{"category", `ALTER TABLE failure_entries ADD COLUMN category TEXT DEFAULT ''`},
		{"severity", `ALTER TABLE failure_entries ADD COLUMN severity TEXT DEFAULT ''`},
		{"status", `ALTER TABLE failure_entries ADD COLUMN status TEXT DEFAULT ''`},
		{"tenant_scope", `ALTER TABLE failure_entries ADD COLUMN tenant_scope TEXT DEFAULT ''`},
		{"source", `ALTER TABLE failure_entries ADD COLUMN source TEXT DEFAULT ''`},
		{"retention_tier", `ALTER TABLE failure_entries ADD COLUMN retention_tier TEXT DEFAULT ''`},
		{"entry_json", `ALTER TABLE failure_entries ADD COLUMN entry_json TEXT DEFAULT ''`},
		{"recall_text", `ALTER TABLE failure_entries ADD COLUMN recall_text TEXT DEFAULT ''`},
		{"embedding_json", `ALTER TABLE failure_entries ADD COLUMN embedding_json TEXT DEFAULT ''`},
		{"recalled_count", `ALTER TABLE failure_entries ADD COLUMN recalled_count INTEGER NOT NULL DEFAULT 0`},
		{"created_at", `ALTER TABLE failure_entries ADD COLUMN created_at REAL NOT NULL DEFAULT 0`},
		{"updated_at", `ALTER TABLE failure_entries ADD COLUMN updated_at REAL NOT NULL DEFAULT 0`},
	}
	for _, column := range missingColumns {
		if exists[column.name] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, column.sql); err != nil {
			return err
		}
	}
	return nil
}

// Close 关闭 SQLite 连接。
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path 返回 SQLite 文件路径。
func (s *SQLiteStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Append 追加写入一条失败经验及其 embedding。
func (s *SQLiteStore) Append(ctx context.Context, entry FailureEntry, recallText string, embedding []float64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	entryJSON, embeddingJSON, err := encodeEntryAndEmbedding(entry, embedding)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO failure_entries (
		failure_id, task_family, boundary, category, severity, status, tenant_scope, source, retention_tier,
		entry_json, recall_text, embedding_json, recalled_count, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.FailureID,
		entry.TaskFamily,
		string(entry.Boundary),
		entry.Category,
		entry.Severity,
		string(entry.Status),
		entry.TenantScope,
		entry.Source,
		entry.RetentionTier,
		entryJSON,
		recallText,
		embeddingJSON,
		entry.RecalledCount,
		entry.CreatedAt,
		entry.UpdatedAt,
	)
	return err
}

// Update 覆盖一条已存在失败日记，主要用于 review 状态和召回次数变化。
func (s *SQLiteStore) Update(ctx context.Context, entry FailureEntry, recallText string, embedding []float64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	entryJSON, embeddingJSON, err := encodeEntryAndEmbedding(entry, embedding)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE failure_entries SET
		task_family = ?,
		boundary = ?,
		category = ?,
		severity = ?,
		status = ?,
		tenant_scope = ?,
		source = ?,
		retention_tier = ?,
		entry_json = ?,
		recall_text = ?,
		embedding_json = ?,
		recalled_count = ?,
		created_at = ?,
		updated_at = ?
		WHERE failure_id = ?`,
		entry.TaskFamily,
		string(entry.Boundary),
		entry.Category,
		entry.Severity,
		string(entry.Status),
		entry.TenantScope,
		entry.Source,
		entry.RetentionTier,
		entryJSON,
		recallText,
		embeddingJSON,
		entry.RecalledCount,
		entry.CreatedAt,
		entry.UpdatedAt,
		entry.FailureID,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("failure entry %q not found", entry.FailureID)
	}
	return nil
}

// SearchRecall 扫描 approved 失败日记，优先按结构化键打分，再用语义相似度辅助排序。
func (s *SQLiteStore) SearchRecall(ctx context.Context, req RecallRequest, query string, queryVector []float64, topK int) ([]FailureMatch, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT entry_json, recall_text, embedding_json
		FROM failure_entries
		WHERE status = ? AND entry_json != ''
		ORDER BY updated_at DESC, id DESC`, string(StatusApproved))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []FailureMatch
	for rows.Next() {
		var entryJSON string
		var recallText string
		var embeddingJSON string
		if err := rows.Scan(&entryJSON, &recallText, &embeddingJSON); err != nil {
			return nil, err
		}
		entry, vector, err := decodeEntryAndEmbedding(entryJSON, embeddingJSON)
		if err != nil {
			return nil, err
		}
		match := scoreRecallMatch(req, query, queryVector, recallText, entry, vector)
		if match.StructuredScore > 0 || match.SemanticScore > 0 {
			matches = append(matches, match)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return topMatches(matches, topK), nil
}

// Get 按 failure_id 读取一条失败日记。
func (s *SQLiteStore) Get(ctx context.Context, failureID string) (FailureEntry, error) {
	if s == nil || s.db == nil {
		return FailureEntry{}, fmt.Errorf("sqlite store is not initialized")
	}
	var entryJSON string
	if err := s.db.QueryRowContext(ctx, `SELECT entry_json FROM failure_entries WHERE failure_id = ?`, failureID).Scan(&entryJSON); err != nil {
		if err == sql.ErrNoRows {
			return FailureEntry{}, fmt.Errorf("failure entry %q not found", failureID)
		}
		return FailureEntry{}, err
	}
	var entry FailureEntry
	if err := json.Unmarshal([]byte(entryJSON), &entry); err != nil {
		return FailureEntry{}, fmt.Errorf("decode entry: %w", err)
	}
	return entry, nil
}

// IncrementRecalledCount 持久化召回次数，并返回更新后的计数。
func (s *SQLiteStore) IncrementRecalledCount(ctx context.Context, failureID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("sqlite store is not initialized")
	}
	entry, err := s.Get(ctx, failureID)
	if err != nil {
		return 0, err
	}
	entry.RecalledCount++
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE failure_entries SET recalled_count = ?, entry_json = ? WHERE failure_id = ?`,
		entry.RecalledCount,
		string(entryJSON),
		failureID,
	)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected == 0 {
		return 0, fmt.Errorf("failure entry %q not found", failureID)
	}
	return entry.RecalledCount, nil
}

// Count 返回当前 SQLite 中已沉淀的失败经验数量。
func (s *SQLiteStore) Count(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("sqlite store is not initialized")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM failure_entries WHERE entry_json != ''`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func encodeEntryAndEmbedding(entry FailureEntry, embedding []float64) (string, string, error) {
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return "", "", err
	}
	embeddingJSON, err := json.Marshal(embedding)
	if err != nil {
		return "", "", err
	}
	return string(entryJSON), string(embeddingJSON), nil
}

func decodeEntryAndEmbedding(entryJSON string, embeddingJSON string) (FailureEntry, []float64, error) {
	var entry FailureEntry
	if err := json.Unmarshal([]byte(entryJSON), &entry); err != nil {
		return FailureEntry{}, nil, fmt.Errorf("decode entry: %w", err)
	}
	var vector []float64
	if err := json.Unmarshal([]byte(embeddingJSON), &vector); err != nil {
		return FailureEntry{}, nil, fmt.Errorf("decode embedding: %w", err)
	}
	return entry, vector, nil
}
