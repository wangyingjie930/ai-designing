package progresstracking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// LongHorizonTracker 协调 v2 长任务的锚、里程碑、账本、工作集和机械态。
type LongHorizonTracker struct {
	taskID string
	store  *longHorizonStore
	now    func() time.Time
	mu     sync.Mutex
}

// NewLongHorizonTracker 初始化 v2 长任务 tracker，并打开 SQLite 存储。
func NewLongHorizonTracker(ctx context.Context, config LongHorizonConfig) (*LongHorizonTracker, error) {
	dbPath := strings.TrimSpace(config.DBPath)
	if dbPath == "" {
		return nil, errors.New("db path is required")
	}
	taskID := strings.TrimSpace(config.TaskID)
	if taskID == "" {
		taskID = defaultLongHorizonTaskID
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	store, err := openLongHorizonStore(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	return &LongHorizonTracker{
		taskID: taskID,
		store:  store,
		now:    now,
	}, nil
}

// Initialize 冻结目标契约并初始化里程碑、工作集和下一步动作。
func (t *LongHorizonTracker) Initialize(ctx context.Context, req InitializeLongHorizonRequest) error {
	if t == nil || t.store == nil {
		return errors.New("long horizon tracker is nil")
	}
	if err := validateInitializeLongHorizonRequest(req); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if req.Goal.Version == 0 {
		req.Goal.Version = defaultGoalContractVersion
	}
	return t.store.saveInitialState(ctx, t.taskID, req, t.now())
}

// AppendEvent 追加一条进度账本事件；历史事件不覆盖。
func (t *LongHorizonTracker) AppendEvent(ctx context.Context, req AppendProgressEventRequest) error {
	if t == nil || t.store == nil {
		return errors.New("long horizon tracker is nil")
	}
	if strings.TrimSpace(req.Event) == "" {
		return errors.New("event is required")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, err := t.store.appendEvent(ctx, t.taskID, req, t.now())
	return err
}

// WriteMechanicalValue 写入机械状态真值；ResumePacket 只暴露 key，真实值留给编排器解析。
func (t *LongHorizonTracker) WriteMechanicalValue(ctx context.Context, value MechanicalValue) error {
	if t == nil || t.store == nil {
		return errors.New("long horizon tracker is nil")
	}
	value.Key = strings.TrimSpace(value.Key)
	if value.Key == "" {
		return errors.New("mechanical value key is required")
	}
	if strings.TrimSpace(value.ValueRef) == "" {
		return errors.New("mechanical value ref is required")
	}
	if value.Value == nil {
		return errors.New("mechanical value is required")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.store.writeMechanicalValue(ctx, t.taskID, value, t.now())
}

// ResolveMechanicalValue 把叙事态中的 STATE.xxx 引用解析成程序侧机械真值。
func (t *LongHorizonTracker) ResolveMechanicalValue(ctx context.Context, stateRef string) (MechanicalValue, error) {
	if t == nil || t.store == nil {
		return MechanicalValue{}, errors.New("long horizon tracker is nil")
	}
	key := normalizeMechanicalStateKey(stateRef)
	if key == "" {
		return MechanicalValue{}, errors.New("mechanical state ref is required")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.store.loadMechanicalValue(ctx, t.taskID, key)
}

// ResumePacket 从 SQLite 组装恢复包，避免新一轮 Agent 翻聊天记录猜状态。
func (t *LongHorizonTracker) ResumePacket(ctx context.Context) (ResumePacket, error) {
	if t == nil || t.store == nil {
		return ResumePacket{}, errors.New("long horizon tracker is nil")
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	snapshot, err := t.store.loadSnapshot(ctx, t.taskID)
	if err == sql.ErrNoRows {
		return ResumePacket{}, fmt.Errorf("long horizon task %s is not initialized", t.taskID)
	}
	if err != nil {
		return ResumePacket{}, err
	}
	current, err := currentMilestoneFromSnapshot(snapshot)
	if err != nil {
		return ResumePacket{}, err
	}
	recentLedger, err := t.store.loadRecentEvents(ctx, t.taskID, defaultRecentLedgerLimit)
	if err != nil {
		return ResumePacket{}, err
	}
	mechanicalKeys, err := t.store.loadMechanicalKeys(ctx, t.taskID)
	if err != nil {
		return ResumePacket{}, err
	}
	return ResumePacket{
		Goal:                snapshot.Goal,
		CurrentMilestone:    current,
		WorkingCollection:   snapshot.WorkingCollection,
		RecentLedger:        recentLedger,
		OpenBlockers:        snapshot.OpenBlockers,
		MechanicalStateKeys: mechanicalKeys,
		LastDriftSignal:     snapshot.LastDriftSignal,
		NextAction:          snapshot.NextAction,
	}, nil
}

// RecitationPrompt 把目标锚、当前里程碑和下一步动作推回上下文尾部。
func (t *LongHorizonTracker) RecitationPrompt(ctx context.Context) string {
	packet, err := t.ResumePacket(ctx)
	if err != nil {
		return ""
	}
	lines := []string{
		"Re-center before continuing.",
		"Original goal: " + packet.Goal.UserGoal,
		"Success criteria: " + strings.Join(packet.Goal.SuccessCriteria, "; "),
		"Current milestone: " + packet.CurrentMilestone.Title,
		"Active subgoal: " + firstLongHorizonNonEmpty(packet.CurrentMilestone.ActiveSubgoal, "decide it"),
		"Non-goals: " + strings.Join(packet.Goal.NonGoals, "; "),
		"Constraints: " + strings.Join(packet.Goal.Constraints, "; "),
		"Open blockers: " + joinOrNone(packet.OpenBlockers),
		"Next action: " + firstLongHorizonNonEmpty(packet.NextAction, "decide it"),
		"Do not report completion until the current verification gate passes.",
	}
	return strings.Join(lines, "\n")
}

// Close 释放 v2 SQLite 连接。
func (t *LongHorizonTracker) Close() error {
	if t == nil || t.store == nil {
		return nil
	}
	return t.store.close()
}

// validateInitializeLongHorizonRequest 校验锚和里程碑的最小可恢复条件。
func validateInitializeLongHorizonRequest(req InitializeLongHorizonRequest) error {
	if strings.TrimSpace(req.Goal.GoalID) == "" {
		return errors.New("goal id is required")
	}
	if strings.TrimSpace(req.Goal.UserGoal) == "" {
		return errors.New("user goal is required")
	}
	if len(req.Milestones) == 0 {
		return errors.New("milestones are required")
	}
	current := strings.TrimSpace(req.CurrentMilestoneID)
	if current == "" {
		return errors.New("current milestone id is required")
	}
	found := false
	for idx, milestone := range req.Milestones {
		if strings.TrimSpace(milestone.MilestoneID) == "" {
			return fmt.Errorf("milestone %d id is required", idx)
		}
		if strings.TrimSpace(milestone.Title) == "" {
			return fmt.Errorf("milestone %s title is required", milestone.MilestoneID)
		}
		if !milestone.Status.Valid() {
			return fmt.Errorf("milestone %s status %q is invalid", milestone.MilestoneID, milestone.Status)
		}
		if milestone.MilestoneID == current {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("current milestone %s not found", current)
	}
	return nil
}

// currentMilestoneFromSnapshot 找到恢复包中的当前里程碑。
func currentMilestoneFromSnapshot(snapshot longHorizonSnapshot) (Milestone, error) {
	for _, milestone := range snapshot.Milestones {
		if milestone.MilestoneID == snapshot.CurrentMilestoneID {
			return milestone, nil
		}
	}
	return Milestone{}, fmt.Errorf("current milestone %s not found", snapshot.CurrentMilestoneID)
}

// joinOrNone 渲染可空列表，避免复诵提示里出现空字符串。
func joinOrNone(values []string) string {
	normalized := normalizeStringList(values)
	if len(normalized) == 0 {
		return "none"
	}
	return strings.Join(normalized, "; ")
}

// firstLongHorizonNonEmpty 返回第一个非空字符串，用于恢复提示默认值。
func firstLongHorizonNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

// normalizeMechanicalStateKey 统一 STATE.xxx 和裸 key，供闸门与编排器复用同一坐标。
func normalizeMechanicalStateKey(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "STATE.")
	return strings.TrimSpace(value)
}
