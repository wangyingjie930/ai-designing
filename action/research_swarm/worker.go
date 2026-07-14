package researchswarm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudwego/eino/adk"
)

// WorkerConfig 配置一个外部 worker 进程的 mailbox 轮询和 ADK 执行路径。
type WorkerConfig struct {
	TeamName      string
	AgentName     string
	Role          AgentRole
	Store         *Store
	SearchClient  SearchClient
	PollInterval  time.Duration
	MaxIdleTicks  int
	MaxIterations int
}

// RunWorker 启动 worker 主循环：消费 mailbox、运行 ADK Agent、写回状态，直到收到 shutdown。
func RunWorker(ctx context.Context, config WorkerConfig) error {
	if config.Store == nil {
		return fmt.Errorf("store is required")
	}
	if config.TeamName == "" {
		config.TeamName = defaultTeamName
	}
	if config.AgentName == "" {
		return fmt.Errorf("agent name is required")
	}
	if config.Role == "" {
		config.Role = roleFromAgentName(config.AgentName)
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 200 * time.Millisecond
	}
	agentID := AgentID(config.AgentName, config.TeamName)
	if err := config.Store.UpsertMember(ctx, Member{
		TeamName: config.TeamName,
		AgentID:  agentID,
		Name:     config.AgentName,
		Role:     config.Role,
		Status:   WorkerStatusIdle,
	}); err != nil {
		return fmt.Errorf("upsert idle member: %w", err)
	}
	agent, err := NewRoleAgent(ctx, RoleAgentConfig{
		Store:         config.Store,
		SearchClient:  config.SearchClient,
		TeamName:      config.TeamName,
		AgentName:     config.AgentName,
		Role:          config.Role,
		MaxIterations: config.MaxIterations,
	})
	if err != nil {
		return fmt.Errorf("create role agent: %w", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	idleTicks := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		messages, err := config.Store.ConsumeMessages(ctx, config.TeamName, agentID, 1)
		if err != nil {
			if isSQLiteLocked(err) {
				time.Sleep(config.PollInterval)
				continue
			}
			return fmt.Errorf("consume mailbox: %w", err)
		}
		if len(messages) == 0 {
			idleTicks++
			// 空闲轮询不做每轮 heartbeat 写入，避免多个外部进程在 SQLite 上制造无意义写锁。
			if config.MaxIdleTicks > 0 && idleTicks >= config.MaxIdleTicks {
				return nil
			}
			time.Sleep(config.PollInterval)
			continue
		}
		idleTicks = 0
		for _, message := range messages {
			if message.Kind == MessageKindShutdown {
				if err := config.Store.UpsertMember(ctx, Member{TeamName: config.TeamName, AgentID: agentID, Name: config.AgentName, Role: config.Role, Status: WorkerStatusStopped}); err != nil {
					return fmt.Errorf("mark worker stopped: %w", err)
				}
				return nil
			}
			if err := runWorkerMessage(ctx, config, runner, agentID, message); err != nil {
				_ = config.Store.UpsertMember(ctx, Member{TeamName: config.TeamName, AgentID: agentID, Name: config.AgentName, Role: config.Role, Status: WorkerStatusFailed})
				return err
			}
		}
	}
}

// runWorkerMessage 把一条 mailbox task 转成 ADK Runner 查询，并等待工具循环结束。
func runWorkerMessage(ctx context.Context, config WorkerConfig, runner *adk.Runner, agentID string, message MailboxMessage) error {
	if err := config.Store.UpsertMember(ctx, Member{TeamName: config.TeamName, AgentID: agentID, Name: config.AgentName, Role: config.Role, Status: WorkerStatusRunning}); err != nil {
		return fmt.Errorf("mark worker running: %w", err)
	}
	payload := TaskPayload{Prompt: message.ContentJSON}
	_ = json.Unmarshal([]byte(message.ContentJSON), &payload)
	queryBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	iter := runner.Query(ctx, string(queryBytes))
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			if payload.TaskID > 0 {
				_ = config.Store.UpdateTask(ctx, payload.TaskID, TaskStatusFailed, fmt.Sprintf(`{"error":%q}`, event.Err.Error()))
			}
			_ = config.Store.UpsertMember(ctx, Member{TeamName: config.TeamName, AgentID: agentID, Name: config.AgentName, Role: config.Role, Status: WorkerStatusFailed})
			_ = notifyLeaderIdle(ctx, config, agentID, payload.TaskID, "failed", "当前任务执行失败")
			return fmt.Errorf("run adk agent: %w", event.Err)
		}
	}
	if err := config.Store.UpsertMember(ctx, Member{TeamName: config.TeamName, AgentID: agentID, Name: config.AgentName, Role: config.Role, Status: WorkerStatusIdle}); err != nil {
		return fmt.Errorf("mark worker idle: %w", err)
	}
	if err := notifyLeaderIdle(ctx, config, agentID, payload.TaskID, "available", "当前轮次已结束，等待下一步"); err != nil {
		return fmt.Errorf("notify leader idle: %w", err)
	}
	return nil
}

// notifyLeaderIdle 只上报 worker 生命周期状态，业务结果必须由模型显式调用 send_message 汇报。
func notifyLeaderIdle(ctx context.Context, config WorkerConfig, agentID string, taskID int64, idleReason string, summary string) error {
	completedStatus := ""
	if taskID > 0 {
		task, err := config.Store.GetTask(ctx, taskID)
		if err != nil {
			return fmt.Errorf("get task for idle notification: %w", err)
		}
		completedStatus = completedStatusForTask(task.Status)
	}
	notification := IdleNotification{
		Type:            "idle_notification",
		AgentName:       config.AgentName,
		IdleReason:      idleReason,
		CompletedTaskID: taskID,
		CompletedStatus: completedStatus,
		Summary:         summary,
	}
	raw, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	_, err = config.Store.EnqueueMessage(ctx, MailboxMessage{
		TeamName:    config.TeamName,
		FromAgent:   agentID,
		ToAgent:     AgentID(defaultLeaderName, config.TeamName),
		Kind:        MessageKindNotification,
		ContentJSON: string(raw),
	})
	return err
}

// completedStatusForTask 把共享任务状态映射成 Claude Code idle notification 的完成摘要。
func completedStatusForTask(status TaskStatus) string {
	switch status {
	case TaskStatusCompleted:
		return "resolved"
	case TaskStatusFailed:
		return "failed"
	default:
		return ""
	}
}
