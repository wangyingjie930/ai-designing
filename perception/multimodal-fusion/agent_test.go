package multimodalfusion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type fakeChatModel struct{}

func (fakeChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("ok", nil), nil
}

func (fakeChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

func TestNewReportAnalysisAgent(t *testing.T) {
	agent, err := NewReportAnalysisAgent(context.Background(), fakeChatModel{}, AgentConfig{})
	if err != nil {
		t.Fatalf("NewReportAnalysisAgent() error = %v", err)
	}
	if agent == nil {
		t.Fatal("expected agent")
	}
}

// TestAnalyzeWithAgentUsesEnhancedToolResult 验证 Agent 工具回填直接携带多模态图片。
func TestAnalyzeWithAgentUsesEnhancedToolResult(t *testing.T) {
	model := &toolCallingChatModel{}
	agent, err := NewReportAnalysisAgent(context.Background(), model, AgentConfig{})
	if err != nil {
		t.Fatalf("NewReportAnalysisAgent() error = %v", err)
	}

	got, err := AnalyzeWithAgent(context.Background(), agent, ReportAnalysisRequest{
		Files:        []FileInput{{Path: "report.pdf", Kind: ModalityPDF, Hint: "metrics"}},
		IncludeTrace: true,
	})
	if err != nil {
		t.Fatalf("AnalyzeWithAgent() error = %v", err)
	}
	if got != "final report" {
		t.Fatalf("AnalyzeWithAgent() = %q, want final report", got)
	}
	if model.calls != 2 {
		t.Fatalf("Generate calls = %d, want 2", model.calls)
	}
}

// TestPrepareReportContextTool 验证报告上下文工具以 EnhancedTool 返回 JSON 文本和图片 part。
func TestPrepareReportContextTool(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "chart.png")
	if err := os.WriteFile(imagePath, []byte("fake png bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	analyzer := NewReportAnalyzer(AnalyzerConfig{
		FuserConfig: FuserConfig{
			PDFExtractor: StaticPDFExtractor{Extract: PDFExtract{
				KeyPages: []PDFPage{{Page: 1, Text: "pdf text"}},
				Images: []PDFImage{{
					Page:        1,
					Path:        imagePath,
					Caption:     "图 1 市场规模趋势图",
					Kind:        "critical_chart_page",
					KeepAsImage: true,
				}},
			}},
		},
	})
	baseTool, err := NewPrepareReportContextTool(analyzer)
	if err != nil {
		t.Fatalf("NewPrepareReportContextTool() error = %v", err)
	}
	if _, ok := baseTool.(tool.InvokableTool); ok {
		t.Fatalf("tool type = %T, should not use plain InvokableTool", baseTool)
	}
	enhanced, ok := baseTool.(tool.EnhancedInvokableTool)
	if !ok {
		t.Fatalf("tool type = %T, want EnhancedInvokableTool", baseTool)
	}
	result, err := enhanced.InvokableRun(context.Background(), &schema.ToolArgument{Text: `{"files":[{"path":"report.pdf","kind":"pdf","hint":"metrics"}],"include_trace":true}`})
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	textPart := firstToolTextPart(result.Parts)
	if textPart == nil || !strings.Contains(textPart.Text, `"type": "image_ref"`) {
		t.Fatalf("tool result = %+v, want image_ref JSON text block", result)
	}
	if !strings.Contains(textPart.Text, imagePath) {
		t.Fatalf("tool text = %s, want image path", textPart.Text)
	}
	imagePart := firstToolImagePart(result.Parts)
	if imagePart == nil || imagePart.Image == nil || imagePart.Image.Base64Data == nil {
		t.Fatalf("tool result = %+v, want local image as base64 part", result)
	}
	if imagePart.Image.MIMEType != "image/png" {
		t.Fatalf("image mime = %s, want image/png", imagePart.Image.MIMEType)
	}
}

// toolCallingChatModel 模拟先发起工具调用、再消费工具结果的两轮 ChatModel。
type toolCallingChatModel struct {
	calls int
}

// Generate 第一轮返回工具调用，第二轮断言工具结果直接携带多模态内容。
func (m *toolCallingChatModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	switch m.calls {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call_prepare_report_context",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      ReportContextToolName,
				Arguments: `{"files":[{"path":"report.pdf","kind":"pdf","hint":"metrics"}],"include_trace":true}`,
			},
		}}), nil
	case 2:
		toolMsg := firstMessageByRole(input, schema.Tool)
		if toolMsg == nil {
			return nil, errors.New("missing tool message")
		}
		if strings.TrimSpace(toolMsg.Content) != "" {
			return nil, errors.New("enhanced tool message should not use plain content")
		}
		if len(toolMsg.UserInputMultiContent) == 0 {
			return nil, errors.New("tool message missing UserInputMultiContent")
		}
		if !strings.Contains(joinInputTextParts(toolMsg.UserInputMultiContent), `"type": "image_ref"`) {
			return nil, errors.New("tool message content missing image_ref JSON")
		}
		if firstImagePart(toolMsg.UserInputMultiContent) == nil {
			return nil, errors.New("tool message missing image part")
		}
		return schema.AssistantMessage("final report", nil), nil
	default:
		return nil, errors.New("unexpected extra Generate call")
	}
}

// Stream 在该测试模型中不参与执行，用于满足 BaseChatModel 接口。
func (m *toolCallingChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

// captureChatModel 记录底层模型收到的消息，用于验证图片输入是否生效。
type captureChatModel struct {
	inputs [][]*schema.Message
}

// Generate 保存输入并返回稳定响应。
func (m *captureChatModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	copied := append([]*schema.Message{}, input...)
	m.inputs = append(m.inputs, copied)
	return schema.AssistantMessage("ok", nil), nil
}

// Stream 在该测试模型中不参与执行，用于满足 BaseChatModel 接口。
func (m *captureChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not implemented")
}

// TestModelImageTextExtractorUsesVisionModel 验证 OCR 提取器会把图片交给大模型读取。
func TestModelImageTextExtractorUsesVisionModel(t *testing.T) {
	base := &captureChatModel{}
	extractor := NewModelImageTextExtractor(base)

	got, err := extractor.ExtractImageText(context.Background(), ImageTextExtractionRequest{
		URI:    "https://cdn.example.com/reports/chart.png",
		Source: "remote chart",
		Hint:   "OCR 截图",
	})
	if err != nil {
		t.Fatalf("ExtractImageText() error = %v", err)
	}
	if got != "ok" {
		t.Fatalf("OCR text = %q, want ok", got)
	}
	if len(base.inputs) != 1 {
		t.Fatalf("inputs len = %d, want 1", len(base.inputs))
	}
	imageMsg := firstUserMessageWithImage(base.inputs[0])
	if imageMsg == nil {
		t.Fatal("missing image input for model OCR")
	}
	if !strings.Contains(imageMsg.UserInputMultiContent[0].Text, "只返回图片中可见") {
		t.Fatalf("OCR prompt = %s", imageMsg.UserInputMultiContent[0].Text)
	}
}

// TestBuildPreparedReportToolResultAddsRemoteImageURLParts 验证远程 image_ref 会直接成为工具图片 part。
func TestBuildPreparedReportToolResultAddsRemoteImageURLParts(t *testing.T) {
	prepared := PreparedReportContext{
		Content: []FusionBlock{{
			Type:     "image_ref",
			Modality: ModalityImage,
			URI:      "https://cdn.example.com/reports/chart.png?token=abc",
			MIMEType: "image/png",
			Source:   "remote chart",
			Hint:     "市场趋势图",
		}},
	}

	result, err := buildPreparedReportToolResult(&prepared)
	if err != nil {
		t.Fatalf("buildPreparedReportToolResult() error = %v", err)
	}
	imagePart := firstToolImagePart(result.Parts)
	if imagePart == nil || imagePart.Image == nil || imagePart.Image.URL == nil {
		t.Fatalf("image part = %+v, want URL image", imagePart)
	}
	if *imagePart.Image.URL != "https://cdn.example.com/reports/chart.png?token=abc" {
		t.Fatalf("image URL = %s", *imagePart.Image.URL)
	}
}

// firstMessageByRole 从模型输入中找到指定 role 的第一条消息，便于断言 ADK 回填内容。
func firstMessageByRole(messages []*schema.Message, role schema.RoleType) *schema.Message {
	for _, message := range messages {
		if message != nil && message.Role == role {
			return message
		}
	}
	return nil
}

// firstUserMessageWithImage 找到包含图片的 user 多模态消息。
func firstUserMessageWithImage(messages []*schema.Message) *schema.Message {
	for _, message := range messages {
		if message == nil || message.Role != schema.User {
			continue
		}
		if firstImagePart(message.UserInputMultiContent) != nil {
			return message
		}
	}
	return nil
}

// joinInputTextParts 拼接多模态输入里的文本块，方便测试检查工具 JSON。
func joinInputTextParts(parts []schema.MessageInputPart) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type == schema.ChatMessagePartTypeText && strings.TrimSpace(part.Text) != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// firstImagePart 返回第一段图片输入，方便测试断言 URL/base64 是否存在。
func firstImagePart(parts []schema.MessageInputPart) *schema.MessageInputPart {
	for i := range parts {
		if parts[i].Type == schema.ChatMessagePartTypeImageURL && parts[i].Image != nil {
			return &parts[i]
		}
	}
	return nil
}

// firstToolTextPart 返回工具结果里的第一段文本输出。
func firstToolTextPart(parts []schema.ToolOutputPart) *schema.ToolOutputPart {
	for i := range parts {
		if parts[i].Type == schema.ToolPartTypeText {
			return &parts[i]
		}
	}
	return nil
}

// firstToolImagePart 返回工具结果里的第一段图片输出。
func firstToolImagePart(parts []schema.ToolOutputPart) *schema.ToolOutputPart {
	for i := range parts {
		if parts[i].Type == schema.ToolPartTypeImage && parts[i].Image != nil {
			return &parts[i]
		}
	}
	return nil
}
