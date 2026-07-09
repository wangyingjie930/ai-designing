package researchswarm

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

// Store 封装 research swarm 的 SQLite mailbox、任务和报告产物持久化。
type Store struct {
	db   *sql.DB
	path string
}

// OpenStore 打开 SQLite 数据库并初始化 schema。
func OpenStore(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("db path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path + "?_busy_timeout=5000&_journal_mode=WAL"
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, path: path}
	if err := store.init(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// Path 返回当前 store 的 SQLite 文件路径，供 leader 传给外部 worker 进程。
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Close 关闭 SQLite 连接。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) init(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS swarm_members (
			agent_id TEXT PRIMARY KEY,
			team_name TEXT NOT NULL,
			name TEXT NOT NULL,
			role TEXT NOT NULL,
			status TEXT NOT NULL,
			pid INTEGER NOT NULL DEFAULT 0,
			last_seen_at TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_swarm_members_team ON swarm_members(team_name)`,
		`CREATE TABLE IF NOT EXISTS swarm_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			team_name TEXT NOT NULL,
			from_agent TEXT NOT NULL,
			to_agent TEXT NOT NULL,
			kind TEXT NOT NULL,
			content_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			consumed_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_swarm_messages_inbox ON swarm_messages(team_name, to_agent, consumed_at, id)`,
		`CREATE TABLE IF NOT EXISTS swarm_tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			team_name TEXT NOT NULL,
			assignee TEXT NOT NULL,
			title TEXT NOT NULL,
			status TEXT NOT NULL,
			result_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_swarm_tasks_team ON swarm_tasks(team_name, assignee, status)`,
		`CREATE TABLE IF NOT EXISTS source_cards (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			team_name TEXT NOT NULL,
			query TEXT NOT NULL,
			title TEXT NOT NULL,
			url TEXT NOT NULL,
			snippet TEXT NOT NULL,
			source TEXT NOT NULL,
			credibility TEXT NOT NULL,
			retrieved_at TEXT NOT NULL,
			created_by TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_source_cards_team ON source_cards(team_name, id)`,
		`CREATE TABLE IF NOT EXISTS report_sections (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			team_name TEXT NOT NULL,
			section TEXT NOT NULL,
			content TEXT NOT NULL,
			evidence_ids_json TEXT NOT NULL,
			created_by TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_report_sections_team ON report_sections(team_name, id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// UpsertMember 创建或更新 teammate 状态；worker 心跳也走这条路径。
func (s *Store) UpsertMember(ctx context.Context, member Member) error {
	now := time.Now().UTC()
	if member.LastSeenAt.IsZero() {
		member.LastSeenAt = now
	}
	if member.CreatedAt.IsZero() {
		member.CreatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO swarm_members(agent_id, team_name, name, role, status, pid, last_seen_at, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			team_name=excluded.team_name,
			name=excluded.name,
			role=excluded.role,
			status=excluded.status,
			pid=excluded.pid,
			last_seen_at=excluded.last_seen_at`,
		member.AgentID, member.TeamName, member.Name, string(member.Role), string(member.Status), member.PID, formatTime(member.LastSeenAt), formatTime(member.CreatedAt))
	return err
}

// ListMembers 返回一个 team 下的全部 teammate 状态。
func (s *Store) ListMembers(ctx context.Context, teamName string) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id, team_name, name, role, status, pid, last_seen_at, created_at
		FROM swarm_members WHERE team_name = ? ORDER BY agent_id`, teamName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var member Member
		var role, status, lastSeen, created string
		if err := rows.Scan(&member.AgentID, &member.TeamName, &member.Name, &role, &status, &member.PID, &lastSeen, &created); err != nil {
			return nil, err
		}
		member.Role = AgentRole(role)
		member.Status = WorkerStatus(status)
		member.LastSeenAt = parseTime(lastSeen)
		member.CreatedAt = parseTime(created)
		out = append(out, member)
	}
	return out, rows.Err()
}

// EnqueueMessage 向指定 teammate 的 mailbox 投递消息。
func (s *Store) EnqueueMessage(ctx context.Context, msg MailboxMessage) (MailboxMessage, error) {
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO swarm_messages(team_name, from_agent, to_agent, kind, content_json, created_at)
		VALUES(?, ?, ?, ?, ?, ?)`,
		msg.TeamName, msg.FromAgent, msg.ToAgent, string(msg.Kind), firstNonEmpty(msg.ContentJSON, "{}"), formatTime(msg.CreatedAt))
	if err != nil {
		return MailboxMessage{}, err
	}
	msg.ID, err = result.LastInsertId()
	return msg, err
}

// ConsumeMessages 原子地取出并标记一个 teammate 未消费的 mailbox 消息。
func (s *Store) ConsumeMessages(ctx context.Context, teamName string, agentID string, limit int) ([]MailboxMessage, error) {
	if limit <= 0 {
		limit = 10
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, team_name, from_agent, to_agent, kind, content_json, created_at
		FROM swarm_messages
		WHERE team_name = ? AND to_agent = ? AND consumed_at IS NULL
		ORDER BY CASE kind
			WHEN 'shutdown' THEN 0
			WHEN 'task' THEN 1
			ELSE 2
		END, id LIMIT ?`, teamName, agentID, limit)
	if err != nil {
		return nil, err
	}
	var messages []MailboxMessage
	var ids []any
	for rows.Next() {
		var msg MailboxMessage
		var kind, created string
		if err := rows.Scan(&msg.ID, &msg.TeamName, &msg.FromAgent, &msg.ToAgent, &kind, &msg.ContentJSON, &created); err != nil {
			rows.Close()
			return nil, err
		}
		msg.Kind = MessageKind(kind)
		msg.CreatedAt = parseTime(created)
		messages = append(messages, msg)
		ids = append(ids, msg.ID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
		args := append([]any{formatTime(time.Now().UTC())}, ids...)
		if _, err := tx.ExecContext(ctx, `UPDATE swarm_messages SET consumed_at = ? WHERE id IN (`+placeholders+`)`, args...); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return messages, nil
}

// CreateTask 记录 leader 分配给 worker 的调查任务。
func (s *Store) CreateTask(ctx context.Context, task ResearchTask) (ResearchTask, error) {
	now := time.Now().UTC()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = now
	}
	if task.Status == "" {
		task.Status = TaskStatusPending
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO swarm_tasks(team_name, assignee, title, status, result_json, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		task.TeamName, task.Assignee, task.Title, string(task.Status), firstNonEmpty(task.ResultJSON, "{}"), formatTime(task.CreatedAt), formatTime(task.UpdatedAt))
	if err != nil {
		return ResearchTask{}, err
	}
	task.ID, err = result.LastInsertId()
	return task, err
}

// UpdateTask 更新调查任务状态和结果摘要。
func (s *Store) UpdateTask(ctx context.Context, taskID int64, status TaskStatus, resultJSON string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE swarm_tasks SET status = ?, result_json = ?, updated_at = ? WHERE id = ?`,
		string(status), firstNonEmpty(resultJSON, "{}"), formatTime(time.Now().UTC()), taskID)
	return err
}

// ListTasks 返回 team 下的任务列表。
func (s *Store) ListTasks(ctx context.Context, teamName string) ([]ResearchTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, team_name, assignee, title, status, result_json, created_at, updated_at
		FROM swarm_tasks WHERE team_name = ? ORDER BY id`, teamName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResearchTask
	for rows.Next() {
		var task ResearchTask
		var status, created, updated string
		if err := rows.Scan(&task.ID, &task.TeamName, &task.Assignee, &task.Title, &status, &task.ResultJSON, &created, &updated); err != nil {
			return nil, err
		}
		task.Status = TaskStatus(status)
		task.CreatedAt = parseTime(created)
		task.UpdatedAt = parseTime(updated)
		out = append(out, task)
	}
	return out, rows.Err()
}

// SaveSourceCard 保存搜索员确认过的资料卡。
func (s *Store) SaveSourceCard(ctx context.Context, card SourceCard) (SourceCard, error) {
	if card.RetrievedAt.IsZero() {
		card.RetrievedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO source_cards(team_name, query, title, url, snippet, source, credibility, retrieved_at, created_by)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		card.TeamName, card.Query, card.Title, card.URL, card.Snippet, card.Source, firstNonEmpty(card.Credibility, "medium"), formatTime(card.RetrievedAt), card.CreatedBy)
	if err != nil {
		return SourceCard{}, err
	}
	card.ID, err = result.LastInsertId()
	return card, err
}

// ListSourceCards 返回 team 的资料卡，供 analyst/writer 引用。
func (s *Store) ListSourceCards(ctx context.Context, teamName string) ([]SourceCard, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, team_name, query, title, url, snippet, source, credibility, retrieved_at, created_by
		FROM source_cards WHERE team_name = ? ORDER BY id`, teamName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceCard
	for rows.Next() {
		var card SourceCard
		var retrieved string
		if err := rows.Scan(&card.ID, &card.TeamName, &card.Query, &card.Title, &card.URL, &card.Snippet, &card.Source, &card.Credibility, &retrieved, &card.CreatedBy); err != nil {
			return nil, err
		}
		card.RetrievedAt = parseTime(retrieved)
		out = append(out, card)
	}
	return out, rows.Err()
}

// SaveReportSection 保存分析员或撰稿员生成的报告片段。
func (s *Store) SaveReportSection(ctx context.Context, section ReportSection) (ReportSection, error) {
	if section.UpdatedAt.IsZero() {
		section.UpdatedAt = time.Now().UTC()
	}
	evidence, err := json.Marshal(section.EvidenceIDs)
	if err != nil {
		return ReportSection{}, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO report_sections(team_name, section, content, evidence_ids_json, created_by, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)`,
		section.TeamName, section.Section, section.Content, string(evidence), section.CreatedBy, formatTime(section.UpdatedAt))
	if err != nil {
		return ReportSection{}, err
	}
	section.ID, err = result.LastInsertId()
	return section, err
}

// ListReportSections 返回 team 的报告章节。
func (s *Store) ListReportSections(ctx context.Context, teamName string) ([]ReportSection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, team_name, section, content, evidence_ids_json, created_by, updated_at
		FROM report_sections WHERE team_name = ? ORDER BY id`, teamName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReportSection
	for rows.Next() {
		var section ReportSection
		var evidence, updated string
		if err := rows.Scan(&section.ID, &section.TeamName, &section.Section, &section.Content, &evidence, &section.CreatedBy, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(evidence), &section.EvidenceIDs); err != nil {
			return nil, err
		}
		section.UpdatedAt = parseTime(updated)
		out = append(out, section)
	}
	return out, rows.Err()
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(raw string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, raw)
	return t
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func isSQLiteLocked(err error) bool {
	return err != nil && strings.Contains(err.Error(), "database is locked")
}
