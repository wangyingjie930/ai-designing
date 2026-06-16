package multimodalfusion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const ReportContextToolName = "prepare_report_context"

// AgentConfig configures the Eino ADK report analysis agent.
type AgentConfig struct {
	Name           string
	Description    string
	Instruction    string
	Analyzer       *ReportAnalyzer
	AnalyzerConfig AnalyzerConfig
	ExtraTools     []tool.BaseTool
	MaxIterations  int
}

type prepareReportContextTool struct {
	analyzer *ReportAnalyzer
	info     *schema.ToolInfo
}

// NewPrepareReportContextTool exposes the deterministic parsing/fusion pipeline as an ADK tool.
func NewPrepareReportContextTool(analyzer *ReportAnalyzer) (tool.BaseTool, error) {
	if analyzer == nil {
		analyzer = NewReportAnalyzer(AnalyzerConfig{})
	}
	// 复用 InferTool 生成 JSON schema，避免手写工具参数结构后和 ReportAnalysisRequest 漂移。
	inferred, err := toolutils.InferTool[ReportAnalysisRequest, *PreparedReportContext](
		ReportContextToolName,
		"Parse uploaded or local report files, route each modality to compact text/image/table/log/PDF representations, and return content blocks with fusion trace for report analysis.",
		analyzer.PrepareReportContext,
	)
	if err != nil {
		return nil, err
	}
	info, err := inferred.Info(context.Background())
	if err != nil {
		return nil, err
	}
	return &prepareReportContextTool{analyzer: analyzer, info: info}, nil
}

// Info returns the inferred schema used by the chat model when it decides tool calls.
func (t *prepareReportContextTool) Info(context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

// InvokableRun 返回 Eino 原生多模态工具结果，让关键图表直接进入 tool message。
func (t *prepareReportContextTool) InvokableRun(ctx context.Context, argument *schema.ToolArgument, _ ...tool.Option) (*schema.ToolResult, error) {
	request, err := parseReportToolArgument(argument)
	if err != nil {
		return nil, err
	}

	prepared, err := t.analyzer.PrepareReportContext(ctx, request)
	if err != nil {
		return nil, err
	}
	return buildPreparedReportToolResult(prepared)
}

// NewReportAnalysisAgent builds an Eino ADK ChatModelAgent for file-based report analysis.
func NewReportAnalysisAgent(ctx context.Context, chatModel model.BaseChatModel, config AgentConfig) (*adk.ChatModelAgent, error) {
	if chatModel == nil {
		return nil, fmt.Errorf("chat model is required")
	}

	analyzer := config.Analyzer
	if analyzer == nil {
		analyzerConfig := config.AnalyzerConfig
		if analyzerConfig.Fuser == nil && analyzerConfig.FuserConfig.ImageTextExtractor == nil {
			analyzerConfig.FuserConfig.ImageTextExtractor = NewModelImageTextExtractor(chatModel)
		}
		analyzer = NewReportAnalyzer(analyzerConfig)
	}
	prepareTool, err := NewPrepareReportContextTool(analyzer)
	if err != nil {
		return nil, err
	}

	tools := make([]tool.BaseTool, 0, 1+len(config.ExtraTools))
	tools = append(tools, prepareTool)
	tools = append(tools, config.ExtraTools...)

	name := config.Name
	if name == "" {
		name = "file_report_analysis_agent"
	}
	description := config.Description
	if description == "" {
		description = "Analyze reports from mixed files such as PDFs, CSV tables, logs, markdown, text, and image references."
	}
	instruction := config.Instruction
	if instruction == "" {
		instruction = defaultReportAgentInstruction()
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 8
	}

	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          name,
		Description:   description,
		Instruction:   instruction,
		Model:         chatModel,
		MaxIterations: maxIterations,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: tools,
			},
		},
	})
}

// AnalyzeWithAgent runs the ADK agent with a structured report request and returns the final text.
func AnalyzeWithAgent(ctx context.Context, agent adk.Agent, request ReportAnalysisRequest) (string, error) {
	if agent == nil {
		return "", fmt.Errorf("agent is required")
	}
	payload, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return "", err
	}

	query := "请分析下面这个文件解析报告请求。先调用 " + ReportContextToolName + "，再基于工具返回的融合内容输出报告。\n\n" + string(payload)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	iter := runner.Query(ctx, query)

	var final string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return "", event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		if event.Output.MessageOutput.Role != schema.Assistant {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return "", err
		}
		if message != nil && strings.TrimSpace(message.Content) != "" {
			final = message.Content
		}
	}
	if strings.TrimSpace(final) == "" {
		return "", fmt.Errorf("agent finished without assistant output")
	}
	return final, nil
}

// defaultReportAgentInstruction defines the report contract and tool-use policy.
func defaultReportAgentInstruction() string {
	return strings.Join([]string{
		"你是一个文件解析报告分析 Agent。",
		"面对 PDF、表格、日志、文本和图片 URL/引用时，必须先调用 prepare_report_context 工具，不允许直接把 PDF 路径或图片 URL 当正文分析。",
		"PDF 必须按多模态融合逻辑拆分：主体抽取 TOC、章节摘要和关键页；关键图表作为工具多模态图片返回，并在 JSON 中保留 image_ref 元数据；表格转 markdown；logo、页脚、模板图标等装饰图丢弃。",
		"图片 URL 或关键图表会作为工具多模态图片进入最终分析轮；如果图片配置为 image_processing=ocr，则先由 vision 大模型做 OCR，再使用 OCR 文本，不要假装看过原图。",
		"分析时遵循：文本给逻辑，表格给结构，图表给空间关系，trace 给质量审查。",
		"输出必须包含：核心论点摘要、数字事实核查、行动或销售要点、风险与缺口。",
		"数字事实核查必须给出 source/page/chart/table/ref 和 confidence；找不到来源时标为 low，不要编造引用。",
		"涉及精确计算时优先使用结构化表格或文本数字，图像只用于趋势、空间关系、图例和版式判断。",
		"如果工具返回 health warning，需要在风险与缺口里解释可能影响。",
	}, "\n")
}

// parseReportToolArgument 兼容 EnhancedInvokableTool 的参数包装，内部仍复用原始 JSON 契约。
func parseReportToolArgument(argument *schema.ToolArgument) (ReportAnalysisRequest, error) {
	if argument == nil || strings.TrimSpace(argument.Text) == "" {
		return ReportAnalysisRequest{}, nil
	}
	var request ReportAnalysisRequest
	if err := json.Unmarshal([]byte(argument.Text), &request); err != nil {
		return ReportAnalysisRequest{}, err
	}
	return request, nil
}
