package hierarchical

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

// sqliteVectorStore 把 long-term tier 的文本、真实 embedding 向量和元数据持久化到 SQLite。
type sqliteVectorStore struct {
	db       *sql.DB
	path     string
	scope    string
	embedder Embedder
}

// storedLongTermEntry 是 SQLite 内部读取时携带向量 payload 的完整实体。
type storedLongTermEntry struct {
	Entry       MemoryEntry
	Embedding   []float64
	ContentHash string
}

// openSQLiteVectorStore 打开 SQLite 文件，并初始化 hierarchical memory 所需表结构。
func openSQLiteVectorStore(ctx context.Context, dbPath string, scope string, embedder Embedder) (*sqliteVectorStore, error) {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return nil, fmt.Errorf("sqlite db path is required")
	}
	if embedder == nil {
		return nil, fmt.Errorf("real embedder is required")
	}
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}
	store := &sqliteVectorStore{
		db:       db,
		path:     dbPath,
		scope:    scope,
		embedder: embedder,
	}
	if err := store.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// init 建表并创建 scope/hash 索引，保证 consolidate 和 retrieve 都有稳定查询路径。
func (s *sqliteVectorStore) init(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS hierarchical_longterm_memories (
			id TEXT PRIMARY KEY,
			scope TEXT NOT NULL,
			content TEXT NOT NULL,
			source TEXT NOT NULL,
			importance REAL NOT NULL,
			token_count INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			last_accessed TEXT NOT NULL,
			access_count INTEGER NOT NULL,
			metadata_json TEXT NOT NULL,
			embedding_json TEXT NOT NULL,
			content_hash TEXT NOT NULL,
			UNIQUE(scope, content_hash)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_hierarchical_memories_scope ON hierarchical_longterm_memories(scope)`,
		`CREATE INDEX IF NOT EXISTS idx_hierarchical_memories_last_accessed ON hierarchical_longterm_memories(scope, last_accessed DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// close 释放 SQLite 连接。
func (s *sqliteVectorStore) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// storePath 返回底层 SQLite 文件路径，供 CLI 输出和排查使用。
func (s *sqliteVectorStore) storePath() string {
	if s == nil {
		return ""
	}
	return s.path
}

// upsertLongTerm 把重要 session/working 记忆写入 long-term tier，并用真实 embedding 更新向量。
func (s *sqliteVectorStore) upsertLongTerm(ctx context.Context, entry MemoryEntry, now time.Time) (MemoryEntry, error) {
	if s == nil || s.db == nil {
		return MemoryEntry{}, fmt.Errorf("sqlite vector store is not initialized")
	}
	entry.Content = strings.TrimSpace(entry.Content)
	entry.Source = strings.TrimSpace(entry.Source)
	if entry.Content == "" {
		return MemoryEntry{}, fmt.Errorf("memory content is empty")
	}
	if entry.Source == "" {
		return MemoryEntry{}, fmt.Errorf("memory source is empty")
	}
	vector, err := s.embedder.Embed(ctx, entry.Content)
	if err != nil {
		return MemoryEntry{}, err
	}
	if entry.ID == "" {
		entry.ID = uuid.NewString()
	}
	entry.Tier = MemoryTierLongTerm
	entry.Importance = clampImportance(entry.Importance)
	if entry.TokenCount <= 0 {
		entry.TokenCount = estimateTokenCount(entry.Content)
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	if entry.LastAccessed.IsZero() {
		entry.LastAccessed = entry.CreatedAt
	}
	metadataJSON, err := json.Marshal(entry.Metadata)
	if err != nil {
		return MemoryEntry{}, err
	}
	embeddingJSON, err := json.Marshal(vector)
	if err != nil {
		return MemoryEntry{}, err
	}
	hash := contentHash(s.scope, entry.Source, entry.Content)
	_, err = s.db.ExecContext(ctx, `INSERT INTO hierarchical_longterm_memories (
		id, scope, content, source, importance, token_count, created_at, last_accessed,
		access_count, metadata_json, embedding_json, content_hash
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(scope, content_hash) DO UPDATE SET
		content = excluded.content,
		source = excluded.source,
		importance = excluded.importance,
		token_count = excluded.token_count,
		last_accessed = excluded.last_accessed,
		access_count = excluded.access_count,
		metadata_json = excluded.metadata_json,
		embedding_json = excluded.embedding_json`,
		entry.ID,
		s.scope,
		entry.Content,
		entry.Source,
		entry.Importance,
		entry.TokenCount,
		entry.CreatedAt.UTC().Format(time.RFC3339Nano),
		entry.LastAccessed.UTC().Format(time.RFC3339Nano),
		entry.AccessCount,
		string(metadataJSON),
		string(embeddingJSON),
		hash,
	)
	if err != nil {
		return MemoryEntry{}, err
	}
	stored, err := s.loadByHash(ctx, hash)
	if err != nil {
		return MemoryEntry{}, err
	}
	return addTierName(stored.Entry), nil
}

// search 使用真实 query embedding 在 SQLite 候选中做余弦相似度排序。
func (s *sqliteVectorStore) search(ctx context.Context, query string, topK int, now time.Time) ([]SearchResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite vector store is not initialized")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is empty")
	}
	if topK <= 0 {
		topK = defaultRetrieveTopK
	}
	queryVector, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	candidates, err := s.listLongTerm(ctx)
	if err != nil {
		return nil, err
	}
	scored := make([]SearchResult, 0, len(candidates))
	for _, candidate := range candidates {
		score := cosineSimilarity(queryVector, candidate.Embedding)
		scored = append(scored, SearchResult{
			Entry: addTierName(candidate.Entry),
			Text:  candidate.Entry.Content,
			Score: score,
		})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})
	if topK < len(scored) {
		scored = scored[:topK]
	}
	if err := s.markAccessed(ctx, scored, now); err != nil {
		return nil, err
	}
	return scored, nil
}

// loadByHash 按幂等 hash 读取 upsert 后的实际 SQLite 行，保留老 ID 和创建时间。
func (s *sqliteVectorStore) loadByHash(ctx context.Context, hash string) (storedLongTermEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, content, source, importance, token_count, created_at, last_accessed,
		access_count, metadata_json, embedding_json, content_hash
		FROM hierarchical_longterm_memories
		WHERE scope = ? AND content_hash = ?
		LIMIT 1`, s.scope, hash)
	if err != nil {
		return storedLongTermEntry{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return storedLongTermEntry{}, fmt.Errorf("memory hash %s not found", hash)
	}
	entry, err := scanStoredLongTerm(rows)
	if err != nil {
		return storedLongTermEntry{}, err
	}
	return entry, rows.Err()
}

// listLongTerm 读取当前 scope 下全部长期记忆，向量排序由上层在进程内完成。
func (s *sqliteVectorStore) listLongTerm(ctx context.Context) ([]storedLongTermEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, content, source, importance, token_count, created_at, last_accessed,
		access_count, metadata_json, embedding_json, content_hash
		FROM hierarchical_longterm_memories
		WHERE scope = ?
		ORDER BY last_accessed DESC`, s.scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []storedLongTermEntry
	for rows.Next() {
		entry, err := scanStoredLongTerm(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// scanStoredLongTerm 将 SQLite 行还原为长期记忆实体和 embedding 向量。
func scanStoredLongTerm(rows interface {
	Scan(dest ...any) error
}) (storedLongTermEntry, error) {
	var entry MemoryEntry
	var createdAt string
	var lastAccessed string
	var metadataJSON string
	var embeddingJSON string
	var hash string
	if err := rows.Scan(
		&entry.ID,
		&entry.Content,
		&entry.Source,
		&entry.Importance,
		&entry.TokenCount,
		&createdAt,
		&lastAccessed,
		&entry.AccessCount,
		&metadataJSON,
		&embeddingJSON,
		&hash,
	); err != nil {
		return storedLongTermEntry{}, err
	}
	entry.Tier = MemoryTierLongTerm
	parsedCreatedAt, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return storedLongTermEntry{}, fmt.Errorf("parse created_at for memory %s: %w", entry.ID, err)
	}
	parsedLastAccessed, err := time.Parse(time.RFC3339Nano, lastAccessed)
	if err != nil {
		return storedLongTermEntry{}, fmt.Errorf("parse last_accessed for memory %s: %w", entry.ID, err)
	}
	entry.CreatedAt = parsedCreatedAt
	entry.LastAccessed = parsedLastAccessed
	if metadataJSON != "" && metadataJSON != "null" {
		if err := json.Unmarshal([]byte(metadataJSON), &entry.Metadata); err != nil {
			return storedLongTermEntry{}, fmt.Errorf("decode metadata for memory %s: %w", entry.ID, err)
		}
	}
	var vector []float64
	if err := json.Unmarshal([]byte(embeddingJSON), &vector); err != nil {
		return storedLongTermEntry{}, fmt.Errorf("decode embedding for memory %s: %w", entry.ID, err)
	}
	return storedLongTermEntry{
		Entry:       addTierName(entry),
		Embedding:   vector,
		ContentHash: hash,
	}, nil
}

// markAccessed 在召回后更新长期记忆访问统计，用于后续可观测性和新一轮评分参考。
func (s *sqliteVectorStore) markAccessed(ctx context.Context, results []SearchResult, now time.Time) error {
	if len(results) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	for _, result := range results {
		if strings.TrimSpace(result.Entry.ID) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hierarchical_longterm_memories
			SET access_count = access_count + 1, last_accessed = ?
			WHERE scope = ? AND id = ?`,
			now.UTC().Format(time.RFC3339Nano),
			s.scope,
			result.Entry.ID,
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
