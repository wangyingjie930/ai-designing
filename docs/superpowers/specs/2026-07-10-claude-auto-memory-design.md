# Claude Code 自动记忆核心链路复刻设计

## 目标

在 `ai-designing` 中新增一个可运行、可测试、适合面试讲解的 Claude Code 风格自动记忆模块。它不追求外围基础设施 1:1，而是复刻最能体现系统设计价值的闭环：

```text
上一轮对话
  -> 独立记忆提取
  -> 模型选择记忆类型和 private/team 作用域
  -> 主题文件与作用域索引落盘
  -> 下一轮按问题选择相关记忆
  -> 注入主 Agent 上下文
```

最终成果必须同时满足三个条件：

1. 可以通过真实多轮 CLI 演示“这一轮记住，下一轮用上”。
2. 可以从代码中清楚讲出提取、存储、召回和上下文注入的边界。
3. 可以通过单元测试证明核心行为，不依赖现场模型输出碰运气。

## 设计取舍

采用“真实模型决策 + 本地 Markdown 存储”的方案。

- 不使用硬编码规则替代模型分类，因为面试重点之一是模型负责语义决策。
- 不引入 SQLite、向量库或 embedding，因为 Claude Code 这条链路的核心是可读文件、摘要索引和模型选择。
- 不实现远程团队同步服务，因为它不影响本地自动提取和下一轮召回闭环。
- 不让模型直接拼接任意文件路径；模型选择语义字段，存储层负责生成并校验安全路径。

## 范围

### 实现范围

1. 四类记忆：`user`、`feedback`、`project`、`reference`。
2. 两种作用域：`private`、`team`。
3. 模型同时选择记忆类型、作用域、主题、摘要和正文。
4. 每个作用域维护主题 Markdown 文件和自己的 `MEMORY.md`。
5. 每轮只处理尚未提取过的 user/assistant 消息。
6. 下一轮先读取两个索引，再由召回模型选择最多 5 个相关主题文件。
7. 读取选中内容并作为独立 memory context 注入主 Agent。
8. 路径越界防护、team 写入敏感信息防护和原子文件写入。
9. 一个真实多轮 CLI 和一组不联网的确定性测试。

### 不实现范围

- HTTP 团队记忆服务、上传下载、ETag 和冲突合并。
- 文件 watcher、跨进程实时同步和多端一致性。
- Claude Code 的所有精确容量阈值、限流、遥测和实验开关。
- SQLite、embedding、向量召回和层级晋升状态机。
- 会话压缩、autoDream、遗忘曲线或现有 `hierarchical_v1` 的 retention 逻辑。
- 编码 Agent、工具权限系统和完整终端 UI。

这些能力可以后续扩展，但不得进入本次核心实现。

## 包结构

```text
memory/claude_auto_memory/
├── types.go             # 类型、作用域、消息和模型边界数据结构
├── prompts.go           # 中文提取与召回 prompt
├── store.go             # 主题文件、双索引和原子写入
├── frontmatter.go       # Markdown frontmatter 编解码
├── security.go          # 路径和 team 敏感信息保护
├── extractor.go         # 增量消息提取与模型调用
├── recall.go            # 索引扫描、候选选择和正文读取
├── agent.go             # Recall -> Main Agent -> Extract 主流程编排
├── README.md            # 运行方式、源码映射和面试讲法
└── *_test.go

cmd/claude-auto-memory-agent/
├── main.go              # .env、真实模型和多轮交互入口
└── main_test.go         # 命令装配与假模型闭环测试
```

文件可以在实施时按实际代码量合并，但提取、存储、召回和主流程不得堆进同一个大文件。

## 文件存储模型

根目录由调用方配置。默认演示目录为仓库根目录下的 `.claude-auto-memory/`：

```text
.claude-auto-memory/
├── MEMORY.md
├── chinese-comment-preference.md
└── team/
    ├── MEMORY.md
    └── tool-schema-convention.md
```

- 根目录中的主题文件和 `MEMORY.md` 属于 `private`。
- `team/` 中的主题文件和 `MEMORY.md` 属于 `team`。
- 主题文件名由存储层根据模型提供的 topic 生成 slug；模型不能传绝对路径或 `../`。
- 同一 `scope + topic` 再次写入时更新原文件，不产生重复主题。

主题文件格式：

```markdown
---
name: chinese-comment-preference
description: 用户希望新增代码使用中文用途注释
type: user
---

用户偏好新增函数和结构体前使用中文用途注释。
```

索引格式：

```markdown
# Memory Index

- [chinese-comment-preference](chinese-comment-preference.md) | user | 用户希望新增代码使用中文用途注释
```

`Store.Upsert` 是唯一写入口。它在一次受控操作中更新主题文件和对应 `MEMORY.md`，避免模型只写正文而忘记索引。索引按主题名稳定排序，便于阅读、测试和 Git diff。

## 记忆分类与作用域

提取模型必须从封闭集合中选择类型：

| 类型 | 含义 | 常见作用域 |
|---|---|---|
| `user` | 用户个人偏好、背景和长期工作习惯 | 必须 `private` |
| `feedback` | 用户对回答或协作方式的纠正 | 默认 `private`，明确为团队规范时可 `team` |
| `project` | 项目约定、架构决策和稳定事实 | 通常 `team`，个人草稿可 `private` |
| `reference` | 后续可能复用的外部资料和说明 | 通常 `team` |

模型通过结构化结果选择 `type` 和 `scope`。工程代码只校验选择是否合法，不实现 `type -> scope` 的固定 switch；唯一强约束是 `user` 不能写入 `team`。

以下内容不进入自动记忆：

- 可以直接从代码、Git 或项目文档重新得到的普通事实。
- 临时调试状态、一次性任务进度和短期中间结果。
- 密码、token、私钥、Cookie 或其他凭据。
- 没有稳定复用价值的闲聊和重复内容。

一次对话可以提取零条或多条记忆。模型返回空列表是合法结果。

## 核心运行链路

### 1. 回忆阶段

`Agent.RunTurn` 收到用户问题后，先调用 `Recaller.Recall`：

1. 读取 private 和 team 的 `MEMORY.md`；不存在时视为空索引。
2. 把用户当前问题和两个索引交给召回模型。
3. 召回模型返回最多 5 个相对主题路径。
4. 存储层校验路径必须属于对应作用域并且确实存在于索引中。
5. 读取合法主题正文，拼成带来源标记的 memory context。
6. 将 memory context 作为独立系统上下文传给主 Agent。

没有相关记忆时不注入空占位文本。召回模型不能直接读取任意项目文件。

### 2. 主回答阶段

主 Agent 只负责回答用户问题。它接收：

- 固定系统 prompt；
- 本轮召回到的 memory context；
- 当前会话消息。

记忆提取 prompt、候选索引和存储工具结果不得进入主 Agent 历史，避免记忆维护污染业务推理。

### 3. 提取阶段

主回答生成并交给调用方之后，`Extractor.Extract` 使用独立模型调用处理新增消息：

1. 根据内存游标截取尚未处理的 user/assistant 消息。
2. 结合提取规则生成零条或多条结构化候选。
3. 逐条校验类型、作用域和必填字段。
4. 对 team 候选执行敏感信息检查。
5. 通过 `Store.Upsert` 更新主题文件和对应索引。
6. 只有整个批次处理结束后才推进消息游标。

CLI 会先展示主回答，再等待本轮提取完成，最后接受下一轮输入。这样保留“回答后独立提取、不污染主推理”的语义，同时保证演示确定性。将来如需降低响应尾延迟，可以在编排层把同一个 `Extractor` 放到后台队列，不改变存储和召回接口。

## 模型边界

为保证测试稳定，模块不直接依赖具体 OpenAI 客户端，而是定义三个小接口：

```go
type MemoryExtractor interface {
    Extract(ctx context.Context, messages []ConversationMessage) ([]MemoryCandidate, error)
}

type MemorySelector interface {
    Select(ctx context.Context, query string, manifest MemoryManifest) ([]MemoryRef, error)
}

type ChatAgent interface {
    Generate(ctx context.Context, messages []ConversationMessage, memoryContext string) (string, error)
}
```

生产实现使用仓库现有 Eino/OpenAI-compatible 模型装配方式。测试使用 fake 实现精确控制分类、作用域、召回结果和回答内容。

模型可见 prompt 使用中文，并明确说明：

- 只提取稳定、未来有用的信息。
- 类型和作用域都由模型根据语义选择。
- `user` 永远是 private。
- 不保存可从代码或 Git 重建的事实。
- 不保存敏感信息。
- 召回最多选择 5 项，只选对当前问题真正有帮助的主题。

## 安全与错误处理

### 路径边界

- 所有路径经过 `filepath.Clean` 和根目录包含关系校验。
- 拒绝绝对路径、`..`、符号链接逃逸和保留文件名 `MEMORY.md` 作为 topic。
- 召回只接受当前 manifest 中出现的引用，防止模型构造任意读路径。

### 敏感信息

team 写入前检查常见凭据形态，包括私钥头、Bearer token、常见 API key/secret 赋值。命中时拒绝该条 team 记忆并返回可观察错误；private 仍不应保存凭据，提取 prompt 会先行禁止。

### 失败降级

- 索引不存在：按空记忆继续。
- 召回模型失败或返回非法引用：记录错误并让主 Agent 在无记忆状态下继续。
- 主 Agent 失败：不执行提取，也不推进游标。
- 单条候选校验失败：跳过该条并收集错误，其他合法候选继续写入。
- 文件写入失败：不更新索引；使用临时文件加 rename，避免留下半写文件。
- 提取模型失败：已经生成的主回答仍然有效，并向 CLI 输出记忆维护警告。

## CLI 演示

命令：

```bash
go run ./cmd/claude-auto-memory-agent
```

配置优先读取仓库根目录 `.env`，沿用现有模型 endpoint、API key 和 model 名称约定。命令支持 `--prepare-only`，用于在不请求模型的情况下验证配置和存储目录。

推荐三轮演示：

1. 用户：“请记住，我个人更喜欢新增代码写中文用途注释。”
   - 预期生成 `user + private` 主题。
2. 用户：“团队约定：所有新工具参数都要提供 jsonschema_description。”
   - 预期生成 `project + team` 主题。
3. 用户：“以后在这个项目里新增工具要注意什么？”
   - 预期召回前两轮相关主题，回答同时体现个人偏好和团队约定。

CLI 每轮额外打印简短 trace：召回了哪些主题、提取写入了哪些主题、写入哪个作用域。不得打印 API key、完整系统 prompt 或无关业务 payload。

## 测试策略

### 存储测试

- private 和 team 分别写入正确目录并更新各自索引。
- 同主题 upsert 不产生重复索引项。
- frontmatter 编解码保持类型、名称和摘要。
- 拒绝路径逃逸、保留名和 team 敏感信息。
- 写入失败不会产生指向不存在主题的索引。

### 提取测试

- 模型可以分别选择四种类型和两种作用域。
- 工程层没有把 `project` 强制映射到 team。
- `user + team` 被拒绝。
- 只把新增消息交给提取模型，成功后推进游标。
- 提取失败不推进游标，下一次可以重试。
- 零候选不会创建空主题文件。

### 召回测试

- 同时向选择器提供 private/team 索引。
- 最多读取 5 个合法引用。
- 忽略不存在、越界、重复或未出现在 manifest 中的路径。
- 无索引和选择器失败时主 Agent 仍可回答。
- 注入内容带作用域和来源，便于回答引用及调试。

### 主链路测试

- 顺序严格为 `Recall -> Main Agent -> Extract`。
- 本轮刚提取的记忆不会倒灌当前回答，但能在下一轮召回。
- 主回答失败时不触发提取。
- 提取失败不覆盖已经生成的主回答。
- 使用 fake 模型跑通三轮面试场景，不访问网络。

### 验证命令

```bash
env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache \
  go test ./memory/claude_auto_memory ./cmd/claude-auto-memory-agent -count=1

env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache \
  go test ./... -count=1

go run ./cmd/claude-auto-memory-agent --prepare-only
```

在 `.env` 提供有效模型配置时，再运行一次真实三轮 CLI 并保存简短输出作为验收证据。

## 验收标准

1. 新模块不依赖 `hierarchical_v1`，两套记忆语义互不混用。
2. 模型真实负责 `type + scope` 语义选择，工程层负责合法性和安全边界。
3. private/team 各自拥有主题文件和 `MEMORY.md`，内容可人工阅读和 Git diff。
4. 第二轮及之后可以召回前序轮次生成的相关记忆并注入主 Agent。
5. 单元测试能够确定性证明增量提取、双索引、最多 5 条召回和下一轮生效。
6. CLI 能使用真实模型完成推荐三轮演示，trace 不泄露敏感数据。
7. README 能把 Go 组件映射回 Claude Code 的“提取、分类、双作用域、索引、召回、注入”六个面试要点。

## 面试表达主线

这套实现重点不是“保存聊天记录”，而是把长期记忆拆成两个独立决策：

1. 写入决策：什么值得长期保存，属于哪一类、哪个作用域。
2. 读取决策：当前问题真正需要哪些主题，而不是把全部历史塞回上下文。

Markdown 主题文件负责可读的长期事实，`MEMORY.md` 负责低成本候选摘要，模型负责语义判断，存储层负责确定性的路径、安全和一致性。主 Agent、提取器和召回器互相隔离，因此记忆维护不会污染主回答，将来可以独立替换模型、存储或调度方式。
