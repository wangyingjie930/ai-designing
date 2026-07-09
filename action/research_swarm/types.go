package researchswarm

import "time"

const (
	defaultTeamName     = "research-demo"
	defaultLeaderName   = "report_director"
	defaultSearchAgent  = "searcher"
	defaultAnalystAgent = "analyst"
	defaultWriterAgent  = "writer"
)

// AgentRole 描述调查报告 swarm 中每个 teammate 的职责边界。
type AgentRole string

const (
	RoleReportDirector AgentRole = "report_director"
	RoleSearcher       AgentRole = "searcher"
	RoleAnalyst        AgentRole = "analyst"
	RoleWriter         AgentRole = "writer"
)

// WorkerStatus 表示外部 worker 进程在 team 中的生命周期状态。
type WorkerStatus string

const (
	WorkerStatusStarting WorkerStatus = "starting"
	WorkerStatusIdle     WorkerStatus = "idle"
	WorkerStatusRunning  WorkerStatus = "running"
	WorkerStatusStopped  WorkerStatus = "stopped"
	WorkerStatusFailed   WorkerStatus = "failed"
)

// MessageKind 区分 mailbox 中的任务、普通通知和关闭控制消息。
type MessageKind string

const (
	MessageKindTask          MessageKind = "task"
	MessageKindNotification  MessageKind = "notification"
	MessageKindTaskCompleted MessageKind = "task_completed"
	MessageKindShutdown      MessageKind = "shutdown"
)

// TaskStatus 描述调查任务在共享 SQLite 中的执行状态。
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
)

// SearchProvider 标识 web_search 工具背后的搜索供应商。
type SearchProvider string

const (
	SearchProviderFake     SearchProvider = "fake"
	SearchProviderHTTPJSON SearchProvider = "http_json"
)

// Member 是写入 swarm_members 的 teammate 身份和心跳状态。
type Member struct {
	AgentID    string
	TeamName   string
	Name       string
	Role       AgentRole
	Status     WorkerStatus
	PID        int
	LastSeenAt time.Time
	CreatedAt  time.Time
}

// MailboxMessage 是外部进程 teammate 之间唯一的持久化消息载体。
type MailboxMessage struct {
	ID          int64
	TeamName    string
	FromAgent   string
	ToAgent     string
	Kind        MessageKind
	ContentJSON string
	CreatedAt   time.Time
	ConsumedAt  *time.Time
}

// ResearchTask 表示 leader 分配给 worker 的可追踪调查任务。
type ResearchTask struct {
	ID         int64
	TeamName   string
	Assignee   string
	Title      string
	Status     TaskStatus
	ResultJSON string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// SourceCard 是搜索结果进入报告上下文前的证据卡片。
type SourceCard struct {
	ID          int64     `json:"id"`
	TeamName    string    `json:"team_name"`
	Query       string    `json:"query"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	Snippet     string    `json:"snippet"`
	Source      string    `json:"source"`
	Credibility string    `json:"credibility"`
	RetrievedAt time.Time `json:"retrieved_at"`
	CreatedBy   string    `json:"created_by"`
}

// ReportSection 是 analyst/writer 通过工具写入的报告章节。
type ReportSection struct {
	ID          int64     `json:"id"`
	TeamName    string    `json:"team_name"`
	Section     string    `json:"section"`
	Content     string    `json:"content"`
	EvidenceIDs []int64   `json:"evidence_ids"`
	CreatedBy   string    `json:"created_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TaskPayload 是 leader 投递给 worker 的 mailbox task 内容。
type TaskPayload struct {
	TaskID int64  `json:"task_id"`
	Topic  string `json:"topic"`
	Prompt string `json:"prompt"`
}

// TaskCompletionEvent 是 worker 回给 leader 的完成事件，leader 会把它作为下一轮 director 输入。
type TaskCompletionEvent struct {
	Type      string     `json:"type"`
	TaskID    int64      `json:"task_id"`
	AgentID   string     `json:"agent_id"`
	AgentName string     `json:"agent_name"`
	Role      AgentRole  `json:"role"`
	Status    TaskStatus `json:"status"`
	Artifact  string     `json:"artifact,omitempty"`
	Section   string     `json:"section,omitempty"`
	Summary   string     `json:"summary,omitempty"`
}

// LeaderDirectorInput 是 leader 喂给 report_director 的每一轮输入。
type LeaderDirectorInput struct {
	Type     string               `json:"type"`
	TeamName string               `json:"team_name"`
	Topic    string               `json:"topic"`
	Event    *TaskCompletionEvent `json:"event,omitempty"`
}

// LeaderResult 是 leader 命令最终返回给 CLI 和测试的紧凑摘要。
type LeaderResult struct {
	TeamName           string          `json:"team_name"`
	Topic              string          `json:"topic"`
	SourceCardCount    int             `json:"source_card_count"`
	ReportSectionCount int             `json:"report_section_count"`
	FailedWorkerCount  int             `json:"failed_worker_count"`
	FinalReport        string          `json:"final_report"`
	SourceCards        []SourceCard    `json:"source_cards,omitempty"`
	ReportSections     []ReportSection `json:"report_sections,omitempty"`
}

// AgentID 使用 Claude Code teammate 风格的稳定身份格式：<agent_name>@<team_name>。
func AgentID(agentName string, teamName string) string {
	return agentName + "@" + teamName
}
