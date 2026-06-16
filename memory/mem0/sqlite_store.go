package mem0

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// SQLiteStore 负责把 mem0 的向量 payload、历史事件和原始消息持久化到 SQLite。
type SQLiteStore struct {
	db *sql.DB
}

// storedMemory 是 SQLite 内部保存的完整 memory payload。
type storedMemory struct {
	ID             string
	Memory         string
	Hash           string
	Embedding      []float64
	Metadata       map[string]any
	UserID         string
	AgentID        string
	RunID          string
	ActorID        string
	Role           string
	TextLemmatized string
	CreatedAt      string
	UpdatedAt      string
}

// openSQLiteStore 打开 SQLite 文件并初始化 mem0 demo 需要的三张表。
func openSQLiteStore(ctx context.Context, path string) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		path = "memory/mem0/mem0.sqlite"
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
	store := &SQLiteStore{db: db}
	if err := store.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// init 建表并创建常用 scope/hash 索引，保证 add/search 的基础路径稳定。
func (s *SQLiteStore) init(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS memories (
			id TEXT PRIMARY KEY,
			memory TEXT NOT NULL,
			hash TEXT NOT NULL,
			embedding_json TEXT NOT NULL,
			metadata_json TEXT NOT NULL,
			user_id TEXT,
			agent_id TEXT,
			run_id TEXT,
			actor_id TEXT,
			role TEXT,
			text_lemmatized TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_scope ON memories(user_id, agent_id, run_id)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_hash_scope ON memories(hash, user_id, agent_id, run_id)`,
		`CREATE TABLE IF NOT EXISTS memory_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			memory_id TEXT NOT NULL,
			old_memory TEXT,
			new_memory TEXT,
			event TEXT NOT NULL,
			created_at TEXT NOT NULL,
			is_deleted INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS memory_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_scope TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			name TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_memory_messages_scope ON memory_messages(session_scope, id DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// Close 释放 SQLite 连接。
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// insertMemory 写入一条 memory 及其向量 payload。
func (s *SQLiteStore) insertMemory(ctx context.Context, memory storedMemory) error {
	embeddingJSON, err := json.Marshal(memory.Embedding)
	if err != nil {
		return err
	}
	metadataJSON, err := json.Marshal(memory.Metadata)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO memories (
		id, memory, hash, embedding_json, metadata_json,
		user_id, agent_id, run_id, actor_id, role, text_lemmatized, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		memory.ID,
		memory.Memory,
		memory.Hash,
		string(embeddingJSON),
		string(metadataJSON),
		memory.UserID,
		memory.AgentID,
		memory.RunID,
		memory.ActorID,
		memory.Role,
		memory.TextLemmatized,
		memory.CreatedAt,
		memory.UpdatedAt,
	)
	return err
}

// addHistory 记录 memory 的 ADD 事件，方便后续检查 add 写入链路。
func (s *SQLiteStore) addHistory(ctx context.Context, memoryID string, oldMemory *string, newMemory string, event string, createdAt string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO memory_history (
		memory_id, old_memory, new_memory, event, created_at, is_deleted
	) VALUES (?, ?, ?, ?, ?, 0)`, memoryID, oldMemory, newMemory, event, createdAt)
	return err
}

// listMemories 按 user/agent/run scope 粗筛候选，剩余元数据过滤由上层执行。
func (s *SQLiteStore) listMemories(ctx context.Context, filters map[string]any) ([]storedMemory, error) {
	query := `SELECT id, memory, hash, embedding_json, metadata_json, user_id, agent_id, run_id, actor_id, role, text_lemmatized, created_at, updated_at FROM memories`
	where, args := scopeWhere(filters)
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY id DESC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []storedMemory
	for rows.Next() {
		var memory storedMemory
		var embeddingJSON string
		var metadataJSON string
		if err := rows.Scan(
			&memory.ID,
			&memory.Memory,
			&memory.Hash,
			&embeddingJSON,
			&metadataJSON,
			&memory.UserID,
			&memory.AgentID,
			&memory.RunID,
			&memory.ActorID,
			&memory.Role,
			&memory.TextLemmatized,
			&memory.CreatedAt,
			&memory.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(embeddingJSON), &memory.Embedding); err != nil {
			return nil, fmt.Errorf("decode embedding for memory %s: %w", memory.ID, err)
		}
		if err := json.Unmarshal([]byte(metadataJSON), &memory.Metadata); err != nil {
			return nil, fmt.Errorf("decode metadata for memory %s: %w", memory.ID, err)
		}
		memories = append(memories, memory)
	}
	return memories, rows.Err()
}

// existingHashes 返回当前 scope 下已存在的 memory hash，用于 infer=true 的冲突去重。
func (s *SQLiteStore) existingHashes(ctx context.Context, filters map[string]any) (map[string]bool, error) {
	memories, err := s.listMemories(ctx, filters)
	if err != nil {
		return nil, err
	}
	hashes := make(map[string]bool, len(memories))
	for _, memory := range memories {
		if memory.Hash != "" {
			hashes[memory.Hash] = true
		}
	}
	return hashes, nil
}

// saveMessages 保存原始 messages，供下一次 add 时作为 last_k_messages 输入给 LLM。
func (s *SQLiteStore) saveMessages(ctx context.Context, sessionScope string, messages []Message, now time.Time) error {
	if strings.TrimSpace(sessionScope) == "" || len(messages) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO memory_messages (
		session_scope, role, content, name, created_at
	) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, message := range messages {
		if strings.TrimSpace(message.Role) == "" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, sessionScope, message.Role, message.Content, message.Name, now.Format(time.RFC3339)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// lastMessages 读取当前 session 最近消息，复刻 mem0 add 的上下文收集阶段。
func (s *SQLiteStore) lastMessages(ctx context.Context, sessionScope string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `SELECT role, content, COALESCE(name, '') FROM memory_messages
		WHERE session_scope = ? ORDER BY id DESC LIMIT ?`, sessionScope, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reversed []Message
	for rows.Next() {
		var message Message
		if err := rows.Scan(&message.Role, &message.Content, &message.Name); err != nil {
			return nil, err
		}
		reversed = append(reversed, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	messages := make([]Message, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		messages = append(messages, reversed[i])
	}
	return messages, nil
}

// scopeWhere 把实体 scope 转换成 SQLite where 条件。
func scopeWhere(filters map[string]any) ([]string, []any) {
	var where []string
	var args []any
	for _, key := range []string{"user_id", "agent_id", "run_id"} {
		value := strings.TrimSpace(asString(filters[key]))
		if value == "" {
			continue
		}
		where = append(where, key+" = ?")
		args = append(args, value)
	}
	return where, args
}
