# Claude Code 风格自动记忆与任务跟踪核心链路

这个包复刻 Claude Code Auto Memory 与 Session Memory 最适合面试讲解的核心闭环，不复用 `hierarchical_v1` 的 SQLite、五层状态和 retention 语义。

```text
当前问题
  -> 追加完整 transcript.jsonl
  -> Session Memory 按阈值后台更新 summary.md
  -> Context 超阈值时压缩为 summary + 最近消息
  -> 读取 private/team MEMORY.md
  -> 选择最多 5 个相关主题
  -> 注入 Task Agent
  -> TaskList / TaskCreate / TaskGet / TaskUpdate
       └-> 更新 JSON Task List
  -> 生成主回答和工具调用计数
  -> 立即返回/打印回答
       └-> 后台单实例提取新增对话
  -> 模型选择 type + scope
  -> 写主题 Markdown 并更新对应 MEMORY.md
  -> 下一轮生效
```

两个记忆系统并行工作：Auto Memory 保存跨会话仍有价值的稳定知识；Session Memory 保存当前任务现场，供 Compact 和 Resume 使用。Session Summary 不会自动晋升为长期记忆。命令层另外装配独立的 JSON Task List，负责可机械读写的进度状态。

## 三层独立状态

| 状态层 | 保存内容 | 事实边界 |
|---|---|---|
| Transcript / Session Summary | 完整会话，以及为 Compact、Resume 派生的当前会话叙述摘要 | Transcript 保存真实对话；Session Summary 只是恢复上下文 |
| Markdown Auto Memory | 跨会话仍稳定的用户偏好、反馈、项目知识和参考信息 | 主题 Markdown 与 `MEMORY.md` 索引是长期知识边界 |
| JSON Task List | `pending`、`in_progress`、`completed`、负责人和任务依赖 | 任务 JSON 是机械进度真相，可由四个 Task 工具精确读写 |

`sessions/<session-id>/session-memory/summary.md` 不是 Task 真相。摘要或最终回答可以概括进度，但不能反向创建、覆盖或完成任务；任务状态只以 `tasks/<task-list-id>/` 下的 JSON 文件为准。

## 快速运行

只检查目录和索引，不调用模型：

```bash
go run ./cmd/claude-auto-memory-agent \
  -prepare-only \
  -memory-dir /private/tmp/claude-auto-memory-prepare \
  -session-id interview
```

使用仓库 `.env` 中的 OpenAI-compatible 配置运行默认三轮场景：

```bash
go run ./cmd/claude-auto-memory-agent \
  -memory-dir /private/tmp/claude-auto-memory-e2e \
  -session-id interview
```

在 linked worktree 中没有 `.env` 时，显式指向主仓库文件：

```bash
go run ./cmd/claude-auto-memory-agent \
  -env-file ../../.env \
  -memory-dir /private/tmp/claude-auto-memory-e2e \
  -session-id interview
```

为了在少量轮次中稳定展示 Session Summary 和 Compact，可以降低演示阈值：

```bash
go run ./cmd/claude-auto-memory-agent \
  -memory-dir /private/tmp/claude-auto-memory-session-demo \
  -session-id interview \
  -session-init-tokens 20 \
  -session-update-tokens 20 \
  -compact-tokens 80 \
  -session-recent-messages 2
```

使用相同 Session 继续任务：

```bash
go run ./cmd/claude-auto-memory-agent \
  -memory-dir /private/tmp/claude-auto-memory-session-demo \
  -session-id interview \
  -resume
```

`-resume` 必须复用相同的 `-memory-dir` 和 `-session-id`。默认 `task-list-id` 就是 `session-id`，因此恢复时 Task Agent 直接读取原 Task List，不根据 Transcript 或 `summary.md` 重放、推断任务状态。

默认输入位于 `examples/interview_rounds.txt`，也可以用 `-rounds-file` 替换。文件支持 JSON 字符串数组，或用单独一行 `---` 分隔多轮消息。

### 运行时语义

- `Runner.RunTurn` 对应交互主链路：生成回答后只向两个调度器提交快照，不等待模型和文件写入。
- `ExtractionScheduler` 同时最多运行一个任务；繁忙时反复提交只保留最新会话快照，当前任务完成后执行一次 trailing extraction。
- `SessionScheduler` 独立维护 `summary.md`，同样采用单实例、latest-wins 和 trailing update。
- `SessionCompactor` 只裁剪 Context View；完整 Transcript 永远只追加，因此两个后台系统不会因 Compact 丢失事实输入。
- `taskChatAgent` 把 Compact 后的 Context View 和 Auto Memory 召回结果交给 `claude_tasklist.Agent`；Task Agent 通过真实工具循环读写独立 Task Store，不在 `claude_auto_memory` 包内维护第二份任务状态。
- Task Agent 返回本轮工具调用数量；`Runner` 把它写入 assistant Transcript，供 Session Memory 的工具调用阈值统计使用。工具参数和结果不写进长期 Auto Memory topic。
- `Runner.Drain` 只等待 Auto Memory，`Runner.WaitSession` 只等待 Session Memory，避免混淆生命周期。
- 默认 CLI 是脚本模式：每轮先把 assistant 回答写到 stdout，再等待两个后台系统，保证下一轮演示稳定。这个等待不属于交互主回答延迟。

每轮低敏 trace 包含：`recalled`（召回条目）、`compacted`（本轮是否使用压缩上下文）、`tasks=pending:...,in_progress:...,completed:...`（Task Store 状态计数）、`task_tool_calls`（本轮 Task 工具调用数）、`written`（长期记忆写入）和 `session_updated` / `summarized_through`（Session Summary 进度）。异常以 `memory_warning` 输出；trace 不打印任务描述、工具参数或长期记忆正文。

## 落盘结构

```text
<memory-root>/
├── MEMORY.md                  # private 候选摘要
├── comment-style.md           # private 主题正文
├── team/
    ├── MEMORY.md              # team 候选摘要
    └── tool-schema.md         # team 主题正文
├── sessions/
    └── <session-id>/
        ├── transcript.jsonl   # 完整、只追加的真实会话
        └── session-memory/
            ├── summary.md     # 当前会话的结构化恢复摘要
            └── state.json     # UUID 摘要边界和 token 水位
└── tasks/
    └── <task-list-id>/        # 默认等于 session-id
        ├── .highwatermark     # 单调任务 ID 水位，删除后也不回退
        ├── 1.json             # 单个任务的完整机械状态
        └── <id>.json
```

主题文件使用 JSON frontmatter 加 Markdown 正文。JSON 是合法 YAML 子集，既可人工阅读，也能用 Go 标准库无损解析。

Task Store 为每个任务保存独立 JSON 文件。`.highwatermark` 与 `<id>.json` 都使用同目录临时文件加 rename 原子替换，避免暴露单文件半写内容。

## Task 工具与 CLI 主调用链

四个模型工具共享同一个 Task Store：

- `TaskCreate`：创建 `pending` 任务，写入标题、描述、进行中文案和可选 metadata。
- `TaskGet`：按 ID 读取完整任务，包括描述、负责人、依赖和 metadata。
- `TaskUpdate`：更新字段、状态、负责人或双向依赖；`status=deleted` 删除任务并清理依赖引用。
- `TaskList`：按数字 ID 返回低成本摘要，并只展示尚未完成的 blocker。

CLI 的实际组合链路是：

```text
cmd/claude-auto-memory-agent.runAgent
  -> 创建 Auto Memory / Transcript / Session / Task Store
  -> buildRunner 创建 claude_tasklist.Agent
  -> taskChatAgent 适配 claude_auto_memory.ChatAgent
  -> claude_auto_memory.Runner.RunTurn
       -> Compact 后的 Context View + Auto Memory Recall
       -> Task Agent ReAct 工具循环
       -> Task JSON 落盘
       -> assistant 正文 + tool_call_count 写入 Transcript
       -> 调度 Session Summary 与 Auto Memory 后台更新
```

`-prepare-only` 也会初始化 Task Store，并输出 `tasks_dir`；普通模式每轮从同一 Store 读取任务计数。`memory/claude_auto_memory` 仍只负责对话、召回、摘要、Compact 和调度，Task 类型、工具和文件一致性由独立的 `memory/claude_tasklist` 包负责。

## 八个面试要点

1. **回答后异步提取**：`Runner` 生成回答后只 fire-and-forget 提交快照；`ExtractionScheduler` 在后台调用独立 `Extractor`，提取模型卡住也不会阻塞主回答。
2. **模型负责语义决策**：提取模型同时选择 `user/feedback/project/reference` 和 `private/team`。工程层不写 `type -> scope` 固定映射，只强制个人 `user` 记忆不能进入 team。
3. **主题正文加低成本索引**：完整内容放主题文件，`MEMORY.md` 只保留 topic、type 和 description。召回前无需把所有正文塞给模型。
4. **两阶段召回**：模型先从两个索引选择最多 5 个引用，存储层再验证引用确实存在于 manifest 后读取正文。
5. **上下文隔离**：主 Agent、长期提取器、召回选择器和 Session Summarizer 是四个小接口。生产环境可以共用一个底层模型，但 Prompt 和会话消息互不串线。
6. **确定性安全边界**：路径、slug、符号链接、索引成员和 team 凭据由工程代码校验；模型只做适合它的语义判断。
7. **Transcript 与 Context 分离**：Transcript 是完整事实源；Context 可以变成 Session Summary 加最近消息，Compact 不会破坏 Auto Memory UUID 游标。
8. **可恢复 Session**：Session Summary 更新成功后才推进 `last_summarized_message_id`；相同 `session-id -resume` 会恢复 Transcript 和摘要上下文。

## 代码职责

| Go 文件 | 责任 |
|---|---|
| `types.go` | 封闭类型、作用域和模型接口 |
| `store.go` | 双目录、主题 upsert、双索引和受控读取 |
| `frontmatter.go` | Markdown topic/index 编解码 |
| `security.go` | 路径、符号链接和 team secret guard |
| `extractor.go` | 新消息游标和回答后提取 |
| `scheduler.go` | fire-and-forget、单实例、latest-wins 合并和 Drain |
| `recall.go` | manifest 选择、去重、最多 5 条正文 |
| `runner.go` | `Recall -> Main Agent -> Schedule` 主链路 |
| `prompts.go` / `llm.go` | 中文契约和 Eino 模型适配 |
| `transcript_store.go` | UUID 消息的 JSONL 追加与恢复 |
| `session_store.go` / `session_prompts.go` | 固定摘要模板、状态和中文更新契约 |
| `session_memory.go` / `session_scheduler.go` | 阈值更新、单实例和 latest-wins |
| `session_compactor.go` | 等待摘要、验证 UUID 边界并裁剪 Context |

任务跟踪的跨包职责：

| Go 文件或目录 | 责任 |
|---|---|
| `memory/claude_tasklist/store*.go` | JSON 任务、单调 ID、依赖校验和进程内互斥 |
| `memory/claude_tasklist/tools.go` | `TaskCreate`、`TaskGet`、`TaskUpdate`、`TaskList` 模型工具 |
| `memory/claude_tasklist/agent.go` / `prompts.go` | Eino Task Agent 工具循环和中文进度规则 |
| `cmd/claude-auto-memory-agent/task_chat.go` | Context View、长期记忆与 Task Agent 的薄适配 |
| `cmd/claude-auto-memory-agent/main.go` | 四种 Store 的装配、Resume 和每轮低敏 trace |

## Claude Code 源码概念映射

本实现根据本地 `claude-code-source-study` 中以下链路提炼：

| Claude Code 概念 | 本实现 |
|---|---|
| `src/services/extractMemories/extractMemories.ts` | `extractor.go`、`scheduler.go`、`runner.go` |
| `src/services/extractMemories/prompts.ts` | `prompts.go` |
| `src/memdir/memoryTypes.ts` | `types.go` |
| `src/memdir/paths.ts`、`teamMemPaths.ts` | `store.go` |
| `src/memdir/memoryScan.ts` | `frontmatter.go`、`store.go` |
| `src/memdir/findRelevantMemories.ts` | `recall.go`、`llm.go` |
| memory attachment/injection | `LLMChatAgent.Generate` |
| `src/services/SessionMemory/sessionMemory.ts` | `session_memory.go`、`session_scheduler.go` |
| `src/services/compact/sessionMemoryCompact.ts` | `session_compactor.go` |

这里复刻的是运行语义，不是 TypeScript 文件结构逐行翻译。

## 与 `hierarchical_v1` 的区别

| 维度 | 本模块 | `hierarchical_v1` |
|---|---|---|
| 存储 | 可读 Markdown | SQLite |
| 分类 | 模型选择四类和两作用域 | 工程定义五层和写入策略 |
| 召回 | 索引摘要 + 模型选最多 5 条 | 按层、预算和状态组装 |
| 生命周期 | Auto/Session 双后台、Context Compact、Resume | retention、evidence、promotion |
| 目标 | Claude Code 核心链路面试演示 | 通用层级记忆实验 |

## 测试

```bash
env GOCACHE=/private/tmp/ai-designing-claude-auto-memory-gocache \
  go test ./memory/claude_auto_memory ./cmd/claude-auto-memory-agent -count=1
```

测试使用 fake 模型确定性证明双索引、UUID 增量游标、五条上限、非法引用降级、本轮写入下一轮生效、两个后台系统不阻塞回答、忙时只保留最新快照、Compact 不修改 Transcript、Resume 不重复抽取历史消息，以及命令确实装配四个隔离角色。普通 `go test` 不访问网络。

## 明确不做

- HTTP team sync、ETag、watcher 和跨设备一致性。
- SQLite、embedding、向量库、遗忘曲线和层级晋升。
- Redis/数据库 Checkpoint、多进程同时写同一 Session、完整遥测和终端 UI。
- Claude Code 的远程特性开关和逐 token 精确 tokenizer；MVP 使用稳定 rune 估算。
- 跨进程文件锁和 team task coordination：当前互斥只覆盖同一进程内指向同一 Task List 的 Store；`owner` 只是数据字段，不包含 teammate 抢占、自动领取或 TeamCreate/TeamDelete 生命周期。
- 多 JSON crash transaction：单个 `.highwatermark` 或任务文件会原子替换，但一次依赖更新可能顺序写多个 JSON；进程在批次中途崩溃时没有日志、回滚或事务恢复保证。

这些外围能力不会改变核心面试主线：模型做语义选择，文件系统做确定性边界，索引降低召回成本，三种状态层保持独立。
