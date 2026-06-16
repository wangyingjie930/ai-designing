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

// init 建表并建立常用索引；embedding_json 保持 JSON 形态，方便本地可视化和迁移。
func (s *SQLiteStore) init(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS failure_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			context TEXT NOT NULL,
			error_type TEXT NOT NULL,
			error_message TEXT NOT NULL,
			fix TEXT NOT NULL,
			heuristic TEXT NOT NULL,
			tags_json TEXT NOT NULL,
			timestamp REAL NOT NULL,
			search_text TEXT NOT NULL,
			embedding_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_failure_entries_timestamp ON failure_entries(timestamp)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
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
func (s *SQLiteStore) Append(ctx context.Context, entry FailureEntry, searchText string, embedding []float64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	tagsJSON, err := json.Marshal(entry.Tags)
	if err != nil {
		return err
	}
	embeddingJSON, err := json.Marshal(embedding)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO failure_entries (
		context, error_type, error_message, fix, heuristic, tags_json, timestamp, search_text, embedding_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.Context,
		entry.ErrorType,
		entry.ErrorMessage,
		entry.Fix,
		entry.Heuristic,
		string(tagsJSON),
		entry.Timestamp,
		searchText,
		string(embeddingJSON),
	)
	return err
}

// Search 扫描 SQLite 中的 embedding_json 并返回分数最高的候选。
func (s *SQLiteStore) Search(ctx context.Context, query string, queryVector []float64, topK int) ([]FailureMatch, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		context, error_type, error_message, fix, heuristic, tags_json, timestamp, search_text, embedding_json
		FROM failure_entries ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []FailureMatch
	for rows.Next() {
		var entry FailureEntry
		var tagsJSON string
		var searchText string
		var embeddingJSON string
		if err := rows.Scan(
			&entry.Context,
			&entry.ErrorType,
			&entry.ErrorMessage,
			&entry.Fix,
			&entry.Heuristic,
			&tagsJSON,
			&entry.Timestamp,
			&searchText,
			&embeddingJSON,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tagsJSON), &entry.Tags); err != nil {
			return nil, fmt.Errorf("decode tags: %w", err)
		}
		var vector []float64
		if err := json.Unmarshal([]byte(embeddingJSON), &vector); err != nil {
			return nil, fmt.Errorf("decode embedding: %w", err)
		}
		matches = append(matches, scoreFailureMatch(query, queryVector, searchText, entry, vector))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return topMatches(matches, topK), nil
}

// Count 返回当前 SQLite 中已沉淀的失败经验数量。
func (s *SQLiteStore) Count(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("sqlite store is not initialized")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM failure_entries`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
