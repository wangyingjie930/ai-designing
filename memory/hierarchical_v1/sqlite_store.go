package hierarchicalv1

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

// sqliteStore 持久化 policy/project/user/task 层记忆；scratchpad 保持进程内临时态。
type sqliteStore struct {
	db   *sql.DB
	path string
}

// openSQLiteStore 打开 SQLite 文件，并初始化 hierarchical_v1 memory 表。
func openSQLiteStore(ctx context.Context, path string) (*sqliteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("sqlite db path is required")
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
	store := &sqliteStore{db: db, path: path}
	if err := store.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// init 建表并建立 layer/source 索引，保证恢复和排查路径稳定。
func (s *sqliteStore) init(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS hierarchical_v1_memories (
			key TEXT PRIMARY KEY,
			value_json TEXT NOT NULL,
			layer TEXT NOT NULL,
			source TEXT NOT NULL,
			evidence_refs_json TEXT NOT NULL,
			confidence REAL NOT NULL,
			token_estimate INTEGER NOT NULL,
			valid_from TEXT NOT NULL,
			valid_until TEXT,
			created_at TEXT NOT NULL,
			last_accessed_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_hierarchical_v1_layer ON hierarchical_v1_memories(layer)`,
		`CREATE INDEX IF NOT EXISTS idx_hierarchical_v1_source ON hierarchical_v1_memories(source)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// Close 释放 SQLite 连接。
func (s *sqliteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path 返回 SQLite 文件路径，供 CLI 和 trace 输出。
func (s *sqliteStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// upsertEntry 保存一条非 scratchpad 记忆，使用 key 做幂等覆盖。
func (s *sqliteStore) upsertEntry(ctx context.Context, entry MemoryEntry) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	if entry.Layer == MemoryLayerScratchpad {
		return nil
	}
	valueJSON, err := json.Marshal(entry.Value)
	if err != nil {
		return err
	}
	evidenceJSON, err := json.Marshal(entry.EvidenceRefs)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO hierarchical_v1_memories (
		key, value_json, layer, source, evidence_refs_json, confidence, token_estimate,
		valid_from, valid_until, created_at, last_accessed_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(key) DO UPDATE SET
		value_json = excluded.value_json,
		layer = excluded.layer,
		source = excluded.source,
		evidence_refs_json = excluded.evidence_refs_json,
		confidence = excluded.confidence,
		token_estimate = excluded.token_estimate,
		valid_from = excluded.valid_from,
		valid_until = excluded.valid_until,
		created_at = excluded.created_at,
		last_accessed_at = excluded.last_accessed_at`,
		entry.Key,
		string(valueJSON),
		string(entry.Layer),
		string(entry.Source),
		string(evidenceJSON),
		entry.Confidence,
		entry.TokenEstimate,
		entry.ValidFrom,
		nullableString(entry.ValidUntil),
		entry.CreatedAt,
		nullableString(entry.LastAccessedAt),
	)
	return err
}

// loadEntries 启动时恢复已持久化的非 scratchpad 记忆。
func (s *sqliteStore) loadEntries(ctx context.Context) ([]MemoryEntry, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		key, value_json, layer, source, evidence_refs_json, confidence, token_estimate,
		valid_from, valid_until, created_at, last_accessed_at
		FROM hierarchical_v1_memories
		ORDER BY key ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []MemoryEntry
	for rows.Next() {
		var entry MemoryEntry
		var valueJSON string
		var layer string
		var source string
		var evidenceJSON string
		var validUntil sql.NullString
		var lastAccessed sql.NullString
		if err := rows.Scan(
			&entry.Key,
			&valueJSON,
			&layer,
			&source,
			&evidenceJSON,
			&entry.Confidence,
			&entry.TokenEstimate,
			&entry.ValidFrom,
			&validUntil,
			&entry.CreatedAt,
			&lastAccessed,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(valueJSON), &entry.Value); err != nil {
			return nil, fmt.Errorf("decode value for key %s: %w", entry.Key, err)
		}
		if err := json.Unmarshal([]byte(evidenceJSON), &entry.EvidenceRefs); err != nil {
			return nil, fmt.Errorf("decode evidence for key %s: %w", entry.Key, err)
		}
		entry.Layer = MemoryLayer(layer)
		entry.Source = MemorySource(source)
		if validUntil.Valid {
			entry.ValidUntil = &validUntil.String
		}
		if lastAccessed.Valid {
			entry.LastAccessedAt = &lastAccessed.String
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// nullableString 把可空字符串指针转换成 database/sql 可识别的空值。
func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
