# Claude Code 风格自动记忆核心链路

这个包复刻 Claude Code Auto Memory 与 Session Memory 最适合面试讲解的核心闭环，不复用 `hierarchical_v1` 的 SQLite、五层状态和 retention 语义。

```text
当前问题
  -> 追加完整 transcript.jsonl
  -> Session Memory 按阈值后台更新 summary.md
  -> Context 超阈值时压缩为 summary + 最近消息
  -> 读取 private/team MEMORY.md
  -> 选择最多 5 个相关主题
  -> 注入主 Agent
  -> 生成回答
  -> 立即返回/打印回答
       └-> 后台单实例提取新增对话
  -> 模型选择 type + scope
  -> 写主题 Markdown 并更新对应 MEMORY.md
  -> 下一轮生效
```

两个记忆系统并行工作：Auto Memory 保存跨会话仍有价值的稳定知识；Session Memory 保存当前任务现场，供 Compact 和 Resume 使用。Session Summary 不会自动晋升为长期记忆。

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

默认输入位于 `examples/interview_rounds.txt`，也可以用 `-rounds-file` 替换。文件支持 JSON 字符串数组，或用单独一行 `---` 分隔多轮消息。

### 运行时语义

- `Runner.RunTurn` 对应交互主链路：生成回答后只向两个调度器提交快照，不等待模型和文件写入。
- `ExtractionScheduler` 同时最多运行一个任务；繁忙时反复提交只保留最新会话快照，当前任务完成后执行一次 trailing extraction。
- `SessionScheduler` 独立维护 `summary.md`，同样采用单实例、latest-wins 和 trailing update。
- `SessionCompactor` 只裁剪 Context View；完整 Transcript 永远只追加，因此两个后台系统不会因 Compact 丢失事实输入。
- `Runner.Drain` 只等待 Auto Memory，`Runner.WaitSession` 只等待 Session Memory，避免混淆生命周期。
- 默认 CLI 是脚本模式：每轮先把 assistant 回答写到 stdout，再等待两个后台系统，保证下一轮演示稳定。这个等待不属于交互主回答延迟。

## 落盘结构

```text
<memory-root>/
├── MEMORY.md                  # private 候选摘要
├── comment-style.md           # private 主题正文
├── team/
    ├── MEMORY.md              # team 候选摘要
    └── tool-schema.md         # team 主题正文
└── sessions/
    └── <session-id>/
        ├── transcript.jsonl   # 完整、只追加的真实会话
        └── session-memory/
            ├── summary.md     # 当前任务结构化摘要
            └── state.json     # UUID 摘要边界和 token 水位
```

主题文件使用 JSON frontmatter 加 Markdown 正文。JSON 是合法 YAML 子集，既可人工阅读，也能用 Go 标准库无损解析。

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
- Redis/数据库 Checkpoint、多进程同时写同一 Session、完整工具循环、遥测和终端 UI。
- Claude Code 的远程特性开关和逐 token 精确 tokenizer；MVP 使用稳定 rune 估算。

这些外围能力不会改变核心面试主线：模型做语义选择，文件系统做确定性边界，索引降低召回成本，三个阶段互相隔离。
