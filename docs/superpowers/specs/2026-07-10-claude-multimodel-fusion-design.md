# Claude Code 多模态读取与上下文生命周期复刻设计

## 目标

在 `perception/multimodal-fusion/claude_multimodel_fusion/` 新建独立 Go 包，按方案 2 复刻本地 restored Claude Code 的多模态读取链。这里的“1:1”指外部可观察行为一致：输入参数、输出类型、Anthropic 消息 JSON、媒体嵌套层级、阈值、处理顺序、错误语义、媒体淘汰顺序和 compact 占位行为均有源码映射与测试证明。

本包不复用现有 `multimodal-fusion` 的报告语义层。它不会做 OCR、关键图表筛选、表格 Markdown、日志摘要或报告事实核查；原始图片与 PDF 仍作为 `image` / `document` 内容块进入模型上下文。

## 1:1 的判定标准

### 必须一致

- `Read` 输入字段：`file_path`、`offset`、`limit`、`pages`。
- 输出联合类型：`text`、`image`、`pdf`、`parts`、`file_unchanged`。
- 图片、PDF 输出在 `tool_result` 和 supplemental user message 中的层级。
- 图片/PDF 的 Base64 `source` 结构和 MIME 类型。
- 图片尺寸、原始大小、显示尺寸及坐标映射元数据。
- PDF 整体读取和指定页渲染两条路径。
- 文本范围读取、行号、默认大小/token 限制和相同范围去重。
- API 前最多保留 100 个媒体项，超限时从最旧媒体开始删除。
- compact 前把顶层及 `tool_result.content` 内的图片/PDF替换成 `[image]` / `[document]`。
- Claude Code 原始常量和用户可见错误文案。

### 不要求逐字节一致

TypeScript 使用 Sharp/libvips，Go 使用 Go 图像编解码器。两种实现对同一张需要重编码的图片不要求生成完全相同的压缩字节，但必须满足相同的 MIME、最大尺寸、大小/token 上限、降级顺序和视觉内容等价。无需重编码的图片必须原样保留字节。

### repaired compatibility

本地 restored 源码的无 `pages` PDF 分支存在一处断链：命中 `shouldExtractPages` 后虽然运行 `extractPDFPages`，却丢弃了结果并继续读取整个 PDF。方案 2 按同文件注释和返回类型表达的预期行为修复：

- 大于 3 MB 的 PDF，或当前模型不支持 `document` block 时，成功渲染的页面图片必须作为 `parts` 和 supplemental image messages 返回。
- 其他阈值、分页限制和错误仍保持源码行为。
- README 和测试会把此处标为 `repaired`，不把修复伪装成字面移植。

## 权威源码映射

| Go 责任 | Claude Code 权威源 |
|---|---|
| Read 输入/输出、分发、去重、tool result | `src/tools/FileReadTool/FileReadTool.ts` |
| Read 默认大小/token 限制 | `src/tools/FileReadTool/limits.ts` |
| 图片缩放、压缩、格式检测、元数据 | `src/utils/imageResizer.ts` |
| 粘贴图片私有缓存和路径索引 | `src/utils/imageStore.ts` |
| PDF 整体读取、页数检测、Poppler 渲染 | `src/utils/pdf.ts`、`src/utils/pdfUtils.ts` |
| 直接图片输入和 user message 组装 | `src/utils/processUserInput/processUserInput.ts`、`processTextPrompt.ts` |
| API 媒体上限 | `src/services/api/claude.ts` |
| API 图片大小校验 | `src/utils/imageValidation.ts` |
| compact 媒体占位 | `src/services/compact/compact.ts` |
| OpenAI 图片输入协议 | OpenAI Images and vision 官方文档、仓库锁定的 Eino OpenAI adapter |

## 范围

### 实现范围

1. 本地文本、图片、PDF 读取。
2. 粘贴/调用方提供的 Base64 图片转换成 Anthropic image block，并按 Claude Code 方式保存私有图片缓存以生成稳定来源路径。
3. 图片格式检测、尺寸缩放、大小压缩和 token budget 压缩。
4. PDF `document` block、显式页范围解析、`pdfinfo` 和 `pdftoppm`。
5. `Read` 工具结果映射及 supplemental meta user messages。
6. API 前媒体数量限制和图片 Base64 上限校验。
7. compact 前图片/文档占位替换。
8. Anthropic 内容块到 OpenAI Chat Completions/Eino 多模态 user message 的图片桥接。
9. README 中逐条列出 Claude 源文件、Go 文件、测试和是否 repaired。

### 不实现范围

- Claude Code 终端 UI、剪贴板监听和 React/Ink 组件。
- `.ipynb` 文件解析和输出映射。
- 权限规则、UNC 凭证防护 UI 和用户审批交互。
- telemetry、GrowthBook、实验开关、skill discovery、memory attachment。
- Anthropic/OpenAI 网络请求和流式模型循环。
- 现有报告 Agent 的 OCR、图表筛选、表格/日志语义融合。
- Claude 服务端 cache editing。

排除这些基础设施不改变本包的媒体输入输出契约。调用方可直接序列化 Anthropic wire message，也可以通过本包的 OpenAI bridge 生成 Eino 多模态 user message。

## 包结构

```text
perception/multimodal-fusion/claude_multimodel_fusion/
├── types.go                 # Anthropic block、消息和 Read 联合结果
├── limits.go                # Claude Code 固定阈值
├── validation.go            # 输入、页范围、设备/二进制和 API 图片校验
├── image.go                 # 格式检测、图片读取、缩放、压缩和元数据
├── image_store.go           # 粘贴图片私有缓存和 200 条路径索引
├── pdf.go                   # PDF 整体读取、页数和 Poppler 分页
├── text.go                  # 范围读取、行号、大小/token 限制
├── reader.go                # Read 总入口、路由和 mtime 去重
├── prompt.go                # 直接图片输入和 user message 构造
├── tool_result.go           # Read 结果映射及 supplemental message
├── media_lifecycle.go       # 100 媒体上限和 compact 占位
├── command.go               # 可替换外部命令执行边界
├── openai_bridge.go         # OpenAI Chat Completions/Eino 图片输入桥接
├── README.md                # 源码/实现/测试对照表
├── testdata/
└── *_test.go
```

每个文件只承担一种责任。所有新增结构体、函数和核心分支均写中文用途注释；prompt 或模型可见补充文本使用中文，但为了 1:1 保留的 Claude Code 原始错误文案、XML 标记和占位符不翻译。

## 核心数据结构

### Anthropic 内容块

`ContentBlock` 表达四种本包需要发送的内容：

```text
text        -> {type:"text", text:"..."}
image       -> {type:"image", source:{type:"base64", media_type:"image/png", data:"..."}}
document    -> {type:"document", source:{type:"base64", media_type:"application/pdf", data:"..."}}
tool_result -> {type:"tool_result", tool_use_id:"...", content:"..." | [...]}
```

`tool_result.content` 需要同时支持字符串和 block 数组。Go 结构使用显式联合包装并实现 JSON marshal/unmarshal，禁止用 `any` 把 wire contract 变成不可检查的动态对象。

### 消息

`Message` 保留 API 需要的 `role` 和 `content`；本地额外保留 `is_meta`、消息 ID、图片粘贴 ID 等不进入 wire JSON 的运行时字段。深拷贝函数确保媒体裁剪和 compact 不修改调用方持有的历史。

### Read 结果

`ReadOutput.Type` 是判别字段，其余 payload 使用明确指针：

- `TextFile`：路径、内容、返回行数、起始行、总行数。
- `ImageFile`：Base64、MIME、原始大小、可选尺寸。
- `PDFFile`：路径、Base64、原始大小。
- `PDFParts`：路径、原始大小、页数、输出目录、页图片路径。
- `UnchangedFile`：路径。

构造函数保证同一结果只能有一个 payload，验证函数拒绝判别字段与 payload 不一致的结果。

## 公共接口

```go
type Engine struct { /* 文件状态、命令执行器、图片处理器和图片缓存 */ }

func NewEngine(config EngineConfig) *Engine

func (e *Engine) Read(ctx context.Context, input ReadInput) (ReadResponse, error)

func MapReadResult(toolUseID string, response ReadResponse) ([]Message, error)

func (e *Engine) BuildUserPrompt(ctx context.Context, text string, images []PastedImage) ([]Message, error)

func ToOpenAIChatMessages(messages []Message) ([]*schema.Message, error)

func PrepareMessagesForAPI(messages []Message, mediaLimit int) ([]Message, error)

func PrepareMessagesForCompaction(messages []Message) []Message
```

`ReadResponse` 同时包含 `Output` 和 `NewMessages`。这对应 Claude Code 工具执行返回的 `data` 与 `newMessages`：图片数据可位于 `tool_result`，完整 PDF 和分页图片通过 supplemental meta user message 进入下一轮上下文。

`EngineConfig` 明确提供 `PDFDocumentSupported`、`MaxTextBytes`、`MaxTokens`、`TokenCounter`、`CommandRunner`、`ImageProcessor`、`ImageStoreDir` 和 `SessionID`。默认阈值使用下文固定常量；依赖接口为空时装配真实本地实现。测试通过依赖注入控制命令结果和图片压缩顺序，不增加测试专用生产方法。

## 处理流程

### 1. 直接图片输入

1. 调用方传入文本和 `PastedImage`。
2. 校验 Base64，检测实际图片格式。
3. 对没有显式来源路径的图片，以 `0600` 权限写入 `<ImageStoreDir>/<SessionID>/<id>.<ext>`；内存只保留最近 200 个 ID 到路径映射。
4. 按图片算法缩放/压缩。
5. 生成文本 block 在前、图片 blocks 在后的 user message。
6. 若图片被缩放或具有来源路径，追加独立 meta user message：`[Image: source: ..., original ... displayed at ...]`。

### 2. Read 图片

1. 扩展路径并按后缀路由到图片读取。
2. 文件只读一次，空文件立即报错。
3. 从 magic bytes 检测 PNG/JPEG/GIF/WebP，不信任扩展名。
4. 先执行标准缩放；再用 `ceil(base64长度 * 0.125)` 估算 token。
5. 超过 token budget 时从同一原始 buffer 进行更激进压缩。
6. 输出 `ImageFile`，映射为嵌套于 `tool_result.content` 的 image block。

### 3. Read PDF

显式提供 `pages`：

1. 解析 `N`、`N-M`、`N-`，页码从 1 开始。
2. 单次范围大于 20 页时报错。
3. 使用 `pdftoppm -jpeg -r 100`，可选 `-f/-l`。
4. 自然排序页面 JPEG，逐张执行图片标准缩放。
5. `ReadOutput` 为 `parts`；tool result 返回元数据文本；页面作为 supplemental meta user image blocks。

未提供 `pages`：

1. `pdfinfo` 可读取页数且超过 10 页时，要求调用方显式指定页范围。
2. PDF 大于 3 MB，或模型不支持 PDF document 时，走 repaired 自动分页路径。
3. 其余 PDF 校验非空、20 MB 上限和 `%PDF-` magic bytes，然后生成 document block。
4. PDF tool result 只包含路径/大小说明，真实 PDF 放在 supplemental meta user document block。

### 4. Read 文本

1. 默认 `offset=1`；内部转成 0-based。
2. 未提供 `limit` 时整文件大于 256 KB 直接报错；提供 `limit` 时只返回指定行范围。
3. 去除 UTF-8 BOM，把 CRLF 规范成 LF。
4. 输出带 `cat -n` 风格行号。
5. 内容接近 25,000-token 阈值时先精确计数；Go 包通过可注入 `TokenCounter` 提供精确计数，没有计数器时使用与源码保守方向一致的估算。
6. 相同路径、offset、limit 且 mtime 未变化时返回 `file_unchanged`；该去重只用于文本，图片/PDF不进入 read state。

## 图片算法

固定顺序如下：

1. 空 buffer 报错。
2. magic bytes 检测实际格式；未知格式与 Claude Code 一致回退 `image/png`，后续解码失败再进入错误路径。
3. 若原始大小不超过 3.75 MB 且宽高不超过 2000，原样返回。
4. 只超大小、不超尺寸时：PNG 先尝试最高压缩；随后尝试 JPEG quality `80、60、40、20`。
5. 尺寸超限时保持纵横比约束到 `2000×2000` 内，再重复 PNG/JPEG 压缩顺序。
6. 仍过大时，宽度最多 1000，JPEG quality 20。
7. token 压缩把 token 限制换算为 `maxBytes=floor(floor(maxTokens/0.125)*0.75)`，再按缩放因子 `1.0、0.75、0.5、0.25` 尝试。
8. Read 图片 token 压缩失败时最后尝试 `400×400` 内、JPEG quality 20；仍失败则按源码回退原图结果，API 边界校验负责最终拒绝超限数据。

Go 默认图片处理器使用标准库编码器和 `golang.org/x/image` 解码/缩放能力。`ImageProcessor` 接口允许测试精确验证调用顺序，也允许以后替换成 libvips 实现，但默认实现必须真实处理图片，不得只返回 mock 元数据。

## 固定阈值

| 常量 | 值 |
|---|---:|
| `APIImageMaxBase64Size` | `5 * 1024 * 1024` |
| `ImageTargetRawSize` | `3.75 MB` |
| `ImageMaxWidth` / `ImageMaxHeight` | `2000` |
| `PDFTargetRawSize` | `20 MB` |
| `APIPDFMaxPages` | `100` |
| `PDFExtractSizeThreshold` | `3 MB` |
| `PDFMaxExtractSize` | `100 MB` |
| `PDFMaxPagesPerRead` | `20` |
| `PDFAtMentionInlineThreshold` | `10` |
| `APIMaxMediaPerRequest` | `100` |
| `DefaultMaxOutputTokens` | `25,000` |
| `DefaultMaxOutputSize` | `256 KB` |

## 媒体生命周期

### API 前

`PrepareMessagesForAPI`：

1. 深拷贝消息。
2. 统计顶层 image/document 和 `tool_result.content` 内嵌媒体。
3. 总数超过限制时，按消息和 block 原顺序删除最旧媒体，保留最近媒体。
4. 校验顶层 Base64 image 长度不超过 5 MB，保持 Claude Code 当前只检查顶层 user image 的可见行为。
5. 不删除空的 `tool_result`，以保持工具调用配对关系。

### compact 前

`PrepareMessagesForCompaction`：

- 只改写 user messages。
- 顶层 image 变成文本 `[image]`。
- 顶层 document 变成文本 `[document]`。
- `tool_result.content` 数组里的媒体在原位置替换成相同占位文本。
- assistant、system 和纯文本消息保持不变。

## OpenAI / Eino 边界

### 已验证的协议能力

OpenAI 官方 Chat Completions 接口支持在 user message 的 `content` 数组中传图片：图片 part 类型为 `image_url`，既可传完整 HTTP(S) URL，也可传 `data:<mime>;base64,<data>`。Responses API 使用另一套 `input_image` 结构，但当前仓库锁定的 `github.com/cloudwego/eino-ext/components/model/openai v0.1.8` 走 Chat Completions，因此本包只实现并验证 Chat Completions bridge，不混用 Responses 字段。

仓库锁定的 `eino v0.9.6` 和 `libs/acl/openai v0.1.17` 已确认具备对应转换：

- 文本写入 `schema.MessageInputPart{Type: ChatMessagePartTypeText}`。
- 远程图片写入 `schema.MessageInputImage.URL`。
- 本地/Base64 图片写入 `schema.MessageInputImage.Base64Data` 和 `MIMEType`；ACL 在请求边界组装成 `data:<mime>;base64,...`。
- 多模态输入挂在 `schema.User.UserInputMultiContent`，图片 `Detail` 默认使用 `auto`。

### bridge 行为

Anthropic wire message 仍是 Claude Code 1:1 主契约；`openai_bridge.go` 负责显式的 provider 适配：

1. 顶层 user text/image blocks 原序转换成一个 `schema.User` 的 `UserInputMultiContent`。
2. 图片有远程 URL 时保留 URL；只有 Base64 时保留 MIME 与 Base64，由 Eino ACL 生成 data URL。
3. Claude `tool_result` 转为 `schema.Tool`：`tool_use_id` 写入 `ToolCallID`，字符串内容或文本 blocks 写入工具结果文本。其内嵌图片不直接作为 OpenAI tool-role 图片发送，而是按原顺序提升为紧随该工具结果之后的 supplemental `schema.User` 多模态消息，避免依赖 OpenAI 对 tool-role 图片的非标准支持。
4. PDF 分页产生的 supplemental image messages 按相同规则进入 OpenAI；因此 OpenAI 模式应配置 `PDFDocumentSupported=false`，让无 `pages` 的 PDF 走 repaired 自动分页路径。
5. Anthropic `document` block 没有映射到当前 Eino Chat Completions adapter 的等价结构；若仍传入 document，bridge 返回明确的 unsupported 错误，禁止把 Base64 PDF伪装成文本或图片。
6. 目标 OpenAI 或 OpenAI-compatible 模型仍须具备视觉输入能力；bridge 只保证协议结构正确，不把模型能力探测伪装成本地静态判断。

`ToOpenAIChatMessages` 不发送网络请求。集成测试会使用本地 `httptest.Server` 接住 Eino OpenAI ChatModel 发出的请求，断言最终 JSON 确实包含 user `content[]`、`type:"image_url"` 和正确 URL/data URL；验收不以“Go 结构体看起来正确”代替适配器线级证明。

## 错误处理

- 页范围格式错误：保留 Claude Code error code 7 和原始英文文案。
- 页范围超过 20：error code 8。
- 阻塞设备路径：error code 9；拒绝 `/dev/zero`、`/dev/random`、标准输入输出 fd 等。
- 非支持二进制：error code 4。
- 图片/PDF 空文件、图片压缩失败、PDF magic 错误、PDF 过大均返回可分类错误。
- Poppler 缺失、超时、密码保护、损坏和未知失败分别映射成 `unavailable/password_protected/corrupted/unknown`。
- 外部命令仅通过 `CommandRunner` 执行，超时固定为 `pdfinfo 10s`、`pdftoppm availability 5s`、页面渲染 120s。
- context 取消必须终止文件读取和外部命令。

## 测试策略

所有生产函数先写失败测试，再实现最小代码，遵循 RED -> GREEN -> REFACTOR。测试不依赖真实模型和网络。

### 单元测试

- 内容块和 message JSON golden tests。
- `tool_result.content` 字符串/数组两种 wire shape。
- PNG/JPEG/GIF/WebP magic bytes 检测和未知格式回退。
- 图片无需处理、仅压缩、仅缩放、缩放后压缩、token budget、400×400 降级。
- PDF 页范围全部合法/非法边界。
- PDF 空文件、magic、20 MB/100 MB 阈值。
- fake `CommandRunner` 验证 `pdfinfo/pdftoppm` 参数、超时和错误分类。
- 粘贴图片缓存权限、路径格式和超过 200 条后的索引淘汰。
- 文本 offset/limit、BOM、CRLF、256 KB、25,000 tokens、mtime 去重。
- Read 结果到 tool result/supplemental messages 的精确映射。
- 101 个媒体删除最旧 1 个，覆盖顶层和 nested 两种情况。
- compact 顶层/nested 媒体占位且不修改原输入。
- OpenAI bridge 的文本、远程 URL、Base64+MIME、tool-result 图片提升和 document 明确失败。

### 集成测试

- 使用包内 testdata 真实读取文本、图片和小 PDF。
- Poppler 可用时运行真实 `pdfinfo/pdftoppm` 分页测试；不可用时验证 `unavailable` 路径，不把环境缺失算成假成功。
- 对 repaired 自动分页路径做单独测试，证明大 PDF 或不支持 document 的模型返回页面图片，而不是丢弃 extraction 结果。
- 使用本地 `httptest.Server` 捕获 Eino OpenAI ChatModel 请求，证明远程 URL 和 Base64 图片最终序列化为 OpenAI Chat Completions `image_url` part；不访问外部模型服务。

### 验证命令

```bash
env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/claude_multimodel_fusion -count=1
env GOCACHE=/private/tmp/ai-designing-go-build go test ./perception/multimodal-fusion/... -count=1
go vet ./perception/multimodal-fusion/claude_multimodel_fusion
```

## 验收清单

- 指定目录存在且是独立 Go 包。
- README 的每条 Claude 源码映射都有实现文件和测试名。
- 所有固定阈值与 Claude 源码一致。
- 图片、PDF、parts、文本和 file-unchanged 五类输出均可实际产生。
- direct prompt、tool result、supplemental message 三种消息路径均有 JSON golden proof。
- PDF 整体、显式分页和 repaired 自动分页三条路径均通过真实或可验证的命令边界。
- API 媒体裁剪和 compact 占位覆盖顶层及 nested 媒体。
- OpenAI/Eino 路径有最终 HTTP 请求 JSON 证明图片参数真实进入 Chat Completions，而非只停留在内部结构体。
- 不调用 OCR、报告 fuser 或模型生成内容。
- 所有新增函数和结构体带中文用途注释。
- 包级测试、父目录测试和 vet 均成功。
