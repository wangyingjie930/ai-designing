package claudetasklist

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

const (
	TaskCreateToolName = "TaskCreate"
	TaskGetToolName    = "TaskGet"
	TaskUpdateToolName = "TaskUpdate"
	TaskListToolName   = "TaskList"
)

// Toolset 把同一 Store 的确定性 CRUD 能力暴露为四个模型工具执行边界。
type Toolset struct {
	Store *Store
}

// BuildTools 构造 Claude Code 风格的四个 Task 工具，并让它们共享同一份文件真相。
func BuildTools(store *Store) ([]tool.BaseTool, error) {
	if store == nil {
		return nil, errors.New("task store is required")
	}
	toolset := Toolset{Store: store}
	createTool, err := toolutils.InferTool[TaskCreateRequest, *TaskCreateResponse](
		TaskCreateToolName,
		"创建一个需要机械跟踪的新任务；新任务固定从 pending 状态开始。",
		toolset.Create,
	)
	if err != nil {
		return nil, fmt.Errorf("build %s tool: %w", TaskCreateToolName, err)
	}
	getTool, err := toolutils.InferTool[TaskGetRequest, *TaskGetResponse](
		TaskGetToolName,
		"按任务 ID 读取完整详情，包括描述、负责人、依赖和扩展信息。",
		toolset.Get,
	)
	if err != nil {
		return nil, fmt.Errorf("build %s tool: %w", TaskGetToolName, err)
	}
	updateTool, err := toolutils.InferTool[TaskUpdateRequest, *TaskUpdateResult](
		TaskUpdateToolName,
		"更新任务字段、状态或依赖；status=deleted 表示删除任务。",
		toolset.Update,
	)
	if err != nil {
		return nil, fmt.Errorf("build %s tool: %w", TaskUpdateToolName, err)
	}
	listTool, err := toolutils.InferTool[TaskListRequest, *TaskListResponse](
		TaskListToolName,
		"按数字 ID 查看任务摘要；只返回尚未完成的阻塞依赖。",
		toolset.List,
	)
	if err != nil {
		return nil, fmt.Errorf("build %s tool: %w", TaskListToolName, err)
	}
	return []tool.BaseTool{createTool, getTool, updateTool, listTool}, nil
}

// Create 创建 pending 任务，并返回已经落盘的完整记录。
func (t Toolset) Create(ctx context.Context, request TaskCreateRequest) (*TaskCreateResponse, error) {
	if t.Store == nil {
		return nil, errors.New("task store is required")
	}
	task, err := t.Store.Create(ctx, request)
	if err != nil {
		return nil, err
	}
	return &TaskCreateResponse{Task: task}, nil
}

// Get 读取完整任务；TaskList 的低成本摘要不能替代这个精确读取边界。
func (t Toolset) Get(ctx context.Context, request TaskGetRequest) (*TaskGetResponse, error) {
	if t.Store == nil {
		return nil, errors.New("task store is required")
	}
	task, err := t.Store.Get(ctx, request.TaskID)
	if err != nil {
		return nil, err
	}
	return &TaskGetResponse{Task: task}, nil
}

// Update 把模型变更交给 Store 做完整校验、依赖维护和原子落盘。
func (t Toolset) Update(ctx context.Context, request TaskUpdateRequest) (*TaskUpdateResult, error) {
	if t.Store == nil {
		return nil, errors.New("task store is required")
	}
	return t.Store.Update(ctx, request)
}

// List 返回任务摘要，避免把所有描述和 metadata 重复塞回模型上下文。
func (t Toolset) List(ctx context.Context, _ TaskListRequest) (*TaskListResponse, error) {
	if t.Store == nil {
		return nil, errors.New("task store is required")
	}
	tasks, err := t.Store.ListSummaries(ctx)
	if err != nil {
		return nil, err
	}
	return &TaskListResponse{Tasks: tasks}, nil
}
