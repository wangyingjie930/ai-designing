package progresstracking

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// AgentQuerier 是 v2 wrapper 依赖的最小 Agent 能力，便于包住真实 ADK Agent 或测试 fake。
type AgentQuerier interface {
	Query(context.Context, AgentRequest) (*AgentResponse, error)
}

// LongHorizonAgentConfig 配置一个带 v2 长任务控制层的 Agent wrapper。
type LongHorizonAgentConfig struct {
	Base            AgentQuerier
	ProgressTracker *ProgressTracker
	LongTracker     *LongHorizonTracker
}

// LongHorizonEventPlanningAgent 在现有 Agent 外层维护 v2 长任务锚、账本、机械态和验收/漂移信号。
type LongHorizonEventPlanningAgent struct {
	base            AgentQuerier
	progressTracker *ProgressTracker
	longTracker     *LongHorizonTracker
}

// NewLongHorizonEventPlanningAgent 用 v2 控制层包住现有活动筹备 Agent。
func NewLongHorizonEventPlanningAgent(config LongHorizonAgentConfig) (*LongHorizonEventPlanningAgent, error) {
	if config.Base == nil {
		return nil, errors.New("base agent is required")
	}
	if config.ProgressTracker == nil {
		return nil, errors.New("progress tracker is required")
	}
	if config.LongTracker == nil {
		return nil, errors.New("long horizon tracker is required")
	}
	return &LongHorizonEventPlanningAgent{
		base:            config.Base,
		progressTracker: config.ProgressTracker,
		longTracker:     config.LongTracker,
	}, nil
}

// Query 在 Agent 推理前注入 v2 恢复上下文，推理后用确定性状态差异写入 v2 ledger。
func (a *LongHorizonEventPlanningAgent) Query(ctx context.Context, req AgentRequest) (*AgentResponse, error) {
	if a == nil || a.base == nil {
		return nil, errors.New("long horizon agent is not initialized")
	}
	if err := a.ensureInitialized(ctx, req.Message); err != nil {
		return nil, err
	}
	before := a.progressTracker.Items()
	enriched, err := a.enrichRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	response, err := a.base.Query(ctx, enriched)
	if err != nil {
		return nil, err
	}
	after := a.progressTracker.Items()
	summary, err := a.recordProgressDelta(ctx, before, after)
	if err != nil {
		return nil, err
	}
	if summary == "" {
		return response, nil
	}
	response.Message = strings.TrimSpace(response.Message) + "\n\n" + summary
	return response, nil
}

// ensureInitialized 首次运行时把用户目标冻结成 v2 GoalContract 和默认里程碑。
func (a *LongHorizonEventPlanningAgent) ensureInitialized(ctx context.Context, userMessage string) error {
	if _, err := a.longTracker.ResumePacket(ctx); err == nil {
		return nil
	}
	goal := GoalContract{
		GoalID:          a.longTracker.TaskID(),
		UserGoal:        strings.TrimSpace(userMessage),
		SuccessCriteria: []string{"建立可恢复的活动筹备计划", "逐项推进计划并记录真实 checkpoint", "完成关键动作后通过 v2 验收闸门"},
		NonGoals:        []string{"不跳过 checkpoint 直接汇报完成", "不把未执行动作写成已完成"},
		Constraints:     []string{"任务状态变化必须来自 ProgressTracker", "v2 账本由 wrapper 根据状态差异写入"},
		Version:         defaultGoalContractVersion,
	}
	return a.longTracker.Initialize(ctx, InitializeLongHorizonRequest{
		Goal: goal,
		Milestones: []Milestone{
			{
				MilestoneID:   "M1",
				Title:         "冻结活动目标并建立计划",
				Acceptance:    []string{"evidence:progress:plan"},
				Status:        TaskStatusInProgress,
				ActiveSubgoal: "读取当前进度并建立可执行计划",
			},
			{
				MilestoneID: "M2",
				Title:       "推进当前计划项",
				Acceptance:  []string{"STATE.progress_item_0", "evidence:progress:item:0"},
				Status:      TaskStatusPending,
			},
			{
				MilestoneID: "M3",
				Title:       "验收活动筹备结果",
				Acceptance:  []string{"evidence:progress:gate"},
				Status:      TaskStatusPending,
			},
		},
		CurrentMilestoneID: "M1",
		WorkingCollection: map[string]any{
			"source": "cmd/progress-tracking-agent",
			"mode":   "event-planning",
		},
		NextAction: "读取 v1 progress tracker 并让 Agent 推进下一项",
	})
}

// enrichRequest 把恢复包和复诵提示合并进 Agent 输入，让模型每轮都看到长任务锚点。
func (a *LongHorizonEventPlanningAgent) enrichRequest(ctx context.Context, req AgentRequest) (AgentRequest, error) {
	packet, err := a.longTracker.ResumePacket(ctx)
	if err != nil {
		return AgentRequest{}, err
	}
	lines := []string{
		"Long-horizon recitation:",
		a.longTracker.RecitationPrompt(ctx),
		"",
		"Long-horizon resume packet:",
		fmt.Sprintf("goal_id: %s", packet.Goal.GoalID),
		fmt.Sprintf("current_milestone: %s", packet.CurrentMilestone.Title),
		fmt.Sprintf("mechanical_state_keys: %s", joinOrNone(packet.MechanicalStateKeys)),
		fmt.Sprintf("recent_ledger_events: %d", len(packet.RecentLedger)),
		"",
		"User message:",
		req.Message,
	}
	return AgentRequest{Message: strings.Join(lines, "\n")}, nil
}

// recordProgressDelta 根据 v1 tracker 的真实状态差异写 v2 账本和机械态。
func (a *LongHorizonEventPlanningAgent) recordProgressDelta(ctx context.Context, before []TaskItem, after []TaskItem) (string, error) {
	if len(after) == 0 {
		return "", nil
	}
	planCreated := len(before) == 0 && len(after) > 0
	changes := diffTaskItems(before, after)
	if !planCreated && len(changes) == 0 {
		return "", nil
	}
	if planCreated {
		if err := a.writePlanCreatedEvent(ctx, after); err != nil {
			return "", err
		}
	}
	for _, change := range changes {
		if err := a.writeTaskChange(ctx, change); err != nil {
			return "", err
		}
	}
	packet, err := a.longTracker.ResumePacket(ctx)
	if err != nil {
		return "", err
	}
	gate := EvaluateVerificationGate(packet)
	drift := EvaluateDrift(packet)
	return fmt.Sprintf("V2 checkpoint: ledger_events=%d gate_passed=%t drift=%s", len(packet.RecentLedger), gate.Passed, drift.Level), nil
}

// writePlanCreatedEvent 记录 Agent 通过 v1 progress tools 建立了可恢复计划。
func (a *LongHorizonEventPlanningAgent) writePlanCreatedEvent(ctx context.Context, items []TaskItem) error {
	return a.longTracker.AppendEvent(ctx, AppendProgressEventRequest{
		Event:          fmt.Sprintf("建立活动筹备计划: %d 项", len(items)),
		Decision:       DecisionApprove,
		Reason:         "v1 progress tracker created executable plan items",
		EvidenceRefs:   []string{"progress:plan"},
		StateDelta:     StateDelta{Write: []string{"STATE.progress_plan"}},
		IdempotencyKey: a.progressTracker.PlanID() + ":plan_created",
		NextAction:     "推进第一个未完成计划项",
	})
}

// writeTaskChange 把单个 v1 任务变化转成 v2 事件和机械态索引。
func (a *LongHorizonEventPlanningAgent) writeTaskChange(ctx context.Context, change taskItemChange) error {
	key := fmt.Sprintf("progress_item_%d", change.Index)
	if err := a.longTracker.WriteMechanicalValue(ctx, MechanicalValue{
		Key:          key,
		Scope:        "plan:" + a.progressTracker.PlanID(),
		ValueRef:     "STATE." + key,
		Value:        change.After,
		Provider:     "progress_tracker",
		RuntimeLayer: fmt.Sprintf("progress_tracking.v1.item.%d", change.Index),
		Trust:        TrustSystemRecord,
	}); err != nil {
		return err
	}
	return a.longTracker.AppendEvent(ctx, AppendProgressEventRequest{
		Event:          eventTitleForTaskChange(change),
		Decision:       decisionForTaskStatus(change.After.Status),
		Reason:         reasonForTaskChange(change),
		EvidenceRefs:   []string{fmt.Sprintf("progress:item:%d", change.Index)},
		StateDelta:     StateDelta{Write: []string{"STATE." + key}},
		CompensateOp:   compensateOpForTaskStatus(change.After.Status),
		IdempotencyKey: fmt.Sprintf("%s:%d:%s", a.progressTracker.PlanID(), change.Index, change.After.Status),
		NextAction:     "读取恢复包并推进下一个未完成计划项",
	})
}

// TaskID 返回当前 v2 长任务标识，便于 cmd 层串联输出和追踪。
func (t *LongHorizonTracker) TaskID() string {
	if t == nil {
		return ""
	}
	return t.taskID
}

type taskItemChange struct {
	Index  int
	Before *TaskItem
	After  TaskItem
}

// diffTaskItems 找出 v1 任务状态或结果的真实变化。
func diffTaskItems(before []TaskItem, after []TaskItem) []taskItemChange {
	changes := make([]taskItemChange, 0)
	for index, item := range after {
		var previous *TaskItem
		if index < len(before) {
			beforeItem := before[index]
			previous = &beforeItem
			if taskItemsEquivalent(beforeItem, item) {
				continue
			}
		}
		if previous == nil && item.Status == TaskStatusPending {
			continue
		}
		changes = append(changes, taskItemChange{
			Index:  index,
			Before: previous,
			After:  item,
		})
	}
	return changes
}

// taskItemsEquivalent 只比较 v2 需要对账的稳定字段。
func taskItemsEquivalent(left TaskItem, right TaskItem) bool {
	return left.Description == right.Description &&
		left.Status == right.Status &&
		stringValue(left.Result) == stringValue(right.Result) &&
		stringValue(left.Error) == stringValue(right.Error) &&
		strings.Join(left.FilesModified, "\x00") == strings.Join(right.FilesModified, "\x00")
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func eventTitleForTaskChange(change taskItemChange) string {
	switch change.After.Status {
	case TaskStatusCompleted:
		return "完成计划项: " + change.After.Description
	case TaskStatusFailed:
		return "计划项失败: " + change.After.Description
	case TaskStatusInProgress:
		return "开始计划项: " + change.After.Description
	default:
		return "更新计划项: " + change.After.Description
	}
}

func decisionForTaskStatus(status TaskStatus) Decision {
	switch status {
	case TaskStatusCompleted:
		return DecisionApprove
	case TaskStatusFailed:
		return DecisionRetry
	case TaskStatusInProgress:
		return DecisionDefer
	default:
		return DecisionDefer
	}
}

func reasonForTaskChange(change taskItemChange) string {
	if change.After.Result != nil && strings.TrimSpace(*change.After.Result) != "" {
		return *change.After.Result
	}
	if change.After.Error != nil && strings.TrimSpace(*change.After.Error) != "" {
		return *change.After.Error
	}
	if change.Before == nil {
		return "v1 progress tracker created this task item"
	}
	return fmt.Sprintf("v1 progress tracker changed status from %s to %s", change.Before.Status, change.After.Status)
}

func compensateOpForTaskStatus(status TaskStatus) string {
	if status == TaskStatusCompleted {
		return "mark_task_needs_rework"
	}
	return ""
}
