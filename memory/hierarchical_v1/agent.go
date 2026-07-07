package hierarchicalv1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	WriteMemoryToolName       = "hierarchical_v1_write_memory"
	PromoteScratchpadToolName = "hierarchical_v1_promote_scratchpad"
	AssembleContextToolName   = "hierarchical_v1_assemble_context"
	HealthReportToolName      = "hierarchical_v1_health_report"
)

// Toolset 把 HierarchicalMemory 的 write/propose/assemble/health 暴露给 Eino ADK。
type Toolset struct {
	Memory *HierarchicalMemory
}

// Agent 用 Eino ADK ChatModelAgent 编排家庭膳食规划场景和五层记忆策略。
type Agent struct {
	runner *adk.Runner
}

// NewWriteMemoryTool 创建“按策略写入 MemoryEntry”的 ADK 工具。
func NewWriteMemoryTool(memory *HierarchicalMemory) (tool.BaseTool, error) {
	if memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	toolset := Toolset{Memory: memory}
	return toolutils.InferTool[WriteRequest, *WriteResponse](
		WriteMemoryToolName,
		"写入一条分层记忆。更新同一业务对象时必须复用 assemble_context.entries 中已有 key 做 upsert，不要因 status/进度/约束变化创建新 key。",
		toolset.WriteMemory,
	)
}

// NewPromoteScratchpadTool 创建“把已验证 scratchpad 写回正式层”的 ADK 工具。
func NewPromoteScratchpadTool(memory *HierarchicalMemory) (tool.BaseTool, error) {
	if memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	toolset := Toolset{Memory: memory}
	return toolutils.InferTool[PromoteScratchpadRequest, *PromoteScratchpadResponse](
		PromoteScratchpadToolName,
		"把已被证据确认的 scratchpad 条目提升为 verified_trace 记忆并写回正式层；目标层由工程代码根据 key 前缀决定，模型不能传 target_layer。",
		toolset.PromoteScratchpad,
	)
}

// NewAssembleContextTool 创建“按 policy/project/user/task/scratchpad 组装上下文”的 ADK 工具。
func NewAssembleContextTool(memory *HierarchicalMemory) (tool.BaseTool, error) {
	if memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	toolset := Toolset{Memory: memory}
	return toolutils.InferTool[AssembleContextRequest, *AssembleContextResponse](
		AssembleContextToolName,
		"按层顺序和 token_budget 选择未过期记忆，并按 confidence 与最近访问时间排序。",
		toolset.AssembleContext,
	)
}

// NewHealthReportTool 创建“读取层级策略健康状态”的 ADK 工具。
func NewHealthReportTool(memory *HierarchicalMemory) (tool.BaseTool, error) {
	if memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	toolset := Toolset{Memory: memory}
	return toolutils.InferTool[HealthReportRequest, *HealthReport](
		HealthReportToolName,
		"读取每层 backend、token_budget、ttl_seconds、allow_agent_write、require_evidence 和 entry_count。",
		toolset.HealthReport,
	)
}

// WriteMemory 是 write tool 的执行边界，只负责策略校验和记忆写入。
func (t Toolset) WriteMemory(ctx context.Context, req WriteRequest) (*WriteResponse, error) {
	if t.Memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	return t.Memory.Write(ctx, req)
}

// PromoteScratchpad 是 scratchpad promotion tool 的执行边界，只接收 key 和证据，层级由工程代码决定。
func (t Toolset) PromoteScratchpad(ctx context.Context, req PromoteScratchpadRequest) (*PromoteScratchpadResponse, error) {
	if t.Memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	return t.Memory.PromoteScratchpadByKey(ctx, req)
}

// AssembleContext 是 context tool 的执行边界，只负责选择当前可见记忆。
func (t Toolset) AssembleContext(ctx context.Context, req AssembleContextRequest) (*AssembleContextResponse, error) {
	if t.Memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	return t.Memory.AssembleContextTool(ctx, req)
}

// HealthReport 是 health tool 的执行边界，只返回层策略摘要，不做业务判断。
func (t Toolset) HealthReport(ctx context.Context, req HealthReportRequest) (*HealthReport, error) {
	if t.Memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	return t.Memory.Health(ctx, req)
}

// NewMealPlanningAgent 创建家庭膳食规划 Agent，用非 coding 场景演示五层记忆策略。
func NewMealPlanningAgent(ctx context.Context, config AgentConfig) (*Agent, error) {
	if config.Model == nil {
		return nil, errors.New("agent model is required")
	}
	if config.Memory == nil {
		return nil, errors.New("hierarchical memory is required")
	}
	if err := adk.SetLanguage(adk.LanguageChinese); err != nil {
		return nil, fmt.Errorf("set adk language: %w", err)
	}
	tools, err := buildMemoryTools(config.Memory, config.ExtraTools)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = "meal_planning_hierarchical_v1_agent"
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = "Meal planning agent using policy/project/user/task/scratchpad hierarchical memory."
	}
	instruction := strings.TrimSpace(config.Instruction)
	if instruction == "" {
		instruction = DefaultMealPlanningInstruction()
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
		return nil, fmt.Errorf("create meal planning agent: %w", err)
	}
	return &Agent{runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})}, nil
}

// Query 处理一条自然语言膳食规划消息，工具调用由模型基于五层策略自主决定。
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
		"请处理下面这条家庭膳食规划消息。",
		"你必须自己决定如何调用工具，不要等待外部代码替你喂答案。",
		"回答前先调用 " + AssembleContextToolName + " 读取当前五层记忆；必要时调用 " + HealthReportToolName + " 查看每层写入策略。",
		"五层含义：policy=不可轻易变更的饮食安全/合规规则，project=当前周计划或聚餐项目规则，user=跨会话家庭偏好，task=本次任务进展，scratchpad=本轮临时推断和待验证线索。",
		"写入时必须设置 source：用户明确提供用 human，工具结果用 tool，你自己的推断用 agent_inference，scratchpad 提升候选用 verified_trace。",
		"policy/project 不能用 agent_inference 直接写；user/task 需要可信来源或 evidence_refs；scratchpad 不要求 evidence，适合暂存未验证推断。",
		"何时写入：本轮刚形成但未验证的观察或推断，立即写 scratchpad；用户确认、工具返回决定性结果、任务完成/失败/阻塞、外部副作用已经发生，立即写 task。",
		"key 复用规则：写 task/project/user 前必须先查看 assemble_context 返回的 entries；如果已有 entry 描述同一个业务对象，必须复用它的 key 做 upsert。",
		"不要因为任务 status 从 started 变成 completed、进度新增、约束新增、菜单更新，就创建新的 task key；这些变化应写进同一个 key 的 value。",
		"推荐 task key 形态：task:<稳定业务对象>，例如 task:current_week_family_dinner_plan；project/user 也要使用能跨轮复用的稳定 key。",
		"低频写入：长期偏好、稳定禁忌、跨会话家庭事实，只在用户明确表达或有证据时写 user；当前周计划的预算、天数、目标和项目边界，只在明确确认时写 project。",
		"不要只等最后一轮才写关键 task 状态；每轮结束前做一次轻量兜底，检查是否有已确认但尚未写入的 user/project/task 事实。",
		"如果 scratchpad 条目已经被证据验证，调用 " + PromoteScratchpadToolName + " 并提供 evidence_refs；正式层级由工程代码根据 key 前缀决定。",
		"最终答复要说明：用了哪些记忆层、是否写入新记忆、哪些内容仍留在 scratchpad 待验证。",
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

// DefaultMealPlanningInstruction 定义非 coding 膳食规划 Agent 的五层记忆边界。
func DefaultMealPlanningInstruction() string {
	return strings.Join([]string{
		"你是一个中文家庭膳食规划 Agent，目标是把家庭成员偏好、健康限制、预算和当周安排变成可执行菜单。",
		"每轮先读取 hierarchical_v1_assemble_context，不能凭空补充工具没有返回的历史事实。",
		"写入 policy/project/user/task 时要尊重 evidence 和 source：稳定事实可用 human/tool，纯推断先写 scratchpad。",
		"关键状态实时写 task：用户确认、工具决定性结果、完成、失败、阻塞、外部副作用发生，都不要等会话结束。",
		"更新同一任务或同一项目时必须复用 assemble_context.entries 里的已有 key；状态、进度、约束和产出变化都写入同一个 key 的 value。",
		"只有业务对象真的不同才创建新 key；不要用 started/completed/progress 这类状态词派生新的 task key。",
		"一般状态批量写：长期偏好写 user，当前项目规则写 project，但必须是明确表达或带证据的稳定事实。",
		"scratchpad 是临时态，不要把它当成已验证事实；只有经过 evidence 或工具/用户确认后，才通过 promote_scratchpad 写回正式层。",
		"每轮结束前做轻量兜底：如果发现已确认的重要事实还没保存，先写入对应层再回答。",
		"回答要面向做饭的人：给出菜单建议、采购或准备动作、依据的记忆层，以及本轮记忆写入情况。",
	}, "\n")
}

// buildMemoryTools 创建 Agent 默认工具集合，并保留外部扩展工具插入点。
func buildMemoryTools(memory *HierarchicalMemory, extraTools []tool.BaseTool) ([]tool.BaseTool, error) {
	assembleTool, err := NewAssembleContextTool(memory)
	if err != nil {
		return nil, err
	}
	healthTool, err := NewHealthReportTool(memory)
	if err != nil {
		return nil, err
	}
	writeTool, err := NewWriteMemoryTool(memory)
	if err != nil {
		return nil, err
	}
	promoteTool, err := NewPromoteScratchpadTool(memory)
	if err != nil {
		return nil, err
	}
	tools := []tool.BaseTool{assembleTool, healthTool, writeTool, promoteTool}
	tools = append(tools, extraTools...)
	return tools, nil
}
