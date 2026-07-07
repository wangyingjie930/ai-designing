package progresstracking

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

// longHorizonStore 持久化 v2 三平面状态和 append-only 账本。
type longHorizonStore struct {
	db   *sql.DB
	path string
}

// longHorizonSnapshot 是从 SQLite 恢复长任务所需的完整状态切片。
type longHorizonSnapshot struct {
	Goal               GoalContract
	CurrentMilestoneID string
	WorkingCollection  map[string]any
	OpenBlockers       []string
	NextAction         string
	Milestones         []Milestone
	LastDriftSignal    *DriftSignal
}

// openLongHorizonStore 打开 v2 SQLite 存储并初始化长任务状态表。
func openLongHorizonStore(ctx context.Context, path string) (*longHorizonStore, error) {
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
	store := &longHorizonStore{db: db, path: path}
	if err := store.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// init 建立 v2 状态表；账本表只追加，其他状态表保存当前可靠快照。
func (s *longHorizonStore) init(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS long_horizon_tasks (
			task_id TEXT PRIMARY KEY,
			goal_json TEXT NOT NULL,
			current_milestone_id TEXT NOT NULL,
			status TEXT NOT NULL,
			working_collection_json TEXT NOT NULL,
			open_blockers_json TEXT NOT NULL,
			next_action TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS long_horizon_milestones (
			task_id TEXT NOT NULL,
			milestone_id TEXT NOT NULL,
			title TEXT NOT NULL,
			acceptance_json TEXT NOT NULL,
			status TEXT NOT NULL,
			active_subgoal TEXT,
			sort_order INTEGER NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(task_id, milestone_id),
			FOREIGN KEY(task_id) REFERENCES long_horizon_tasks(task_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS long_horizon_events (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			event TEXT NOT NULL,
			decision TEXT,
			reason TEXT,
			evidence_refs_json TEXT NOT NULL,
			state_delta_json TEXT NOT NULL,
			compensate_op TEXT,
			idempotency_key TEXT,
			next_action TEXT,
			created_at TEXT NOT NULL,
			FOREIGN KEY(task_id) REFERENCES long_horizon_tasks(task_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_long_horizon_events_task_sequence ON long_horizon_events(task_id, sequence)`,
		`CREATE TABLE IF NOT EXISTS long_horizon_mechanical_values (
			task_id TEXT NOT NULL,
			key TEXT NOT NULL,
			scope TEXT NOT NULL,
			value_ref TEXT NOT NULL,
			value_json TEXT NOT NULL,
			provider TEXT NOT NULL,
			runtime_layer TEXT NOT NULL,
			trust TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(task_id, key),
			FOREIGN KEY(task_id) REFERENCES long_horizon_tasks(task_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS long_horizon_drift_signals (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			level TEXT NOT NULL,
			goal_relevance REAL NOT NULL,
			milestone_progress REAL NOT NULL,
			evidence_health REAL NOT NULL,
			error_pressure REAL NOT NULL,
			reason TEXT NOT NULL,
			created_at TEXT NOT NULL,
			FOREIGN KEY(task_id) REFERENCES long_horizon_tasks(task_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS long_horizon_gate_results (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			milestone_id TEXT NOT NULL,
			passed INTEGER NOT NULL,
			missing_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			FOREIGN KEY(task_id) REFERENCES long_horizon_tasks(task_id) ON DELETE CASCADE
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return s.ensureMechanicalValueColumns(ctx)
}

func (s *longHorizonStore) ensureMechanicalValueColumns(ctx context.Context) error {
	hasValueJSON, err := s.hasColumn(ctx, "long_horizon_mechanical_values", "value_json")
	if err != nil {
		return err
	}
	if hasValueJSON {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE long_horizon_mechanical_values ADD COLUMN value_json TEXT NOT NULL DEFAULT '{}'`)
	return err
}

func (s *longHorizonStore) hasColumn(ctx context.Context, tableName, columnName string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+tableName+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return false, err
		}
		if name == columnName {
			return true, nil
		}
	}
	return false, rows.Err()
}

// close 关闭 v2 SQLite 连接。
func (s *longHorizonStore) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// saveInitialState 用事务重置一个长任务的锚、里程碑和当前工作集。
func (s *longHorizonStore) saveInitialState(ctx context.Context, taskID string, req InitializeLongHorizonRequest, now time.Time) error {
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

	timestamp := now.Format(time.RFC3339Nano)
	goalJSON, err := encodeLongHorizonJSON(req.Goal)
	if err != nil {
		return err
	}
	workingJSON, err := encodeLongHorizonJSON(req.WorkingCollection)
	if err != nil {
		return err
	}
	blockersJSON, err := encodeLongHorizonJSON(req.OpenBlockers)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO long_horizon_tasks(
		task_id, goal_json, current_milestone_id, status, working_collection_json, open_blockers_json, next_action, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(task_id) DO UPDATE SET
		goal_json = excluded.goal_json,
		current_milestone_id = excluded.current_milestone_id,
		status = excluded.status,
		working_collection_json = excluded.working_collection_json,
		open_blockers_json = excluded.open_blockers_json,
		next_action = excluded.next_action,
		updated_at = excluded.updated_at`,
		taskID,
		goalJSON,
		req.CurrentMilestoneID,
		string(TaskStatusInProgress),
		workingJSON,
		blockersJSON,
		nullableString(emptyToNil(req.NextAction)),
		timestamp,
		timestamp,
	); err != nil {
		return err
	}
	for _, statement := range []string{
		`DELETE FROM long_horizon_milestones WHERE task_id = ?`,
		`DELETE FROM long_horizon_events WHERE task_id = ?`,
		`DELETE FROM long_horizon_mechanical_values WHERE task_id = ?`,
		`DELETE FROM long_horizon_drift_signals WHERE task_id = ?`,
		`DELETE FROM long_horizon_gate_results WHERE task_id = ?`,
	} {
		if _, err = tx.ExecContext(ctx, statement, taskID); err != nil {
			return err
		}
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO long_horizon_milestones(
		task_id, milestone_id, title, acceptance_json, status, active_subgoal, sort_order, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for idx, milestone := range req.Milestones {
		acceptanceJSON, err := encodeLongHorizonJSON(milestone.Acceptance)
		if err != nil {
			return err
		}
		if _, err = stmt.ExecContext(ctx,
			taskID,
			milestone.MilestoneID,
			milestone.Title,
			acceptanceJSON,
			string(milestone.Status),
			nullableString(emptyToNil(milestone.ActiveSubgoal)),
			idx,
			timestamp,
		); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// loadSnapshot 读取目标锚、里程碑、工作集和最近漂移信号。
func (s *longHorizonStore) loadSnapshot(ctx context.Context, taskID string) (longHorizonSnapshot, error) {
	var snapshot longHorizonSnapshot
	var goalJSON string
	var workingJSON string
	var blockersJSON string
	var nextAction sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT goal_json, current_milestone_id, working_collection_json, open_blockers_json, next_action
		FROM long_horizon_tasks WHERE task_id = ?`, taskID).Scan(
		&goalJSON,
		&snapshot.CurrentMilestoneID,
		&workingJSON,
		&blockersJSON,
		&nextAction,
	)
	if err != nil {
		return longHorizonSnapshot{}, err
	}
	if err := decodeLongHorizonJSON(goalJSON, &snapshot.Goal); err != nil {
		return longHorizonSnapshot{}, err
	}
	if err := decodeLongHorizonJSON(workingJSON, &snapshot.WorkingCollection); err != nil {
		return longHorizonSnapshot{}, err
	}
	if err := decodeLongHorizonJSON(blockersJSON, &snapshot.OpenBlockers); err != nil {
		return longHorizonSnapshot{}, err
	}
	if nextAction.Valid {
		snapshot.NextAction = nextAction.String
	}
	milestones, err := s.loadMilestones(ctx, taskID)
	if err != nil {
		return longHorizonSnapshot{}, err
	}
	snapshot.Milestones = milestones
	drift, err := s.loadLastDriftSignal(ctx, taskID)
	if err != nil {
		return longHorizonSnapshot{}, err
	}
	snapshot.LastDriftSignal = drift
	return snapshot, nil
}

// loadMilestones 按初始化顺序恢复里程碑列表。
func (s *longHorizonStore) loadMilestones(ctx context.Context, taskID string) ([]Milestone, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT milestone_id, title, acceptance_json, status, active_subgoal
		FROM long_horizon_milestones WHERE task_id = ? ORDER BY sort_order ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var milestones []Milestone
	for rows.Next() {
		var milestone Milestone
		var acceptanceJSON string
		var status string
		var activeSubgoal sql.NullString
		if err := rows.Scan(&milestone.MilestoneID, &milestone.Title, &acceptanceJSON, &status, &activeSubgoal); err != nil {
			return nil, err
		}
		if err := decodeLongHorizonJSON(acceptanceJSON, &milestone.Acceptance); err != nil {
			return nil, err
		}
		milestone.Status = TaskStatus(status)
		if activeSubgoal.Valid {
			milestone.ActiveSubgoal = activeSubgoal.String
		}
		milestones = append(milestones, milestone)
	}
	return milestones, rows.Err()
}

// appendEvent 在账本末尾追加一条事件，历史记录不覆盖。
func (s *longHorizonStore) appendEvent(ctx context.Context, taskID string, req AppendProgressEventRequest, now time.Time) (ProgressEvent, error) {
	evidenceJSON, err := encodeLongHorizonJSON(normalizeStringList(req.EvidenceRefs))
	if err != nil {
		return ProgressEvent{}, err
	}
	stateDelta := StateDelta{
		Read:  normalizeStringList(req.StateDelta.Read),
		Write: normalizeStringList(req.StateDelta.Write),
	}
	stateDeltaJSON, err := encodeLongHorizonJSON(stateDelta)
	if err != nil {
		return ProgressEvent{}, err
	}
	createdAt := now.Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProgressEvent{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	result, err := tx.ExecContext(ctx, `INSERT INTO long_horizon_events(
		task_id, event, decision, reason, evidence_refs_json, state_delta_json, compensate_op, idempotency_key, next_action, created_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID,
		strings.TrimSpace(req.Event),
		string(req.Decision),
		nullableString(emptyToNil(req.Reason)),
		evidenceJSON,
		stateDeltaJSON,
		nullableString(emptyToNil(req.CompensateOp)),
		nullableString(emptyToNil(req.IdempotencyKey)),
		nullableString(emptyToNil(req.NextAction)),
		createdAt,
	)
	if err != nil {
		return ProgressEvent{}, err
	}
	if strings.TrimSpace(req.NextAction) != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE long_horizon_tasks SET next_action = ?, updated_at = ? WHERE task_id = ?`,
			strings.TrimSpace(req.NextAction),
			createdAt,
			taskID,
		); err != nil {
			return ProgressEvent{}, err
		}
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return ProgressEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProgressEvent{}, err
	}
	committed = true
	return ProgressEvent{
		Sequence:       sequence,
		Event:          strings.TrimSpace(req.Event),
		Decision:       req.Decision,
		Reason:         strings.TrimSpace(req.Reason),
		EvidenceRefs:   normalizeStringList(req.EvidenceRefs),
		StateDelta:     stateDelta,
		CompensateOp:   strings.TrimSpace(req.CompensateOp),
		IdempotencyKey: strings.TrimSpace(req.IdempotencyKey),
		NextAction:     strings.TrimSpace(req.NextAction),
		CreatedAt:      createdAt,
	}, nil
}

// loadRecentEvents 读取最近 N 条账本事件，并恢复为正序。
func (s *longHorizonStore) loadRecentEvents(ctx context.Context, taskID string, limit int) ([]ProgressEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, event, decision, reason, evidence_refs_json, state_delta_json, compensate_op, idempotency_key, next_action, created_at
		FROM long_horizon_events
		WHERE task_id = ?
		ORDER BY sequence DESC
		LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reversed []ProgressEvent
	for rows.Next() {
		var event ProgressEvent
		var decision string
		var reason sql.NullString
		var evidenceJSON string
		var stateDeltaJSON string
		var compensateOp sql.NullString
		var idempotencyKey sql.NullString
		var nextAction sql.NullString
		if err := rows.Scan(&event.Sequence, &event.Event, &decision, &reason, &evidenceJSON, &stateDeltaJSON, &compensateOp, &idempotencyKey, &nextAction, &event.CreatedAt); err != nil {
			return nil, err
		}
		event.Decision = Decision(decision)
		if reason.Valid {
			event.Reason = reason.String
		}
		if err := decodeLongHorizonJSON(evidenceJSON, &event.EvidenceRefs); err != nil {
			return nil, err
		}
		if err := decodeLongHorizonJSON(stateDeltaJSON, &event.StateDelta); err != nil {
			return nil, err
		}
		if compensateOp.Valid {
			event.CompensateOp = compensateOp.String
		}
		if idempotencyKey.Valid {
			event.IdempotencyKey = idempotencyKey.String
		}
		if nextAction.Valid {
			event.NextAction = nextAction.String
		}
		reversed = append(reversed, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed, nil
}

// writeMechanicalValue 写入或更新一个可复用机械真值。
func (s *longHorizonStore) writeMechanicalValue(ctx context.Context, taskID string, value MechanicalValue, now time.Time) error {
	timestamp := now.Format(time.RFC3339Nano)
	valueJSON, err := encodeLongHorizonJSON(value.Value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO long_horizon_mechanical_values(
		task_id, key, scope, value_ref, value_json, provider, runtime_layer, trust, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(task_id, key) DO UPDATE SET
		scope = excluded.scope,
		value_ref = excluded.value_ref,
		value_json = excluded.value_json,
		provider = excluded.provider,
		runtime_layer = excluded.runtime_layer,
		trust = excluded.trust,
		updated_at = excluded.updated_at`,
		taskID,
		value.Key,
		value.Scope,
		value.ValueRef,
		valueJSON,
		value.Provider,
		value.RuntimeLayer,
		string(value.Trust),
		timestamp,
	)
	return err
}

// loadMechanicalValue 读取程序侧机械真值；调用方决定是否绑定到工具参数。
func (s *longHorizonStore) loadMechanicalValue(ctx context.Context, taskID string, key string) (MechanicalValue, error) {
	var (
		value     MechanicalValue
		valueJSON string
		trust     string
	)
	err := s.db.QueryRowContext(ctx, `SELECT key, scope, value_ref, value_json, provider, runtime_layer, trust
		FROM long_horizon_mechanical_values
		WHERE task_id = ? AND key = ?`, taskID, key).Scan(
		&value.Key,
		&value.Scope,
		&value.ValueRef,
		&valueJSON,
		&value.Provider,
		&value.RuntimeLayer,
		&trust,
	)
	if err == sql.ErrNoRows {
		return MechanicalValue{}, fmt.Errorf("mechanical state %s not found", key)
	}
	if err != nil {
		return MechanicalValue{}, err
	}
	if err := decodeLongHorizonJSON(valueJSON, &value.Value); err != nil {
		return MechanicalValue{}, err
	}
	value.Trust = TrustLevel(trust)
	return value, nil
}

// loadMechanicalKeys 只把机械态索引暴露给恢复包，避免模型直接复制真值。
func (s *longHorizonStore) loadMechanicalKeys(ctx context.Context, taskID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM long_horizon_mechanical_values WHERE task_id = ? ORDER BY key ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// loadLastDriftSignal 返回最近一次漂移判断，当前没有记录时返回 nil。
func (s *longHorizonStore) loadLastDriftSignal(ctx context.Context, taskID string) (*DriftSignal, error) {
	var signal DriftSignal
	var level string
	err := s.db.QueryRowContext(ctx, `SELECT level, goal_relevance, milestone_progress, evidence_health, error_pressure, reason, created_at
		FROM long_horizon_drift_signals
		WHERE task_id = ?
		ORDER BY sequence DESC
		LIMIT 1`, taskID).Scan(
		&level,
		&signal.GoalRelevance,
		&signal.MilestoneProgress,
		&signal.EvidenceHealth,
		&signal.ErrorPressure,
		&signal.Reason,
		&signal.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	signal.Level = DriftLevel(level)
	return &signal, nil
}

// encodeLongHorizonJSON 将复杂字段稳定编码为 SQLite 文本。
func encodeLongHorizonJSON(value any) (string, error) {
	if value == nil {
		value = map[string]any{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// decodeLongHorizonJSON 解码 SQLite 中保存的 JSON 字段。
func decodeLongHorizonJSON(text string, out any) error {
	if strings.TrimSpace(text) == "" {
		text = "{}"
	}
	return json.Unmarshal([]byte(text), out)
}

// emptyToNil 将空字符串转成 nil，便于 SQLite 保存 NULL。
func emptyToNil(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
