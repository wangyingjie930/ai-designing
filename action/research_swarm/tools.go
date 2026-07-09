package researchswarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

const (
	SendMessageToolName        = "send_message"
	UpdateTaskToolName         = "update_task"
	WebSearchToolName          = "web_search"
	SaveSourceCardToolName     = "save_source_card"
	ListSourceCardsToolName    = "list_source_cards"
	SaveReportSectionToolName  = "save_report_section"
	ListReportSectionsToolName = "list_report_sections"
)

// ToolConfig 汇总角色工具执行时需要的 team 身份和共享依赖。
type ToolConfig struct {
	Store        *Store
	SearchClient SearchClient
	TeamName     string
	AgentID      string
	Role         AgentRole
}

type sendMessageRequest struct {
	ToAgent string `json:"to_agent" jsonschema:"required" jsonschema_description:"跨 agent 消息接收者，必须是 name@team 格式"`
	Kind    string `json:"kind" jsonschema:"required,enum=notification,enum=task,enum=shutdown" jsonschema_description:"消息类型，普通协作消息使用 notification"`
	Content string `json:"content" jsonschema:"required" jsonschema_description:"消息正文；需要结构化内容时传 JSON 字符串"`
}

type sendMessageResponse struct {
	MessageID int64  `json:"message_id"`
	ToAgent   string `json:"to_agent"`
	Kind      string `json:"kind"`
}

type updateTaskRequest struct {
	TaskID int64  `json:"task_id" jsonschema:"required" jsonschema_description:"要更新的 swarm_tasks.id"`
	Status string `json:"status" jsonschema:"required,enum=pending,enum=in_progress,enum=completed,enum=failed" jsonschema_description:"任务状态"`
	Result string `json:"result" jsonschema_description:"任务结果摘要或失败原因"`
}

type updateTaskResponse struct {
	TaskID int64  `json:"task_id"`
	Status string `json:"status"`
}

type saveSourceCardRequest struct {
	Query       string `json:"query" jsonschema:"required" jsonschema_description:"产生该资料卡的搜索 query"`
	Title       string `json:"title" jsonschema:"required" jsonschema_description:"资料标题"`
	URL         string `json:"url" jsonschema:"required" jsonschema_description:"资料 URL"`
	Snippet     string `json:"snippet" jsonschema:"required" jsonschema_description:"可用于调查报告的摘要片段"`
	Source      string `json:"source" jsonschema_description:"搜索来源或供应商"`
	Credibility string `json:"credibility" jsonschema_description:"证据可信度，例如 high、medium、low"`
}

type listSourceCardsRequest struct {
	Limit int `json:"limit,omitempty" jsonschema:"minimum=1,maximum=20,default=10" jsonschema_description:"最多返回多少张资料卡"`
}

type saveReportSectionRequest struct {
	Section     string  `json:"section" jsonschema:"required" jsonschema_description:"报告章节名，例如 事实归纳、冲突点、最终报告"`
	Content     string  `json:"content" jsonschema:"required" jsonschema_description:"章节内容"`
	EvidenceIDs []int64 `json:"evidence_ids" jsonschema_description:"该章节引用的 source_cards.id 列表"`
}

type listReportSectionsRequest struct {
	Limit int `json:"limit,omitempty" jsonschema:"minimum=1,maximum=20,default=10" jsonschema_description:"最多返回多少个报告章节"`
}

// NewRoleTools 根据 teammate 角色返回模型可见的工具集合。
func NewRoleTools(ctx context.Context, config ToolConfig) ([]tool.BaseTool, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if config.SearchClient == nil {
		config.SearchClient = NewFakeSearchClient()
	}
	common, err := commonTools(config)
	if err != nil {
		return nil, err
	}
	switch config.Role {
	case RoleSearcher:
		searchTool, err := newWebSearchTool(config.SearchClient)
		if err != nil {
			return nil, err
		}
		saveSource, err := newSaveSourceCardTool(config)
		if err != nil {
			return nil, err
		}
		return append([]tool.BaseTool{searchTool, saveSource}, common...), nil
	case RoleAnalyst:
		listSources, err := newListSourceCardsTool(config)
		if err != nil {
			return nil, err
		}
		saveSection, err := newSaveReportSectionTool(config)
		if err != nil {
			return nil, err
		}
		return append([]tool.BaseTool{listSources, saveSection}, common...), nil
	case RoleWriter:
		listSources, err := newListSourceCardsTool(config)
		if err != nil {
			return nil, err
		}
		listSections, err := newListReportSectionsTool(config)
		if err != nil {
			return nil, err
		}
		saveSection, err := newSaveReportSectionTool(config)
		if err != nil {
			return nil, err
		}
		return append([]tool.BaseTool{listSources, listSections, saveSection}, common...), nil
	default:
		return common, nil
	}
}

func commonTools(config ToolConfig) ([]tool.BaseTool, error) {
	send, err := toolutils.InferTool[sendMessageRequest, *sendMessageResponse](
		SendMessageToolName,
		"Send a mailbox message to another teammate. Normal assistant text is not visible to peers.",
		func(ctx context.Context, req sendMessageRequest) (*sendMessageResponse, error) {
			kind := MessageKind(strings.TrimSpace(req.Kind))
			if kind == "" {
				kind = MessageKindNotification
			}
			payload, err := json.Marshal(map[string]string{"content": req.Content})
			if err != nil {
				return nil, err
			}
			msg, err := config.Store.EnqueueMessage(ctx, MailboxMessage{
				TeamName:    config.TeamName,
				FromAgent:   config.AgentID,
				ToAgent:     req.ToAgent,
				Kind:        kind,
				ContentJSON: string(payload),
			})
			if err != nil {
				return nil, err
			}
			return &sendMessageResponse{MessageID: msg.ID, ToAgent: req.ToAgent, Kind: string(kind)}, nil
		},
	)
	if err != nil {
		return nil, err
	}
	update, err := toolutils.InferTool[updateTaskRequest, *updateTaskResponse](
		UpdateTaskToolName,
		"Update the current research task status in SQLite.",
		func(ctx context.Context, req updateTaskRequest) (*updateTaskResponse, error) {
			status := TaskStatus(req.Status)
			resultJSON, err := json.Marshal(map[string]string{"result": req.Result})
			if err != nil {
				return nil, err
			}
			if err := config.Store.UpdateTask(ctx, req.TaskID, status, string(resultJSON)); err != nil {
				return nil, err
			}
			return &updateTaskResponse{TaskID: req.TaskID, Status: string(status)}, nil
		},
	)
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{send, update}, nil
}

func newWebSearchTool(client SearchClient) (tool.BaseTool, error) {
	return toolutils.InferTool[SearchRequest, *SearchResponse](
		WebSearchToolName,
		"Search external sources for the investigation report. Save useful results as source cards before other teammates rely on them.",
		client.Search,
	)
}

func newSaveSourceCardTool(config ToolConfig) (tool.BaseTool, error) {
	return toolutils.InferTool[saveSourceCardRequest, *SourceCard](
		SaveSourceCardToolName,
		"Persist a verified search result as a source card for analyst and writer teammates.",
		func(ctx context.Context, req saveSourceCardRequest) (*SourceCard, error) {
			card, err := config.Store.SaveSourceCard(ctx, SourceCard{
				TeamName:    config.TeamName,
				Query:       req.Query,
				Title:       req.Title,
				URL:         req.URL,
				Snippet:     req.Snippet,
				Source:      firstNonEmpty(req.Source, string(SearchProviderFake)),
				Credibility: firstNonEmpty(req.Credibility, "medium"),
				CreatedBy:   config.AgentID,
			})
			if err != nil {
				return nil, err
			}
			return &card, nil
		},
	)
}

func newListSourceCardsTool(config ToolConfig) (tool.BaseTool, error) {
	return toolutils.InferTool[listSourceCardsRequest, []SourceCard](
		ListSourceCardsToolName,
		"List persisted source cards for this research team.",
		func(ctx context.Context, req listSourceCardsRequest) ([]SourceCard, error) {
			cards, err := config.Store.ListSourceCards(ctx, config.TeamName)
			if err != nil || req.Limit <= 0 || req.Limit >= len(cards) {
				return cards, err
			}
			return cards[:req.Limit], nil
		},
	)
}

func newSaveReportSectionTool(config ToolConfig) (tool.BaseTool, error) {
	return toolutils.InferTool[saveReportSectionRequest, *ReportSection](
		SaveReportSectionToolName,
		"Persist an analysis or writing section with source card references.",
		func(ctx context.Context, req saveReportSectionRequest) (*ReportSection, error) {
			section, err := config.Store.SaveReportSection(ctx, ReportSection{
				TeamName:    config.TeamName,
				Section:     req.Section,
				Content:     req.Content,
				EvidenceIDs: req.EvidenceIDs,
				CreatedBy:   config.AgentID,
			})
			if err != nil {
				return nil, err
			}
			return &section, nil
		},
	)
}

func newListReportSectionsTool(config ToolConfig) (tool.BaseTool, error) {
	return toolutils.InferTool[listReportSectionsRequest, []ReportSection](
		ListReportSectionsToolName,
		"List persisted report sections for this research team.",
		func(ctx context.Context, req listReportSectionsRequest) ([]ReportSection, error) {
			sections, err := config.Store.ListReportSections(ctx, config.TeamName)
			if err != nil || req.Limit <= 0 || req.Limit >= len(sections) {
				return sections, err
			}
			return sections[:req.Limit], nil
		},
	)
}
