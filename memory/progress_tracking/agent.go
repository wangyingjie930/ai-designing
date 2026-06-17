package progresstracking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	CreatePlanToolName        = "progress_create_plan"
	StartTaskToolName         = "progress_start_task"
	CompleteTaskToolName      = "progress_complete_task"
	FailTaskToolName          = "progress_fail_task"
	ResumptionContextToolName = "progress_resumption_context"
)

// Toolset 把 ProgressTracker 的 create/start/complete/fail/resume 暴露给 Eino ADK。
type Toolset struct {
	Tracker *ProgressTracker
}

// AgentConfig 配置一个会使用 progress tracker 的非 coding 场景 Agent。
type AgentConfig struct {
	Name          string
	Description   string
	Instruction   string
	Model         model.BaseChatModel
	Tracker       *ProgressTracker
	ExtraTools    []tool.BaseTool
	MaxIterations int
}

// Agent 用 Eino ADK 编排线下活动筹备场景和 progress tracker 工具。
type Agent struct {
	runner *adk.Runner
}

// AgentRequest 是业务 Agent 的自然语言输入，不要求用户手写工具参数。
type AgentRequest struct {
	Message string `json:"message"`
}

// AgentResponse 返回业务 Agent 对当前消息的处理结果。
type AgentResponse struct {
	Message string `json:"message"`
}

// NewCreatePlanTool 创建“初始化计划”的 ADK 工具。
func NewCreatePlanTool(tracker *ProgressTracker) (tool.BaseTool, error) {
	if tracker == nil {
		return nil, errors.New("progress tracker is required")
	}
	toolset := Toolset{Tracker: tracker}
	return toolutils.InferTool[CreatePlanRequest, *CreatePlanResponse](
		CreatePlanToolName,
		"根据用户目标拆出任务列表并初始化计划。已有计划需要重建时必须显式传 reset=true。",
		toolset.CreatePlan,
	)
}

// NewStartTaskTool 创建“标记任务进行中”的 ADK 工具。
func NewStartTaskTool(tracker *ProgressTracker) (tool.BaseTool, error) {
	if tracker == nil {
		return nil, errors.New("progress tracker is required")
	}
	toolset := Toolset{Tracker: tracker}
	return toolutils.InferTool[StartTaskRequest, *TaskUpdateResponse](
		StartTaskToolName,
		"把指定 0-based index 的任务标记为 in_progress，用于长任务 checkpoint。",
		toolset.StartTask,
	)
}

// NewCompleteTaskTool 创建“完成任务并 checkpoint”的 ADK 工具。
func NewCompleteTaskTool(tracker *ProgressTracker) (tool.BaseTool, error) {
	if tracker == nil {
		return nil, errors.New("progress tracker is required")
	}
	toolset := Toolset{Tracker: tracker}
	return toolutils.InferTool[CompleteTaskRequest, *TaskUpdateResponse](
		CompleteTaskToolName,
		"完成指定 0-based index 的任务，result 写实际结果，files 可写相关文件、材料或凭证引用。",
		toolset.CompleteTask,
	)
}

// NewFailTaskTool 创建“记录任务失败”的 ADK 工具。
func NewFailTaskTool(tracker *ProgressTracker) (tool.BaseTool, error) {
	if tracker == nil {
		return nil, errors.New("progress tracker is required")
	}
	toolset := Toolset{Tracker: tracker}
	return toolutils.InferTool[FailTaskRequest, *TaskUpdateResponse](
		FailTaskToolName,
		"记录指定 0-based index 的任务失败原因，便于恢复后优先处理阻塞。",
		toolset.FailTask,
	)
}

// NewResumptionContextTool 创建“读取恢复上下文”的 ADK 工具。
func NewResumptionContextTool(tracker *ProgressTracker) (tool.BaseTool, error) {
	if tracker == nil {
		return nil, errors.New("progress tracker is required")
	}
	toolset := Toolset{Tracker: tracker}
	return toolutils.InferTool[ResumptionContextRequest, *ResumptionContextResponse](
		ResumptionContextToolName,
		"读取当前计划的恢复上下文。每轮行动前都应先调用。",
		toolset.ResumptionContext,
	)
}

// CreatePlan 是 create tool 的执行边界，只负责初始化计划，不替 Agent 做业务判断。
func (t Toolset) CreatePlan(ctx context.Context, req CreatePlanRequest) (*CreatePlanResponse, error) {
	if t.Tracker == nil {
		return nil, errors.New("progress tracker is required")
	}
	if len(t.Tracker.Items()) > 0 && !req.Reset {
		return nil, errors.New("plan already exists; pass reset=true only when the user explicitly wants to rebuild it")
	}
	if err := t.Tracker.CreatePlan(ctx, req.Items); err != nil {
		return nil, err
	}
	return &CreatePlanResponse{
		PlanID: t.Tracker.PlanID(),
		Items:  t.Tracker.Items(),
	}, nil
}

// StartTask 是 start tool 的执行边界，更新状态后返回恢复上下文。
func (t Toolset) StartTask(ctx context.Context, req StartTaskRequest) (*TaskUpdateResponse, error) {
	if t.Tracker == nil {
		return nil, errors.New("progress tracker is required")
	}
	if err := t.Tracker.Start(ctx, req.Index); err != nil {
		return nil, err
	}
	return t.taskUpdateResponse(req.Index), nil
}

// CompleteTask 是 complete tool 的执行边界，保留 Python complete 的参数语义。
func (t Toolset) CompleteTask(ctx context.Context, req CompleteTaskRequest) (*TaskUpdateResponse, error) {
	if t.Tracker == nil {
		return nil, errors.New("progress tracker is required")
	}
	if err := t.Tracker.Complete(ctx, req.Index, req.Result, req.Files); err != nil {
		return nil, err
	}
	return t.taskUpdateResponse(req.Index), nil
}

// FailTask 是 fail tool 的执行边界，写入失败原因后立即 checkpoint。
func (t Toolset) FailTask(ctx context.Context, req FailTaskRequest) (*TaskUpdateResponse, error) {
	if t.Tracker == nil {
		return nil, errors.New("progress tracker is required")
	}
	if err := t.Tracker.Fail(ctx, req.Index, req.Error); err != nil {
		return nil, err
	}
	return t.taskUpdateResponse(req.Index), nil
}

// ResumptionContext 是 resume tool 的执行边界，返回给模型的文本与本地方法一致。
func (t Toolset) ResumptionContext(context.Context, ResumptionContextRequest) (*ResumptionContextResponse, error) {
	if t.Tracker == nil {
		return nil, errors.New("progress tracker is required")
	}
	return &ResumptionContextResponse{
		PlanID:  t.Tracker.PlanID(),
		Context: t.Tracker.ResumptionContext(),
	}, nil
}

// taskUpdateResponse 在状态变更后给 Agent 同时返回单项和整体上下文。
func (t Toolset) taskUpdateResponse(index int) *TaskUpdateResponse {
	items := t.Tracker.Items()
	item := TaskItem{}
	if index >= 0 && index < len(items) {
		item = items[index]
	}
	return &TaskUpdateResponse{
		PlanID:            t.Tracker.PlanID(),
		Item:              item,
		ResumptionContext: t.Tracker.ResumptionContext(),
	}
}

// NewEventPlanningAgent 创建线下活动筹备 Agent，用非 coding 场景演示 crash-recoverable progress tracking。
func NewEventPlanningAgent(ctx context.Context, config AgentConfig) (*Agent, error) {
	if config.Model == nil {
		return nil, errors.New("agent model is required")
	}
	if config.Tracker == nil {
		return nil, errors.New("progress tracker is required")
	}
	if err := adk.SetLanguage(adk.LanguageChinese); err != nil {
		return nil, fmt.Errorf("set adk language: %w", err)
	}
	tools, err := buildProgressTools(config.Tracker, config.ExtraTools)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = "event_planning_progress_agent"
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = "Event planning agent that uses a SQLite progress tracker for crash recovery."
	}
	instruction := strings.TrimSpace(config.Instruction)
	if instruction == "" {
		instruction = DefaultEventPlanningInstruction()
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 10
	}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          name,
		Description:   description,
		Instruction:   instruction,
		Model:         config.Model,
		MaxIterations: maxIterations,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create event planning agent: %w", err)
	}
	return &Agent{runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})}, nil
}

// Query 处理一条自然语言活动筹备消息，工具调用由模型按当前恢复上下文决定。
func (a *Agent) Query(ctx context.Context, req AgentRequest) (*AgentResponse, error) {
	if a == nil || a.runner == nil {
		return nil, errors.New("agent is not initialized")
	}
	if strings.TrimSpace(req.Message) == "" {
		return nil, errors.New("agent request message is empty")
	}
	payload, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, err
	}
	query := strings.Join([]string{
		"请处理下面这条线下活动筹备消息。",
		"你必须自己决定是否以及如何调用工具，不要等待外部代码替你拆任务或标记状态。",
		"每轮行动前必须先调用 " + ResumptionContextToolName + " 查看当前进度。",
		"如果还没有计划，并且用户给的是筹备目标或约束，调用 " + CreatePlanToolName + " 拆出可执行任务；不要把最终答案写死进任务。",
		"如果用户明确说明某个任务已经完成，调用 " + CompleteTaskToolName + "；如果明确说明失败或阻塞，调用 " + FailTaskToolName + "。",
		"如果需要开始推进某个未完成任务，调用 " + StartTaskToolName + " 标记当前焦点。",
		"最终答复请给出：当前进度、下一步建议、是否更新了 checkpoint。",
		"",
		string(payload),
	}, "\n")

	iter := a.runner.Query(ctx, query)
	var final string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return nil, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		if event.Output.MessageOutput.Role != schema.Assistant {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return nil, err
		}
		if message != nil && strings.TrimSpace(message.Content) != "" {
			final = message.Content
		}
	}
	if strings.TrimSpace(final) == "" {
		return nil, errors.New("agent finished without assistant output")
	}
	return &AgentResponse{Message: final}, nil
}

// DefaultEventPlanningInstruction 定义活动筹备 Agent 的工具顺序和记录边界。
func DefaultEventPlanningInstruction() string {
	return strings.Join([]string{
		"你是一个线下活动筹备 Agent，负责把用户的活动目标拆成可执行计划，并在执行过程中持续 checkpoint。",
		"每次回答前必须先调用 progress_resumption_context，不能凭记忆猜当前进度。",
		"没有计划时，基于用户给出的活动目标、人数、预算、场地、嘉宾、物料、排期等事实调用 progress_create_plan。",
		"计划任务必须是可执行动作，例如确认场地合同、锁定嘉宾档期、准备签到物料、发布报名页；不要把最终结论或补偿话术当成已完成结果。",
		"只有用户明确提供完成证据或执行结果时，才调用 progress_complete_task；只有用户明确提供失败原因或阻塞时，才调用 progress_fail_task。",
		"当用户只是询问下一步时，优先选择第一个 pending 或 failed 任务给建议，可用 progress_start_task 标记当前焦点。",
		"回答要面向活动负责人：说明当前进度、下一步动作、风险边界、以及本轮是否写入 SQLite checkpoint。",
	}, "\n")
}

// buildProgressTools 创建 Agent 默认工具集合，并保留外部扩展工具的插入点。
func buildProgressTools(tracker *ProgressTracker, extraTools []tool.BaseTool) ([]tool.BaseTool, error) {
	createTool, err := NewCreatePlanTool(tracker)
	if err != nil {
		return nil, err
	}
	startTool, err := NewStartTaskTool(tracker)
	if err != nil {
		return nil, err
	}
	completeTool, err := NewCompleteTaskTool(tracker)
	if err != nil {
		return nil, err
	}
	failTool, err := NewFailTaskTool(tracker)
	if err != nil {
		return nil, err
	}
	resumeTool, err := NewResumptionContextTool(tracker)
	if err != nil {
		return nil, err
	}
	tools := []tool.BaseTool{resumeTool, createTool, startTool, completeTool, failTool}
	tools = append(tools, extraTools...)
	return tools, nil
}
