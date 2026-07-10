# Claude Multimodal Fusion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 `perception/multimodal-fusion/claude_multimodel_fusion/` 建立独立 Go 包，复刻 Claude Code 的文本、图片、PDF Read 工具结果与媒体生命周期，并提供经过最终 HTTP 请求体证明的 OpenAI Chat Completions/Eino 图片桥接。

**Architecture:** 包内先以强类型结构表达 Anthropic message/content block 和 Read 判别联合，再由 `Engine` 编排文本、图片、PDF 三条读取路径。图片处理、外部命令、token 计数和图片缓存均通过小接口隔离；Anthropic wire contract 保持主语义，`openai_bridge.go` 只承担 provider 适配，不把报告 OCR 或模型调用混入读取层。

**Tech Stack:** Go 1.25.8、标准库 `encoding/json`/`image`/`os/exec`、`golang.org/x/image v0.44.0`、`github.com/cloudwego/eino v0.9.6`、`github.com/cloudwego/eino-ext/components/model/openai v0.1.8`、`github.com/cloudwego/eino-ext/libs/acl/openai v0.1.17`。

## Global Constraints

- Anthropic 可观察行为是主契约；OpenAI bridge 是显式适配层，不能反向污染 Anthropic JSON。
- `ReadInput` 只暴露 `file_path`、`offset`、`limit`、`pages`；输出只包含 `text`、`image`、`pdf`、`parts`、`file_unchanged`。
- 不创建 `.ipynb` 解析器，不定义 notebook 输出类型，不加入相关 fixture 或测试。
- 固定阈值：Base64 图片 5 MiB、原始图片目标 3.75 MiB、最大尺寸 2000×2000、PDF document 20 MiB、PDF extraction 100 MiB、自动分页阈值 3 MiB、单次分页 20 页、PDF 整体读取页数阈值 10、每请求媒体 100、文本 256 KiB、文本 25,000 tokens。
- PDF `repaired` 行为必须保留：文件大于 3 MiB或 provider 不支持 document 时，成功 extraction 的结果必须真实返回 `parts` 和 supplemental images。
- 所有新增函数和结构体，包括测试函数与测试辅助函数，前面都写简洁中文用途注释；核心 `Engine.Read`、消息映射和 provider bridge 注释要说明主流程角色与边界。
- Codex 新增的模型可见 prompt/补充说明使用中文；为了兼容 Claude Code 而保留的原始英文错误文案、XML reminder 和 `[image]`/`[document]` 占位符不翻译。
- 不复用同级 `multimodal-fusion` 的 OCR、报告 fuser、图表筛选或 PDF 假 extractor。
- 所有生产代码必须遵循 RED → GREEN → REFACTOR；没有观察到预期失败前不得写对应实现。
- 测试不得访问外部模型或公网；OpenAI 线级测试使用 `httptest.Server` 捕获 Eino 生成的请求。

---

## File Structure

- Create `perception/multimodal-fusion/claude_multimodel_fusion/types.go`: Anthropic 内容块、消息、Read 输入/输出、错误和依赖接口。
- Create `perception/multimodal-fusion/claude_multimodel_fusion/limits.go`: 与 Claude Code 对齐的稳定阈值。
- Create `perception/multimodal-fusion/claude_multimodel_fusion/validation.go`: 页范围、阻塞设备、二进制扩展和 API 图片限制。
- Create `perception/multimodal-fusion/claude_multimodel_fusion/text.go`: 文本范围读取、格式规范化、token 限制和 mtime 状态。
- Create `perception/multimodal-fusion/claude_multimodel_fusion/image.go`: 图片 magic、解码、缩放、压缩和 token budget。
- Create `perception/multimodal-fusion/claude_multimodel_fusion/image_store.go`: 私有图片缓存和最近 200 条索引。
- Create `perception/multimodal-fusion/claude_multimodel_fusion/prompt.go`: 直接文本+图片 user message 组装。
- Create `perception/multimodal-fusion/claude_multimodel_fusion/command.go`: 可替换外部命令执行边界。
- Create `perception/multimodal-fusion/claude_multimodel_fusion/pdf.go`: PDF document 校验、页数探测和 Poppler 分页。
- Create `perception/multimodal-fusion/claude_multimodel_fusion/reader.go`: `Engine` 装配、路由、去重和 repaired PDF 编排。
- Create `perception/multimodal-fusion/claude_multimodel_fusion/tool_result.go`: Read 输出到 tool result/supplemental messages 的精确映射。
- Create `perception/multimodal-fusion/claude_multimodel_fusion/media_lifecycle.go`: API 前媒体淘汰和 compact 占位。
- Create `perception/multimodal-fusion/claude_multimodel_fusion/openai_bridge.go`: Anthropic message 到 Eino OpenAI Chat Completions message 的转换。
- Create `perception/multimodal-fusion/claude_multimodel_fusion/README.md`: Claude 源码、Go 实现、测试与 repaired 差异矩阵。
- Create `perception/multimodal-fusion/claude_multimodel_fusion/test_helpers_test.go`: 跨测试文件复用的小型真实 fixture 构造器。
- Create colocated `*_test.go` files and `testdata/` fixtures for each responsibility.
- Modify `go.mod` and `go.sum`: add direct `golang.org/x/image v0.44.0` dependency.

---

### Task 1: Strongly Typed Anthropic Wire Contracts

**Files:**
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/types.go`
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/limits.go`
- Test: `perception/multimodal-fusion/claude_multimodel_fusion/types_test.go`
- Test helper: `perception/multimodal-fusion/claude_multimodel_fusion/test_helpers_test.go`

**Interfaces:**
- Produces `ContentBlock`, `MediaSource`, `ToolResultContent`, `Message`, `ReadInput`, `ReadOutput`, `ReadResponse`, `EngineConfig`.
- Produces `NewTextOutput`, `NewImageOutput`, `NewPDFOutput`, `NewPartsOutput`, `NewUnchangedOutput` and `ReadOutput.Validate()`.
- Produces `ReadError` with stable `Code`, `Kind`, `Message`, and `Unwrap()`.
- Produces dependency contracts `TokenCounter`, `ImageProcessor`, and `CommandRunner` used by later tasks.

- [ ] **Step 1: Write failing JSON and discriminated-union tests**

```go
// TestMessageMarshalNestedImageToolResult 验证嵌套图片保持 Anthropic wire 层级。
func TestMessageMarshalNestedImageToolResult(t *testing.T) {
    got, err := json.Marshal(Message{
        Role: RoleUser,
        Content: []ContentBlock{{
            Type: BlockToolResult,
            ToolUseID: "tool-1",
            ToolContent: ToolResultBlocks([]ContentBlock{{
                Type: BlockImage,
                Source: &MediaSource{Type: SourceBase64, MediaType: "image/png", Data: "aGVsbG8="},
            }}),
        }},
    })
    if err != nil {
        t.Fatal(err)
    }
    want := `{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`
    if string(got) != want {
        t.Fatalf("wire JSON\n got: %s\nwant: %s", got, want)
    }
}

// TestReadOutputRejectsMismatchedPayload 验证判别类型与 payload 不一致时被拒绝。
func TestReadOutputRejectsMismatchedPayload(t *testing.T) {
    output := ReadOutput{Type: OutputImage, Text: &TextFile{FilePath: "a.txt"}}
    if err := output.Validate(); err == nil {
        t.Fatal("expected mismatched payload error")
    }
}

// TestLimitsMatchClaudeCode 固定服务端和客户端边界，防止常量在移植中漂移。
func TestLimitsMatchClaudeCode(t *testing.T) {
    if APIImageMaxBase64Size != 5*1024*1024 || ImageTargetRawSize != APIImageMaxBase64Size*3/4 {
        t.Fatalf("image limits = %d/%d", APIImageMaxBase64Size, ImageTargetRawSize)
    }
    if ImageMaxWidth != 2000 || ImageMaxHeight != 2000 || PDFTargetRawSize != 20*1024*1024 || PDFMaxExtractSize != 100*1024*1024 {
        t.Fatal("image or PDF limits drifted from Claude Code")
    }
    if PDFMaxPagesPerRead != 20 || PDFAtMentionInlineThreshold != 10 || APIMaxMediaPerRequest != 100 || DefaultMaxOutputTokens != 25_000 || DefaultMaxOutputSize != 256*1024 {
        t.Fatal("read or media limits drifted from Claude Code")
    }
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestMessageMarshalNestedImageToolResult|TestReadOutputRejectsMismatchedPayload' -count=1`

Expected: FAIL because the package and contract types do not exist.

- [ ] **Step 3: Implement exact contracts and constants**

Use these signatures and enum values:

```go
type BlockType string

const (
    BlockText       BlockType = "text"
    BlockImage      BlockType = "image"
    BlockDocument   BlockType = "document"
    BlockToolResult BlockType = "tool_result"
)

const (
    RoleUser      = "user"
    RoleAssistant = "assistant"
    RoleSystem    = "system"
    SourceBase64  = "base64"
    SourceURL     = "url"
)

type OutputType string

const (
    OutputText      OutputType = "text"
    OutputImage     OutputType = "image"
    OutputPDF       OutputType = "pdf"
    OutputParts     OutputType = "parts"
    OutputUnchanged OutputType = "file_unchanged"
)

type MediaSource struct {
    Type      string `json:"type"`
    MediaType string `json:"media_type,omitempty"`
    Data      string `json:"data,omitempty"`
    URL       string `json:"url,omitempty"`
}

type ToolResultContent struct {
    Text   *string
    Blocks []ContentBlock
}

type ContentBlock struct {
    Type        BlockType         `json:"type"`
    Text        string            `json:"text,omitempty"`
    Source      *MediaSource      `json:"source,omitempty"`
    ToolUseID   string            `json:"tool_use_id,omitempty"`
    ToolContent ToolResultContent `json:"content,omitempty"`
}

type Message struct {
    Role          string         `json:"role"`
    Content       []ContentBlock `json:"content"`
    IsMeta        bool           `json:"-"`
    ID            string         `json:"-"`
    PastedImageID string         `json:"-"`
}

type ReadInput struct {
    FilePath string `json:"file_path"`
    Offset   int    `json:"offset,omitempty"`
    Limit    *int   `json:"limit,omitempty"`
    Pages    string `json:"pages,omitempty"`
}

type EngineConfig struct {
    PDFDocumentSupported bool
    MaxTextBytes          int
    MaxTokens             int
    TokenCounter          TokenCounter
    CommandRunner         CommandRunner
    ImageProcessor        ImageProcessor
    ImageStoreDir         string
    ToolResultsDir        string
    SessionID             string
}

type TextFile struct {
    FilePath  string `json:"filePath"`
    Content   string `json:"content"`
    NumLines  int    `json:"numLines"`
    StartLine int    `json:"startLine"`
    TotalLines int   `json:"totalLines"`
}

type ImageDimensions struct {
    OriginalWidth  int `json:"originalWidth,omitempty"`
    OriginalHeight int `json:"originalHeight,omitempty"`
    DisplayWidth   int `json:"displayWidth,omitempty"`
    DisplayHeight  int `json:"displayHeight,omitempty"`
}

type ImageFile struct {
    Base64       string           `json:"base64"`
    MIMEType     string           `json:"type"`
    OriginalSize int              `json:"originalSize"`
    Dimensions   *ImageDimensions `json:"dimensions,omitempty"`
}

type PDFFile struct {
    FilePath     string `json:"filePath"`
    Base64       string `json:"base64"`
    OriginalSize int    `json:"originalSize"`
}

type PDFParts struct {
    FilePath     string      `json:"filePath"`
    OriginalSize int         `json:"originalSize"`
    Count        int         `json:"count"`
    OutputDir    string      `json:"outputDir"`
    PageImages   []ImageFile `json:"-"`
}

type UnchangedFile struct {
    FilePath string `json:"filePath"`
}

type ReadOutput struct {
    Type      OutputType
    Text      *TextFile
    Image     *ImageFile
    PDF       *PDFFile
    Parts     *PDFParts
    Unchanged *UnchangedFile
}

type ReadResponse struct {
    Output      ReadOutput
    NewMessages []Message
}

type ImageProcessOptions struct {
    DeclaredFormat string
    MaxTokens      int
}

type ProcessedImage struct {
    Data       []byte
    MediaType  string
    Dimensions *ImageDimensions
}

type CommandResult struct {
    ExitCode int
    Stdout   string
    Stderr   string
}

type TokenCounter interface {
    CountTokens(ctx context.Context, content string) (int, error)
}

type ImageProcessor interface {
    Process(ctx context.Context, source []byte, options ImageProcessOptions) (ProcessedImage, error)
}

type CommandRunner interface {
    Run(ctx context.Context, name string, args []string, timeout time.Duration) (CommandResult, error)
}

// ToolResultText 构造字符串形态的 tool result content。
func ToolResultText(value string) ToolResultContent

// ToolResultBlocks 构造 block 数组形态的 tool result content。
func ToolResultBlocks(blocks []ContentBlock) ToolResultContent
```

`ToolResultContent.MarshalJSON` must emit either a JSON string or a block array and reject the state where both are populated. `ContentBlock.MarshalJSON` must omit `content` for non-tool blocks and require it for tool results. Define all fixed limits in `limits.go` with integer byte values, including `ImageTargetRawSize = APIImageMaxBase64Size * 3 / 4`.

Add these shared test helpers so later snippets do not rely on hidden fixtures:

```go
// ptr 为测试构造可选标量，避免每个用例重复声明临时变量。
func ptr[T any](value T) *T { return &value }

// writeTextFixture 把给定文本写入临时文件并返回路径。
func writeTextFixture(t *testing.T, content string) string {
    t.Helper()
    path := filepath.Join(t.TempDir(), "fixture.txt")
    if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
        t.Fatal(err)
    }
    return path
}

// tinyPNGBytes 生成可被真实解码器读取的 2×2 PNG。
func tinyPNGBytes() []byte {
    img := image.NewRGBA(image.Rect(0, 0, 2, 2))
    img.Set(0, 0, color.RGBA{R: 255, A: 255})
    var buf bytes.Buffer
    if err := png.Encode(&buf, img); err != nil {
        panic(err)
    }
    return buf.Bytes()
}

// tinyJPEGBytes 生成可被真实解码器读取的 2×2 JPEG。
func tinyJPEGBytes() []byte {
    img := image.NewRGBA(image.Rect(0, 0, 2, 2))
    img.Set(0, 0, color.RGBA{G: 255, A: 255})
    var buf bytes.Buffer
    if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
        panic(err)
    }
    return buf.Bytes()
}
```

- [ ] **Step 4: Run focused and package tests and verify GREEN**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestMessage|TestToolResult|TestReadOutput|TestLimits' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the contract slice**

```bash
git add perception/multimodal-fusion/claude_multimodel_fusion/types.go perception/multimodal-fusion/claude_multimodel_fusion/limits.go perception/multimodal-fusion/claude_multimodel_fusion/types_test.go perception/multimodal-fusion/claude_multimodel_fusion/test_helpers_test.go
git commit -m "feat: add claude multimodal wire contracts"
```

---

### Task 2: Input Validation And Text Read Semantics

**Files:**
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/validation.go`
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/text.go`
- Test: `perception/multimodal-fusion/claude_multimodel_fusion/validation_test.go`
- Test: `perception/multimodal-fusion/claude_multimodel_fusion/text_test.go`
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/testdata/text/bom-crlf.txt`

**Interfaces:**
- Consumes Task 1 `ReadInput`, `ReadError`, `TextFile`, and `TokenCounter`.
- Produces `ParsePDFPageRange(raw string) (PageRange, error)` and `ValidateReadInput(input ReadInput) error`.
- Produces internal `readText(ctx, path, offset, limit, maxBytes, maxTokens, counter) (TextFile, fileStamp, error)`.
- Produces `readStateKey` and `fileStamp` used by `Engine` in Task 6.

- [ ] **Step 1: Write failing validation tests**

```go
// TestParsePDFPageRangeRejectsMoreThanTwentyPages 验证单次 PDF 范围上限。
func TestParsePDFPageRangeRejectsMoreThanTwentyPages(t *testing.T) {
    _, err := ParsePDFPageRange("1-21")
    var readErr *ReadError
    if !errors.As(err, &readErr) || readErr.Code != 8 {
        t.Fatalf("error = %#v, want code 8", err)
    }
}

// TestValidateReadInputBlocksInfiniteDevice 验证会阻塞的设备路径在 I/O 前被拒绝。
func TestValidateReadInputBlocksInfiniteDevice(t *testing.T) {
    err := ValidateReadInput(ReadInput{FilePath: "/dev/zero"})
    var readErr *ReadError
    if !errors.As(err, &readErr) || readErr.Code != 9 {
        t.Fatalf("error = %#v, want code 9", err)
    }
}
```

Cover `N`, `N-M`, `N-`, zero/negative/reversed ranges, non-PDF `pages`, blocked `/dev/*` and `/proc/*/fd/[0-2]`, and unsupported binary extensions with code 4. Preserve the Claude English messages for codes 7, 8, 9, and 4.

- [ ] **Step 2: Run validation tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestParsePDFPageRange|TestValidateReadInput' -count=1`

Expected: FAIL because validation functions are undefined.

- [ ] **Step 3: Implement validation and verify GREEN**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestParsePDFPageRange|TestValidateReadInput' -count=1`

Expected: PASS.

- [ ] **Step 4: Write failing text-read tests**

```go
// TestReadTextNormalizesBOMCRLFAndUsesOneBasedOffset 验证文本规范化与行号语义。
func TestReadTextNormalizesBOMCRLFAndUsesOneBasedOffset(t *testing.T) {
    got, _, err := readText(context.Background(), "testdata/text/bom-crlf.txt", 2, ptr(2), DefaultMaxOutputSize, DefaultMaxOutputTokens, nil)
    if err != nil {
        t.Fatal(err)
    }
    if got.Content != "second\nthird" || got.StartLine != 2 || got.NumLines != 2 || got.TotalLines != 3 {
        t.Fatalf("text file = %#v", got)
    }
}

// TestReadTextRejectsWholeFileAboveByteLimit 验证未指定范围时先检查整文件大小。
func TestReadTextRejectsWholeFileAboveByteLimit(t *testing.T) {
    path := writeTextFixture(t, strings.Repeat("x", 33))
    _, _, err := readText(context.Background(), path, 1, nil, 32, DefaultMaxOutputTokens, nil)
    if err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed size") {
        t.Fatalf("error = %v", err)
    }
}
```

Also cover explicit `limit` bypassing whole-file byte precheck, empty file, offset past EOF, context cancellation, rough token short-circuit, exact counter overflow, and exact counter failure falling back to the conservative estimate.

- [ ] **Step 5: Run text tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestReadText' -count=1`

Expected: FAIL because `readText` is undefined.

- [ ] **Step 6: Implement text reader and verify GREEN**

The internal token contract is:

```go
type TokenCounter interface {
    CountTokens(ctx context.Context, content string) (int, error)
}
```

Define `readStateEntry` next to `fileStamp`; it stores normalized path, exact one-based offset, optional limit, and the observed mtime. It never stores image or PDF state.

Use the same guard direction as Claude Code: only call the exact counter when the rough estimate exceeds one quarter of `maxTokens`; reject when exact count, or the estimate after counter failure, exceeds `maxTokens`.

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestReadText' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit validation and text semantics**

```bash
git add perception/multimodal-fusion/claude_multimodel_fusion/validation.go perception/multimodal-fusion/claude_multimodel_fusion/validation_test.go perception/multimodal-fusion/claude_multimodel_fusion/text.go perception/multimodal-fusion/claude_multimodel_fusion/text_test.go perception/multimodal-fusion/claude_multimodel_fusion/testdata/text/bom-crlf.txt
git commit -m "feat: add claude text read semantics"
```

---

### Task 3: Image Detection, Resize, Compression, And Token Budget

**Files:**
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/image.go`
- Test: `perception/multimodal-fusion/claude_multimodel_fusion/image_test.go`
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/testdata/images/small.png`
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/testdata/images/wide.png`
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/testdata/images/sample.gif`
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/testdata/images/sample.webp`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes Task 1 image limits and `ImageFile`/`ImageDimensions`.
- Produces `DefaultImageProcessor` implementing `ImageProcessor.Process(ctx, source, options)`.
- Produces internal `detectImageMediaType`, `readImageWithTokenBudget`, `resizeWithin`, and encoding helpers.

- [ ] **Step 1: Add the pinned image dependency**

Run: `go get golang.org/x/image@v0.44.0`

Expected: `go.mod` contains direct `golang.org/x/image v0.44.0` and `go.sum` gains its checksums.

- [ ] **Step 2: Write failing magic and passthrough tests**

```go
// TestDetectImageMediaTypeUsesMagicBytes 验证图片类型不依赖扩展名。
func TestDetectImageMediaTypeUsesMagicBytes(t *testing.T) {
    cases := map[string]string{
        "testdata/images/small.png":  "image/png",
        "testdata/images/sample.gif": "image/gif",
        "testdata/images/sample.webp": "image/webp",
    }
    for path, want := range cases {
        data, err := os.ReadFile(path)
        if err != nil {
            t.Fatal(err)
        }
        if got := detectImageMediaType(data); got != want {
            t.Fatalf("%s media = %q, want %q", path, got, want)
        }
    }
}

// TestDefaultImageProcessorPreservesSmallImageBytes 验证合规小图不被重编码。
func TestDefaultImageProcessorPreservesSmallImageBytes(t *testing.T) {
    source, _ := os.ReadFile("testdata/images/small.png")
    got, err := (DefaultImageProcessor{}).Process(context.Background(), source, ImageProcessOptions{DeclaredFormat: "png", MaxTokens: DefaultMaxOutputTokens})
    if err != nil {
        t.Fatal(err)
    }
    if !bytes.Equal(got.Data, source) || got.MediaType != "image/png" {
        t.Fatalf("small image was unexpectedly rewritten")
    }
}
```

- [ ] **Step 3: Run image tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestDetectImageMediaType|TestDefaultImageProcessorPreservesSmallImageBytes' -count=1`

Expected: FAIL because image processing functions are undefined.

- [ ] **Step 4: Implement magic detection and passthrough, then verify GREEN**

Decode PNG/JPEG/GIF with standard packages, WebP with `golang.org/x/image/webp`, and scale with `golang.org/x/image/draw.CatmullRom`. Unknown magic returns `image/png` before decode, matching Claude Code's fallback direction; corrupt bytes fail during decode.

Run the command from Step 3 again.

Expected: PASS.

- [ ] **Step 5: Write failing resize/compression-order tests**

Use a pure internal attempt builder to prove the exact order without manufacturing multi-megabyte fixtures:

```go
// TestImageCompressionAttemptsPNGThenJPEGQualities 验证压缩尝试顺序与 Claude Code 一致。
func TestImageCompressionAttemptsPNGThenJPEGQualities(t *testing.T) {
    got := buildCompressionAttempts("png")
    want := []imageAttempt{
        {Format: "png", PNGCompression: 9, Palette: true},
        {Format: "jpeg", JPEGQuality: 80},
        {Format: "jpeg", JPEGQuality: 60},
        {Format: "jpeg", JPEGQuality: 40},
        {Format: "jpeg", JPEGQuality: 20},
    }
    if !reflect.DeepEqual(got, want) {
        t.Fatalf("attempts = %#v, want %#v", got, want)
    }
}
```

The pure internal types are exact and stay unexported:

```go
type imageAttempt struct {
    Format         string
    PNGCompression int
    Palette        bool
    JPEGQuality    int
}

// buildCompressionAttempts 返回 Claude Code 对当前源格式的编码尝试顺序。
func buildCompressionAttempts(sourceFormat string) []imageAttempt
```

Add tests for 2000×2000 aspect-ratio bounding, JPEG qualities `80,60,40,20`, 1000px/q20 fallback, token scale factors `1.0,0.75,0.5,0.25`, 400×400/q20 final attempt, Base64 token estimate `ceil(len(base64)*0.125)`, and empty image rejection.

- [ ] **Step 6: Run resize tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestImageCompression|TestImageResize|TestImageToken|TestReadImage' -count=1`

Expected: FAIL on missing resize/compression behavior.

- [ ] **Step 7: Implement the exact attempt order and verify GREEN**

`ImageProcessor` contract:

```go
type ImageProcessor interface {
    Process(ctx context.Context, source []byte, options ImageProcessOptions) (ProcessedImage, error)
}
```

Always restart each encoding attempt from the original decoded image. Populate `ImageDimensions` with original and displayed dimensions. If final token compression fails, `readImageWithTokenBudget` returns the standard processed image as Claude Code does, leaving the API boundary to reject a remaining oversized Base64 payload.

Run the command from Step 6 again, followed by:

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -count=1`

Expected: PASS.

- [ ] **Step 8: Commit image processing**

```bash
git add go.mod go.sum perception/multimodal-fusion/claude_multimodel_fusion/image.go perception/multimodal-fusion/claude_multimodel_fusion/image_test.go perception/multimodal-fusion/claude_multimodel_fusion/testdata/images
git commit -m "feat: add claude image processing pipeline"
```

---

### Task 4: Private Pasted-Image Store And Direct Prompt Assembly

**Files:**
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/image_store.go`
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/prompt.go`
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/reader.go` with the `Engine` shell used by prompt assembly.
- Test: `perception/multimodal-fusion/claude_multimodel_fusion/image_store_test.go`
- Test: `perception/multimodal-fusion/claude_multimodel_fusion/prompt_test.go`

**Interfaces:**
- Consumes Task 3 `ImageProcessor` and `ProcessedImage`.
- Produces `PastedImage`, `ImageStore`, `NewImageStore(root, sessionID)`, `ImageStore.Save`, and `ImageStore.Path`.
- Produces `(*Engine).BuildUserPrompt(ctx, text, images) ([]Message, error)` once `Engine` is introduced as a minimal shell in this task.

Use this public input and cache limit:

```go
const ImageStoreMaxEntries = 200

type PastedImage struct {
    ID         string
    Base64     string
    SourcePath string
    URL        string
}
```

- [ ] **Step 1: Write failing private-cache tests**

```go
// TestImageStoreWritesPrivateFileAndEvictsOnlyIndex 验证缓存权限和索引淘汰边界。
func TestImageStoreWritesPrivateFileAndEvictsOnlyIndex(t *testing.T) {
    store := NewImageStore(t.TempDir(), "session-a")
    var firstPath string
    for i := 0; i < ImageStoreMaxEntries+1; i++ {
        path, err := store.Save(context.Background(), fmt.Sprintf("img-%03d", i), tinyPNGBytes())
        if err != nil {
            t.Fatal(err)
        }
        if i == 0 {
            firstPath = path
        }
    }
    info, err := os.Stat(firstPath)
    if err != nil {
        t.Fatal(err)
    }
    if info.Mode().Perm() != 0o600 {
        t.Fatalf("mode = %o, want 600", info.Mode().Perm())
    }
    if _, ok := store.Path("img-000"); ok {
        t.Fatal("oldest id remained indexed")
    }
    if _, err := os.Stat(firstPath); err != nil {
        t.Fatalf("eviction must not delete persisted file: %v", err)
    }
}
```

- [ ] **Step 2: Run cache tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestImageStore' -count=1`

Expected: FAIL because image store is undefined.

- [ ] **Step 3: Implement the cache and verify GREEN**

Use `<root>/<sessionID>/<id>.<ext>`, directory mode `0700`, file mode `0600`, a mutex, and FIFO insertion order capped at 200 IDs. Validate/sanitize session and image IDs so neither can escape the root.

Run the command from Step 2 again.

Expected: PASS.

- [ ] **Step 4: Write failing direct-prompt tests**

```go
// TestBuildUserPromptOrdersTextBeforeImagesAndAddsMetadata 验证直接图片输入的消息顺序。
func TestBuildUserPromptOrdersTextBeforeImagesAndAddsMetadata(t *testing.T) {
    engine := NewEngine(EngineConfig{
        ImageStoreDir: t.TempDir(),
        SessionID: "test-session",
        ImageProcessor: DefaultImageProcessor{},
    })
    messages, err := engine.BuildUserPrompt(context.Background(), "分析图片", []PastedImage{{ID: "shot-1", Base64: base64.StdEncoding.EncodeToString(tinyPNGBytes())}})
    if err != nil {
        t.Fatal(err)
    }
    if len(messages) != 2 || messages[0].Content[0].Type != BlockText || messages[0].Content[1].Type != BlockImage {
        t.Fatalf("messages = %#v", messages)
    }
    if !messages[1].IsMeta || !strings.Contains(messages[1].Content[0].Text, "[Image: source:") {
        t.Fatalf("metadata message = %#v", messages[1])
    }
}
```

Also cover invalid Base64, extension derived from magic bytes, explicit source path, empty text with images, and no images.

- [ ] **Step 5: Run prompt tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestBuildUserPrompt' -count=1`

Expected: FAIL because prompt assembly is undefined.

- [ ] **Step 6: Implement prompt assembly and verify GREEN**

The first message contains text first and images after it. Metadata is a separate `IsMeta=true` user message. Build metadata from original/display dimensions and stable source path; do not invoke OCR or a model.

Create the complete `Engine` struct now so later tasks only add behavior, not a second definition:

```go
type Engine struct {
    config         EngineConfig
    commandRunner  CommandRunner
    imageProcessor ImageProcessor
    imageStore     *ImageStore
    readStateMu    sync.Mutex
    readState      map[string]readStateEntry
}
```

`NewEngine` sets text/image/store defaults available at this stage. Task 6 adds the real command-runner default after `ExecCommandRunner` exists.

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestImageStore|TestBuildUserPrompt' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit image store and prompt assembly**

```bash
git add perception/multimodal-fusion/claude_multimodel_fusion/image_store.go perception/multimodal-fusion/claude_multimodel_fusion/image_store_test.go perception/multimodal-fusion/claude_multimodel_fusion/prompt.go perception/multimodal-fusion/claude_multimodel_fusion/prompt_test.go perception/multimodal-fusion/claude_multimodel_fusion/reader.go
git commit -m "feat: add claude direct image prompts"
```

---

### Task 5: Command Boundary And PDF Processing

**Files:**
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/command.go`
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/pdf.go`
- Test: `perception/multimodal-fusion/claude_multimodel_fusion/command_test.go`
- Test: `perception/multimodal-fusion/claude_multimodel_fusion/pdf_test.go`
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/testdata/pdf/small.pdf`
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/testdata/pdf/not-a-pdf.pdf`

**Interfaces:**
- Consumes Task 1 PDF limits and `PDFFile`, `PDFParts`; Task 2 `PageRange`; Task 3 `ImageProcessor`.
- Produces `ExecCommandRunner.Run(ctx, name, args, timeout) (CommandResult, error)`.
- Produces internal `readPDFDocument`, `getPDFPageCount`, `isPDFRendererAvailable`, and `extractPDFPages`.

- [ ] **Step 1: Write failing command-boundary tests**

```go
// TestExecCommandRunnerHonorsContextAndTimeout 验证外部命令响应取消与超时。
func TestExecCommandRunnerHonorsContextAndTimeout(t *testing.T) {
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    _, err := (ExecCommandRunner{}).Run(ctx, "sh", []string{"-c", "sleep 5"}, time.Second)
    if !errors.Is(err, context.Canceled) {
        t.Fatalf("error = %v, want context canceled", err)
    }
}
```

- [ ] **Step 2: Run command tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestExecCommandRunner' -count=1`

Expected: FAIL because command runner is undefined.

- [ ] **Step 3: Implement command runner and verify GREEN**

Use `exec.CommandContext`; distinguish caller cancellation from the local timeout, capture stdout/stderr separately, and never construct a shell command string for PDF paths.

Run the command from Step 2 again.

Expected: PASS.

- [ ] **Step 4: Write failing PDF validation and command-shape tests**

```go
// TestReadPDFDocumentRejectsMissingMagic 验证伪 PDF 不进入上下文。
func TestReadPDFDocumentRejectsMissingMagic(t *testing.T) {
    _, err := readPDFDocument(context.Background(), "testdata/pdf/not-a-pdf.pdf")
    var pdfErr *PDFError
    if !errors.As(err, &pdfErr) || pdfErr.Reason != PDFErrorCorrupted {
        t.Fatalf("error = %#v", err)
    }
}

// TestExtractPDFPagesUsesClaudeArgumentsAndTimeout 验证 Poppler 参数和超时。
func TestExtractPDFPagesUsesClaudeArgumentsAndTimeout(t *testing.T) {
    runner := successfulPDFRunner(t, 3)
    _, err := extractPDFPages(context.Background(), runner, DefaultImageProcessor{}, "testdata/pdf/small.pdf", t.TempDir(), &PageRange{FirstPage: 2, LastPage: 4})
    if err != nil {
        t.Fatal(err)
    }
    call := runner.find("pdftoppm")
    want := []string{"-jpeg", "-r", "100", "-f", "2", "-l", "4"}
    if !slices.Equal(call.Args[:len(want)], want) || call.Timeout != 120*time.Second {
        t.Fatalf("call = %#v", call)
    }
}
```

Define the reusable fake command boundary in `pdf_test.go`:

```go
type commandCall struct {
    Name    string
    Args    []string
    Timeout time.Duration
}

type recordingCommandRunner struct {
    t         *testing.T
    pageCount int
    calls     []commandCall
}

// successfulPDFRunner 构造会产出指定页数 JPEG 的可观测命令执行器。
func successfulPDFRunner(t *testing.T, pageCount int) *recordingCommandRunner {
    return &recordingCommandRunner{t: t, pageCount: pageCount}
}

// Run 记录命令，并为 pdfinfo/pdftoppm 生成确定性输出。
func (r *recordingCommandRunner) Run(_ context.Context, name string, args []string, timeout time.Duration) (CommandResult, error) {
    r.calls = append(r.calls, commandCall{Name: name, Args: append([]string(nil), args...), Timeout: timeout})
    switch name {
    case "pdfinfo":
        return CommandResult{ExitCode: 0, Stdout: fmt.Sprintf("Pages:          %d\n", r.pageCount)}, nil
    case "pdftoppm":
        if slices.Equal(args, []string{"-v"}) {
            return CommandResult{ExitCode: 0, Stderr: "pdftoppm version test"}, nil
        }
        prefix := args[len(args)-1]
        for i := 1; i <= r.pageCount; i++ {
            path := fmt.Sprintf("%s-%02d.jpg", prefix, i)
            if err := os.WriteFile(path, tinyJPEGBytes(), 0o600); err != nil {
                r.t.Fatal(err)
            }
        }
        return CommandResult{ExitCode: 0}, nil
    default:
        return CommandResult{ExitCode: 127, Stderr: "unexpected command"}, nil
    }
}

// find 返回指定命令的首次调用记录。
func (r *recordingCommandRunner) find(name string) commandCall {
    for _, call := range r.calls {
        if call.Name == name && !(name == "pdftoppm" && slices.Equal(call.Args, []string{"-v"})) {
            return call
        }
    }
    r.t.Fatalf("command %q was not called", name)
    return commandCall{}
}
```

Cover empty PDF, document >20 MiB, extraction >100 MiB, `pdfinfo` 10s parsing, `pdftoppm -v` 5s availability, password/corrupt/unknown stderr classification, zero rendered pages, natural page order, and page JPEG resize.

- [ ] **Step 5: Run PDF tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestReadPDF|TestGetPDF|TestExtractPDF|TestPDF' -count=1`

Expected: FAIL because PDF behavior is missing.

- [ ] **Step 6: Implement PDF document and extraction paths and verify GREEN**

Use this classified error surface:

```go
type PDFErrorReason string

const (
    PDFErrorEmpty             PDFErrorReason = "empty"
    PDFErrorTooLarge          PDFErrorReason = "too_large"
    PDFErrorPasswordProtected PDFErrorReason = "password_protected"
    PDFErrorCorrupted         PDFErrorReason = "corrupted"
    PDFErrorUnknown           PDFErrorReason = "unknown"
    PDFErrorUnavailable       PDFErrorReason = "unavailable"
)
```

The availability result may be cached inside an `pdfRenderer` instance owned by one `Engine`; tests create fresh instances instead of exposing a production reset method.

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestExecCommandRunner|TestReadPDF|TestGetPDF|TestExtractPDF|TestPDF' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit PDF processing**

```bash
git add perception/multimodal-fusion/claude_multimodel_fusion/command.go perception/multimodal-fusion/claude_multimodel_fusion/command_test.go perception/multimodal-fusion/claude_multimodel_fusion/pdf.go perception/multimodal-fusion/claude_multimodel_fusion/pdf_test.go perception/multimodal-fusion/claude_multimodel_fusion/testdata/pdf
git commit -m "feat: add claude pdf processing"
```

---

### Task 6: Engine Routing, Read Deduplication, And Tool-Result Mapping

**Files:**
- Modify: `perception/multimodal-fusion/claude_multimodel_fusion/reader.go`
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/tool_result.go`
- Test: `perception/multimodal-fusion/claude_multimodel_fusion/reader_test.go`
- Test: `perception/multimodal-fusion/claude_multimodel_fusion/tool_result_test.go`
- Test: `perception/multimodal-fusion/claude_multimodel_fusion/integration_test.go`

**Interfaces:**
- Consumes all Tasks 1-5 contracts.
- Produces `NewEngine(config EngineConfig) *Engine`.
- Produces `(*Engine).Read(ctx, input) (ReadResponse, error)`.
- Produces `MapReadResult(toolUseID, response) ([]Message, error)`.

- [ ] **Step 1: Write failing routing and text-dedupe tests**

```go
// TestEngineReadReturnsFileUnchangedOnlyForSameTextRangeAndMTime 验证文本精确范围去重。
func TestEngineReadReturnsFileUnchangedOnlyForSameTextRangeAndMTime(t *testing.T) {
    path := writeTextFixture(t, "one\ntwo\n")
    engine := NewEngine(EngineConfig{})
    first, err := engine.Read(context.Background(), ReadInput{FilePath: path, Offset: 1, Limit: ptr(1)})
    if err != nil || first.Output.Type != OutputText {
        t.Fatalf("first = %#v, err = %v", first, err)
    }
    second, err := engine.Read(context.Background(), ReadInput{FilePath: path, Offset: 1, Limit: ptr(1)})
    if err != nil || second.Output.Type != OutputUnchanged {
        t.Fatalf("second = %#v, err = %v", second, err)
    }
    differentRange, err := engine.Read(context.Background(), ReadInput{FilePath: path, Offset: 2, Limit: ptr(1)})
    if err != nil || differentRange.Output.Type != OutputText {
        t.Fatalf("different range = %#v, err = %v", differentRange, err)
    }
}
```

Add tests that image/PDF reads never dedupe, relative and `~` paths normalize, extension routes PNG/JPG/JPEG/GIF/WebP/PDF, unsupported binary fails before I/O, and text state is protected for concurrent reads.

Before implementing the routes, add this cross-component test to `integration_test.go` so the complete local Read path also starts RED:

```go
// TestIntegrationReadTextImageAndSmallPDF 验证三条本地读取路径共享同一 Engine 仍保持判别输出正确。
func TestIntegrationReadTextImageAndSmallPDF(t *testing.T) {
    engine := NewEngine(EngineConfig{PDFDocumentSupported: true, ToolResultsDir: t.TempDir()})
    cases := []struct {
        input ReadInput
        want  OutputType
    }{
        {ReadInput{FilePath: "testdata/text/bom-crlf.txt"}, OutputText},
        {ReadInput{FilePath: "testdata/images/small.png"}, OutputImage},
        {ReadInput{FilePath: "testdata/pdf/small.pdf"}, OutputPDF},
    }
    for _, tc := range cases {
        got, err := engine.Read(context.Background(), tc.input)
        if err != nil {
            t.Fatalf("Read(%s): %v", tc.input.FilePath, err)
        }
        if got.Output.Type != tc.want {
            t.Fatalf("Read(%s) type = %s, want %s", tc.input.FilePath, got.Output.Type, tc.want)
        }
    }
}
```

Also add an optional real Poppler integration test before implementation. It uses `exec.LookPath("pdfinfo")` and `exec.LookPath("pdftoppm")`; call `t.Skip` with the missing binary name when unavailable. Fake-runner tests remain mandatory and prove the unavailable path on every environment.

- [ ] **Step 2: Run reader tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestEngineRead|TestIntegration' -count=1`

Expected: FAIL because `Engine.Read` is undefined.

- [ ] **Step 3: Implement Engine assembly and routes, then verify basic GREEN**

`NewEngine` supplies real defaults for nil dependencies and uses:

```go
type Engine struct {
    config         EngineConfig
    commandRunner  CommandRunner
    imageProcessor ImageProcessor
    imageStore     *ImageStore
    readStateMu    sync.Mutex
    readState      map[string]readStateEntry
}
```

Run the command from Step 2 again.

Expected: PASS for text/image/document routing tests that do not exercise repaired PDF.

- [ ] **Step 4: Write failing repaired-PDF orchestration tests**

```go
// TestEngineReadRepairsDiscardedAutomaticPDFExtraction 验证自动分页结果不会被丢弃。
func TestEngineReadRepairsDiscardedAutomaticPDFExtraction(t *testing.T) {
    runner := successfulPDFRunner(t, 2)
    engine := NewEngine(EngineConfig{
        PDFDocumentSupported: false,
        CommandRunner: runner,
        ToolResultsDir: t.TempDir(),
    })
    got, err := engine.Read(context.Background(), ReadInput{FilePath: "testdata/pdf/small.pdf"})
    if err != nil {
        t.Fatal(err)
    }
    if got.Output.Type != OutputParts || len(got.NewMessages) != 1 || len(got.NewMessages[0].Content) != 2 {
        t.Fatalf("response = %#v", got)
    }
}
```

Add cases for explicit page range, >10 pages requiring `pages`, >3 MiB auto extraction, supported small PDF document, and failed extraction when document is unsupported.

- [ ] **Step 5: Run repaired-PDF tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestEngineRead.*PDF' -count=1`

Expected: FAIL because repaired orchestration is incomplete.

- [ ] **Step 6: Implement repaired PDF route and verify GREEN**

When extraction is selected and succeeds, immediately return `OutputParts` plus the generated image messages. Do not continue into `readPDFDocument`. If extraction fails and `PDFDocumentSupported=false`, return the classified extraction error; if extraction was selected only by size, return that extraction error rather than silently sending an oversized document.

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestEngineRead' -count=1`

Expected: PASS.

- [ ] **Step 7: Write failing exact message-mapping tests**

```go
// TestMapReadResultNestsImageInToolResult 验证图片位于 tool_result.content 内。
func TestMapReadResultNestsImageInToolResult(t *testing.T) {
    response := ReadResponse{Output: NewImageOutput(ImageFile{Base64: "aGVsbG8=", MIMEType: "image/png", OriginalSize: 5})}
    messages, err := MapReadResult("tool-1", response)
    if err != nil {
        t.Fatal(err)
    }
    got, _ := json.Marshal(messages)
    want := `[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}]`
    if string(got) != want {
        t.Fatalf("messages = %s", got)
    }
}
```

Cover text line numbering plus empty/past-offset warnings, PDF metadata string plus document supplemental message, parts metadata string plus page-image supplemental message, file-unchanged stub, and propagation of existing `ReadResponse.NewMessages` without duplicate media.

- [ ] **Step 8: Run mapping tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestMapReadResult' -count=1`

Expected: FAIL because mapping is undefined.

- [ ] **Step 9: Implement mapping and verify GREEN**

Keep tool correlation in one user message containing `tool_result`; append supplemental meta messages after it. Preserve Claude Code's `FILE_UNCHANGED_STUB`, empty-file warning, past-offset warning, and malware-analysis system reminder as exact constants covered by tests.

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestEngineRead|TestMapReadResult' -count=1`

Expected: PASS.

- [ ] **Step 10: Commit Engine and message mapping**

```bash
git add perception/multimodal-fusion/claude_multimodel_fusion/reader.go perception/multimodal-fusion/claude_multimodel_fusion/reader_test.go perception/multimodal-fusion/claude_multimodel_fusion/tool_result.go perception/multimodal-fusion/claude_multimodel_fusion/tool_result_test.go perception/multimodal-fusion/claude_multimodel_fusion/integration_test.go
git commit -m "feat: add claude read routing and message mapping"
```

---

### Task 7: API Media Limit And Compaction Lifecycle

**Files:**
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/media_lifecycle.go`
- Test: `perception/multimodal-fusion/claude_multimodel_fusion/media_lifecycle_test.go`

**Interfaces:**
- Consumes Task 1 `Message` and `ContentBlock`.
- Produces `PrepareMessagesForAPI(messages []Message, mediaLimit int) ([]Message, error)`.
- Produces `PrepareMessagesForCompaction(messages []Message) []Message`.

- [ ] **Step 1: Write failing media-order and immutability tests**

```go
// TestPrepareMessagesForAPIRemovesOldestMediaAcrossTopLevelAndNested 验证跨层级媒体淘汰顺序。
func TestPrepareMessagesForAPIRemovesOldestMediaAcrossTopLevelAndNested(t *testing.T) {
    input := mediaHistory(101)
    got, err := PrepareMessagesForAPI(input, APIMaxMediaPerRequest)
    if err != nil {
        t.Fatal(err)
    }
    if countMedia(got) != 100 || containsMediaID(got, "media-000") {
        t.Fatalf("oldest media was not removed")
    }
    if countMedia(input) != 101 {
        t.Fatal("input history was mutated")
    }
}

// TestPrepareMessagesForCompactionReplacesNestedMediaInPlace 验证 compact 保留 block 相对位置。
func TestPrepareMessagesForCompactionReplacesNestedMediaInPlace(t *testing.T) {
    input := nestedMediaMessage()
    got := PrepareMessagesForCompaction(input)
    blocks := got[0].Content[0].ToolContent.Blocks
    if blocks[0].Text != "before" || blocks[1].Text != "[image]" || blocks[2].Text != "[document]" || blocks[3].Text != "after" {
        t.Fatalf("blocks = %#v", blocks)
    }
}
```

Define the lifecycle fixtures in the same test file:

```go
// mediaHistory 构造按 Data 字段编号的顶层图片历史。
func mediaHistory(count int) []Message {
    messages := make([]Message, 0, count)
    for i := 0; i < count; i++ {
        id := fmt.Sprintf("media-%03d", i)
        messages = append(messages, Message{Role: RoleUser, Content: []ContentBlock{{
            Type: BlockImage,
            Source: &MediaSource{Type: SourceBase64, MediaType: "image/png", Data: id},
        }}})
    }
    return messages
}

// countMedia 统计顶层和 tool result 内嵌的图片/文档数量。
func countMedia(messages []Message) int {
    total := 0
    for _, message := range messages {
        for _, block := range message.Content {
            if block.Type == BlockImage || block.Type == BlockDocument {
                total++
            }
            if block.Type != BlockToolResult {
                continue
            }
            for _, nested := range block.ToolContent.Blocks {
                if nested.Type == BlockImage || nested.Type == BlockDocument {
                    total++
                }
            }
        }
    }
    return total
}

// containsMediaID 检查测试媒体标识是否仍在历史中。
func containsMediaID(messages []Message, id string) bool {
    for _, message := range messages {
        for _, block := range message.Content {
            if block.Source != nil && block.Source.Data == id {
                return true
            }
            for _, nested := range block.ToolContent.Blocks {
                if nested.Source != nil && nested.Source.Data == id {
                    return true
                }
            }
        }
    }
    return false
}

// nestedMediaMessage 构造文本、图片、文档交错的 tool result。
func nestedMediaMessage() []Message {
    return []Message{{Role: RoleUser, Content: []ContentBlock{{
        Type: BlockToolResult,
        ToolUseID: "tool-1",
        ToolContent: ToolResultBlocks([]ContentBlock{
            {Type: BlockText, Text: "before"},
            {Type: BlockImage, Source: &MediaSource{Type: SourceBase64, MediaType: "image/png", Data: "image"}},
            {Type: BlockDocument, Source: &MediaSource{Type: SourceBase64, MediaType: "application/pdf", Data: "pdf"}},
            {Type: BlockText, Text: "after"},
        }),
    }}}}
}
```

Production may use one unexported traversal helper, but the test counters above deliberately traverse independently so a bug in the production walker cannot make both implementation and assertion agree incorrectly.

Also test top-level Base64 image >5 MiB rejection, nested-image non-rejection matching Claude's current validation visibility, `mediaLimit<=0` defaulting to 100, empty tool result preservation, assistant/system no-op, and document/image order.

- [ ] **Step 2: Run lifecycle tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestPrepareMessages' -count=1`

Expected: FAIL because lifecycle functions are undefined.

- [ ] **Step 3: Implement deep-copy traversal and verify GREEN**

Use one internal depth-aware walker for top-level and nested blocks, but keep API pruning and compact replacement as separate policies. Media order is message order, then top-level block order, then nested `tool_result.content` order.

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestPrepareMessages' -count=1`

Expected: PASS.

- [ ] **Step 4: Commit media lifecycle**

```bash
git add perception/multimodal-fusion/claude_multimodel_fusion/media_lifecycle.go perception/multimodal-fusion/claude_multimodel_fusion/media_lifecycle_test.go
git commit -m "feat: add claude media lifecycle"
```

---

### Task 8: OpenAI Chat Completions / Eino Image Bridge

**Files:**
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/openai_bridge.go`
- Test: `perception/multimodal-fusion/claude_multimodel_fusion/openai_bridge_test.go`
- Test: `perception/multimodal-fusion/claude_multimodel_fusion/openai_wire_test.go`

**Interfaces:**
- Consumes Task 1 messages and Task 6 tool-result shapes.
- Produces `ToOpenAIChatMessages(messages []Message) ([]*schema.Message, error)`.

- [ ] **Step 1: Write failing bridge-structure tests**

```go
// TestToOpenAIChatMessagesMapsBase64ImageToUserMultiContent 验证 Base64 图片进入 Eino user 多模态字段。
func TestToOpenAIChatMessagesMapsBase64ImageToUserMultiContent(t *testing.T) {
    got, err := ToOpenAIChatMessages([]Message{{
        Role: RoleUser,
        Content: []ContentBlock{
            {Type: BlockText, Text: "看图"},
            {Type: BlockImage, Source: &MediaSource{Type: SourceBase64, MediaType: "image/png", Data: "aGVsbG8="}},
        },
    }})
    if err != nil {
        t.Fatal(err)
    }
    if len(got) != 1 || got[0].Role != schema.User || len(got[0].UserInputMultiContent) != 2 {
        t.Fatalf("messages = %#v", got)
    }
    image := got[0].UserInputMultiContent[1].Image
    if image == nil || image.Base64Data == nil || *image.Base64Data != "aGVsbG8=" || image.MIMEType != "image/png" {
        t.Fatalf("image = %#v", image)
    }
}

// TestToOpenAIChatMessagesLiftsToolImageAfterToolResult 验证工具图片被提升为后续 user 消息。
func TestToOpenAIChatMessagesLiftsToolImageAfterToolResult(t *testing.T) {
    got, err := ToOpenAIChatMessages([]Message{nestedToolImageMessage("tool-1")})
    if err != nil {
        t.Fatal(err)
    }
    if len(got) != 2 || got[0].Role != schema.Tool || got[0].ToolCallID != "tool-1" || got[1].Role != schema.User {
        t.Fatalf("messages = %#v", got)
    }
}
```

Define the bridge fixtures explicitly:

```go
// nestedToolImageMessage 构造带文本和图片的 Claude tool result。
func nestedToolImageMessage(toolUseID string) Message {
    return Message{Role: RoleUser, Content: []ContentBlock{{
        Type: BlockToolResult,
        ToolUseID: toolUseID,
        ToolContent: ToolResultBlocks([]ContentBlock{
            {Type: BlockText, Text: "image read"},
            {Type: BlockImage, Source: &MediaSource{Type: SourceBase64, MediaType: "image/png", Data: "aGVsbG8="}},
        }),
    }}}
}

// base64ImageMessages 构造 OpenAI 线级测试使用的 Base64 user image。
func base64ImageMessages() []Message {
    return []Message{{Role: RoleUser, Content: []ContentBlock{
        {Type: BlockText, Text: "看图"},
        {Type: BlockImage, Source: &MediaSource{Type: SourceBase64, MediaType: "image/png", Data: "aGVsbG8="}},
    }}}
}
```

Add remote URL, image detail `auto`, text-only, multiple tool results, document unsupported, malformed source, and order-preservation cases.

- [ ] **Step 2: Run bridge tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestToOpenAIChatMessages' -count=1`

Expected: FAIL because bridge is undefined.

- [ ] **Step 3: Implement provider bridge and verify GREEN**

Mapping rules:

```go
schema.MessageInputPart{
    Type: schema.ChatMessagePartTypeImageURL,
    Image: &schema.MessageInputImage{
        MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: source.MediaType},
        Detail: schema.ImageURLDetailAuto,
    },
}
```

For a URL source, set `MessagePartCommon.URL` instead of `Base64Data`. Convert each `tool_result` to `schema.Tool` with `ToolCallID`; concatenate only its text blocks into tool content and emit its images in a following `schema.User.UserInputMultiContent`. Return `ErrOpenAIDocumentUnsupported` for document blocks instead of relabeling PDF bytes.

Run the command from Step 2 again.

Expected: PASS.

- [ ] **Step 4: Write failing final-HTTP-request tests**

```go
// TestEinoOpenAIRequestContainsBase64ImageURL 验证最终 HTTP 请求包含 data URL 图片参数。
func TestEinoOpenAIRequestContainsBase64ImageURL(t *testing.T) {
    requestBody := make(chan []byte, 1)
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        body, _ := io.ReadAll(r.Body)
        requestBody <- body
        w.Header().Set("Content-Type", "application/json")
        io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
    }))
    defer server.Close()

    model, err := openaimodel.NewChatModel(context.Background(), &openaimodel.ChatModelConfig{
        APIKey: "test-key",
        BaseURL: server.URL + "/v1",
        Model: "gpt-test",
    })
    if err != nil {
        t.Fatal(err)
    }
    messages, err := ToOpenAIChatMessages(base64ImageMessages())
    if err != nil {
        t.Fatal(err)
    }
    if _, err := model.Generate(context.Background(), messages); err != nil {
        t.Fatal(err)
    }
    body := <-requestBody
    if !bytes.Contains(body, []byte(`"type":"image_url"`)) || !bytes.Contains(body, []byte(`data:image/png;base64,aGVsbG8=`)) {
        t.Fatalf("request body = %s", body)
    }
}
```

Add a second request test for a remote HTTPS URL and assert the request uses user-role `content[]`, not a serialized internal `user_input_multi_content` field.

- [ ] **Step 5: Run wire tests and verify RED**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestEinoOpenAIRequest' -count=1`

Expected: FAIL until the bridge exactly matches the pinned Eino adapter contract.

- [ ] **Step 6: Correct bridge/request configuration and verify GREEN**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestToOpenAIChatMessages|TestEinoOpenAIRequest' -count=1`

Expected: PASS with both URL and Base64 request bodies captured locally.

- [ ] **Step 7: Commit OpenAI bridge**

```bash
git add perception/multimodal-fusion/claude_multimodel_fusion/openai_bridge.go perception/multimodal-fusion/claude_multimodel_fusion/openai_bridge_test.go perception/multimodal-fusion/claude_multimodel_fusion/openai_wire_test.go
git commit -m "feat: add openai image input bridge"
```

---

### Task 9: Source Mapping, Integration Fixtures, And Package Documentation

**Files:**
- Verify: `perception/multimodal-fusion/claude_multimodel_fusion/integration_test.go`
- Create: `perception/multimodal-fusion/claude_multimodel_fusion/README.md`
- Verify: every production and test file created in Tasks 1-8.

**Interfaces:**
- Consumes the complete public package surface.
- Produces reviewer-facing source-to-test matrix and reruns the end-to-end local Read proofs created before Task 6 implementation.

- [ ] **Step 1: Run the existing end-to-end package tests**

```go
env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -run 'TestIntegration' -count=1
```

Expected: PASS, with an explicit SKIP only for absent real Poppler binaries.

- [ ] **Step 2: Write the source-to-implementation matrix**

`README.md` must contain these exact columns:

```markdown
| Claude Code source | Go implementation | Proof test | Compatibility |
|---|---|---|---|
| `src/tools/FileReadTool/FileReadTool.ts` | `reader.go`, `tool_result.go` | `TestEngineReadReturnsFileUnchangedOnlyForSameTextRangeAndMTime`, `TestMapReadResultNestsImageInToolResult` | parity |
| `src/tools/FileReadTool/limits.ts` | `limits.go`, `text.go` | `TestLimitsMatchClaudeCode`, `TestReadTextNormalizesBOMCRLFAndUsesOneBasedOffset` | parity |
| `src/utils/imageResizer.ts` | `image.go` | `TestImageCompressionAttemptsPNGThenJPEGQualities` | codec-equivalent |
| `src/utils/imageStore.ts` | `image_store.go` | `TestImageStoreWritesPrivateFileAndEvictsOnlyIndex` | parity |
| `src/utils/pdf.ts`, `src/utils/pdfUtils.ts` | `pdf.go`, `validation.go` | `TestReadPDFDocumentRejectsMissingMagic`, `TestEngineReadRepairsDiscardedAutomaticPDFExtraction` | repaired |
| `src/services/api/claude.ts`, `src/utils/imageValidation.ts` | `media_lifecycle.go` | `TestPrepareMessagesForAPIRemovesOldestMediaAcrossTopLevelAndNested` | parity |
| `src/services/compact/compact.ts` | `media_lifecycle.go` | `TestPrepareMessagesForCompactionReplacesNestedMediaInPlace` | parity |
| OpenAI image-input contract + Eino ACL v0.1.17 | `openai_bridge.go` | `TestEinoOpenAIRequestContainsBase64ImageURL` | provider bridge |
```

Document the public API with one Anthropic example and one Eino/OpenAI example. State that `.ipynb`, OCR, report fusion, network model loops, permissions UI, telemetry, and cache editing are excluded.

- [ ] **Step 3: Verify README references resolve**

Run:

```bash
rg -n 'src/tools/FileReadTool/FileReadTool.ts|src/utils/imageResizer.ts|src/utils/pdf.ts|openai_bridge.go|repaired' perception/multimodal-fusion/claude_multimodel_fusion/README.md
rg -n '^func Test' perception/multimodal-fusion/claude_multimodel_fusion/*_test.go
```

Expected: each matrix row has at least one real production file and one real test function in the package.

- [ ] **Step 4: Commit documentation and integration coverage**

```bash
git add perception/multimodal-fusion/claude_multimodel_fusion/README.md
git commit -m "docs: map claude multimodal parity"
```

---

### Task 10: Requirement Audit And Final Verification

**Files:**
- Verify: `perception/multimodal-fusion/claude_multimodel_fusion/`
- Verify: `docs/superpowers/specs/2026-07-10-claude-multimodel-fusion-design.md`
- Verify: `docs/superpowers/plans/2026-07-10-claude-multimodel-fusion.md`

**Interfaces:**
- Consumes the complete implementation.
- Produces fresh verification evidence and a requirement-by-requirement handoff; no new feature surface.

- [ ] **Step 1: Format all Go files**

Run: `gofmt -w perception/multimodal-fusion/claude_multimodel_fusion/*.go`

Expected: command exits 0.

- [ ] **Step 2: Run the focused package tests with race detection**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test -race ./perception/multimodal-fusion/claude_multimodel_fusion -count=1`

Expected: PASS; real Poppler test may report SKIP only when a required binary is absent.

- [ ] **Step 3: Run the parent multimodal package regression tests**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/... -count=1`

Expected: PASS.

- [ ] **Step 4: Run static analysis**

Run: `env GOCACHE=/private/tmp/ai-designing-go-build go vet ./perception/multimodal-fusion/claude_multimodel_fusion`

Expected: PASS with no diagnostics.

- [ ] **Step 5: Audit prohibited and required scope**

Run:

```bash
if rg -n -i 'notebook|NotebookFile|Jupyter' perception/multimodal-fusion/claude_multimodel_fusion; then exit 1; fi
rg -n 'APIImageMaxBase64Size|PDFExtractSizeThreshold|APIMaxMediaPerRequest|PrepareMessagesForCompaction|ToOpenAIChatMessages' perception/multimodal-fusion/claude_multimodel_fusion
```

Expected: the prohibited search has no matches; every required constant/function has production and test references.

- [ ] **Step 6: Inspect the complete diff and requirement matrix**

Run:

```bash
git status --short
git diff --check 0e72f7c..HEAD
git diff --stat 0e72f7c..HEAD
```

Expected: only this package, its direct dependency metadata, README, spec, and plan are in scope; no whitespace errors.

- [ ] **Step 7: Request code review before completion claim**

Invoke `superpowers:requesting-code-review`, inspect every reported issue against the spec and runtime evidence, fix valid findings through fresh RED/GREEN cycles, and rerun Steps 1-6.

- [ ] **Step 8: Commit verification-only corrections if present**

If review required code or documentation corrections, stage only those files and commit with `fix: align claude multimodal parity`. If no correction exists, do not create an empty commit.
