package multimodalfusion

import (
	"context"
	"time"
)

// ModalityType describes the form that should enter the fusion layer.
type ModalityType string

const (
	ModalityText      ModalityType = "text"
	ModalityImage     ModalityType = "image"
	ModalityTable     ModalityType = "table"
	ModalityLog       ModalityType = "log"
	ModalityPDF       ModalityType = "pdf"
	ModalityAudio     ModalityType = "audio"
	ModalitySQLResult ModalityType = "sql_result"
)

// ImageProcessingMode 表示图片进入模型前的处理策略。
type ImageProcessingMode string

const (
	ImageProcessingAuto   ImageProcessingMode = "auto"
	ImageProcessingVision ImageProcessingMode = "vision"
	ImageProcessingOCR    ImageProcessingMode = "ocr"
)

// FileInput is one user-provided file or inline content item.
type FileInput struct {
	Path            string              `json:"path,omitempty" jsonschema:"description=Local file path to parse; HTTP/HTTPS image URLs are also accepted for CLI compatibility"`
	URL             string              `json:"url,omitempty" jsonschema:"description=HTTP/HTTPS URL for image content that should be handed to a vision model"`
	Kind            ModalityType        `json:"kind,omitempty" jsonschema:"description=Optional modality override: text, image, table, log, pdf, audio, sql_result"`
	Hint            string              `json:"hint,omitempty" jsonschema:"description=Business hint, for example market size chart or auth service logs"`
	KeepAsImage     bool                `json:"keep_as_image,omitempty" jsonschema:"description=Force preserving this item as an image reference when spatial layout matters"`
	ImageProcessing ImageProcessingMode `json:"image_processing,omitempty" jsonschema:"description=Image handling mode: auto, vision, or ocr"`
	InlineContent   string              `json:"inline_content,omitempty" jsonschema:"description=Inline content for text, logs, CSV, markdown, or JSON"`
	Metadata        map[string]string   `json:"metadata,omitempty" jsonschema:"description=Extra source metadata such as page, figure, table, or unit"`
}

// ReportProfile carries business context for the final report.
type ReportProfile struct {
	ReportType     string `json:"report_type,omitempty" jsonschema:"description=Report type such as industry, company, macro, contract, audit, or operations"`
	Industry       string `json:"industry,omitempty" jsonschema:"description=Industry or domain of the report"`
	TargetAudience string `json:"target_audience,omitempty" jsonschema:"description=Who will read the report, for example sales, broker, manager, engineer"`
	Goal           string `json:"goal,omitempty" jsonschema:"description=Analysis goal or decision to support"`
}

// ReportAnalysisRequest is the ADK tool input contract.
type ReportAnalysisRequest struct {
	Files        []FileInput   `json:"files" jsonschema:"description=Files or inline content to parse and fuse"`
	Report       ReportProfile `json:"report,omitempty" jsonschema:"description=Business context for the report"`
	Task         string        `json:"task,omitempty" jsonschema:"description=User requested analysis task"`
	MaxTextBytes int           `json:"max_text_bytes,omitempty" jsonschema:"description=Optional max bytes to read from text-like files"`
	MaxTableRows int           `json:"max_table_rows,omitempty" jsonschema:"description=Optional max rows to keep from CSV or table-like files"`
	IncludeTrace bool          `json:"include_trace,omitempty" jsonschema:"description=Whether the final answer should include fusion trace details"`
}

// Table is a compact representation for CSV, SQL results, and extracted tables.
type Table struct {
	Header    []string   `json:"header,omitempty"`
	Rows      [][]string `json:"rows,omitempty"`
	TotalRows int        `json:"total_rows,omitempty"`
}

// ModalityInput is the normalized input consumed by the fuser.
type ModalityInput struct {
	Type            ModalityType        `json:"type"`
	Payload         any                 `json:"payload,omitempty"`
	Source          string              `json:"source,omitempty"`
	Hint            string              `json:"hint,omitempty"`
	KeepAsImage     bool                `json:"keep_as_image,omitempty"`
	ImageProcessing ImageProcessingMode `json:"image_processing,omitempty"`
	Metadata        map[string]string   `json:"metadata,omitempty"`
}

// FusionBlock is one compact content block passed to the reasoning layer.
type FusionBlock struct {
	Type     string            `json:"type"`
	Modality ModalityType      `json:"modality"`
	Text     string            `json:"text,omitempty"`
	URI      string            `json:"uri,omitempty"`
	MIMEType string            `json:"mime_type,omitempty"`
	Source   string            `json:"source,omitempty"`
	Hint     string            `json:"hint,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// FusionEvent records how one input was transformed.
type FusionEvent struct {
	Modality     ModalityType `json:"modality"`
	Source       string       `json:"source,omitempty"`
	BytesIn      int          `json:"bytes_in,omitempty"`
	TokensOut    int          `json:"tokens_out"`
	Method       string       `json:"method"`
	ProcessingMS int64        `json:"processing_ms"`
	Timestamp    time.Time    `json:"timestamp"`
	Warning      string       `json:"warning,omitempty"`
}

// FusionResult is the fuser output plus observable trace.
type FusionResult struct {
	Content             []FusionBlock     `json:"content"`
	TotalTokensEstimate int               `json:"total_tokens_estimate"`
	FusionTrace         []FusionEvent     `json:"fusion_trace"`
	Health              map[string]string `json:"health,omitempty"`
}

// PreparedReportContext is the structured context returned by the ADK tool.
type PreparedReportContext struct {
	Report               ReportProfile     `json:"report"`
	Task                 string            `json:"task,omitempty"`
	Content              []FusionBlock     `json:"content"`
	TotalTokensEstimate  int               `json:"total_tokens_estimate"`
	FusionTrace          []FusionEvent     `json:"fusion_trace,omitempty"`
	Health               map[string]string `json:"health,omitempty"`
	OutputContract       []string          `json:"output_contract"`
	FactCheckRequirement []string          `json:"fact_check_requirement"`
}

// PDFPage is one extracted text page from a PDF.
type PDFPage struct {
	Page int    `json:"page"`
	Text string `json:"text"`
}

// PDFSectionSummary keeps the report skeleton compact before the LLM sees it.
type PDFSectionSummary struct {
	Page    int    `json:"page,omitempty"`
	Heading string `json:"heading,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// PDFTable is one table extracted from a PDF page with citation metadata.
type PDFTable struct {
	Page    int    `json:"page,omitempty"`
	Caption string `json:"caption,omitempty"`
	Data    Table  `json:"data"`
}

// PDFImage is one retained page/chart image extracted from a PDF.
type PDFImage struct {
	Page        int    `json:"page,omitempty"`
	Path        string `json:"path,omitempty"`
	Caption     string `json:"caption,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Bytes       int    `json:"bytes,omitempty"`
	KeepAsImage bool   `json:"keep_as_image,omitempty"`
}

// PDFExtract is the compact PDF representation used by the fuser.
type PDFExtract struct {
	PageCount        int                 `json:"page_count,omitempty"`
	TOC              string              `json:"toc,omitempty"`
	SectionSummaries []PDFSectionSummary `json:"section_summaries,omitempty"`
	KeyPages         []PDFPage           `json:"key_pages,omitempty"`
	Tables           []PDFTable          `json:"tables,omitempty"`
	Images           []PDFImage          `json:"images,omitempty"`
	Warnings         []string            `json:"warnings,omitempty"`
}

// CriticalChartSpec lists chart/table hints that should preserve spatial signal.
type CriticalChartSpec struct {
	PatternKeywords []string
}

// ImageTextExtractionRequest 汇总 OCR 服务需要的图片来源和业务提示。
type ImageTextExtractionRequest struct {
	URI      string
	MIMEType string
	Source   string
	Hint     string
	Metadata map[string]string
}

// ImageTextExtractor 抽象真实 OCR 能力，默认链路没有配置时会回退到 vision 图片输入。
type ImageTextExtractor interface {
	ExtractImageText(ctx context.Context, request ImageTextExtractionRequest) (string, error)
}
