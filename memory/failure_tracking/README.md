# Failure Tracking

这个目录实现《失败日记：让 Agent 把摔过的跤变成本事》里的 Failure Journals 模式，并把它作为 Eino ADK Agent 的工具能力使用。

核心对应关系：

- 失败边界 `Boundary`：`hard_failure` / `gate_failure` / `semantic_failure` / `safety_failure`
- 失败分类 `Category`：`tool_failure`、`mechanical_state_mismatch`、`boundary_leak` 等可召回类别
- 证据包 `EvidenceBundle`：`workspace_refs`、`narrative_refs`、`state_refs`、`observation_refs`
- 根因与补救：`symptom`、`root_cause`、`repair`、`lesson`、`do_not`
- 召回触发器 `RecallTrigger`：`task_families`、`tools`、`mechanical_keys`、`categories`
- 留存与审查 `Status`：`draft`、`needs_review`、`approved`、`archived`

实现要点：

- `FailureJournal.Record` 写入完整六层结构，不能只写一段错误故事。
- `FailureJournal.RecallBeforeTool` 只召回 `approved` 条目。
- 召回排序优先使用结构化键：`task_family + tool + mechanical_keys + categories`；embedding/关键词只做辅助排序。
- ADK 工具通过 Eino `InferTool` 从 Go struct tags 生成 JSON Schema，`required`、`enum` 和字段说明都写在 `types.go`。
- SQLite 仍是唯一持久化路径，完整条目保存到 `entry_json`，向量保存到 `embedding_json`。
- `recalled_count` 会在每次命中后递增，方便后续统计召回有效率、漏召回率等指标。

业务复现场景是酒店运营 Agent 的两轮自然语言消息：

1. 第一轮是一条已解决且已由值班经理审查的复盘。Agent 必须先调用 `failure_tracking_search`，没有相似经验时再调用 `failure_tracking_record` 写入 approved 失败日记。
2. 第二轮是一条新的现场求助。Agent 必须先调用 `failure_tracking_search`，优先用真实任务族、失败类别和 query 召回上一轮经验；没有真实工具调用或 SessionState 绑定时，不允许编造 `tool` 或 `mechanical_keys`。

运行：

```bash
GOCACHE=/private/tmp/ai-designing-gocache go run ./cmd/failure-tracking-demo
```

端到端测试：

```bash
CMD_E2E=1 GOCACHE=/private/tmp/ai-designing-gocache go test ./cmd/failure-tracking-demo -run TestFailureTrackingAgentEndToEnd -count=1 -v
```

默认每次运行会使用临时空 SQLite，保证可以复现“第一轮新经验 -> 第二轮召回”。如果需要保留数据库，可设置环境变量：

```bash
FAILURE_TRACKING_DB=memory/failure_tracking/failure_tracking.sqlite go run ./cmd/failure-tracking-demo
```

Embedding 配置来自 `.env`：

```bash
EMBEDDING_MODEL=google:gemini-embedding-001
EMBEDDING_BASE_URL=https://generativelanguage.googleapis.com/v1beta
EMBEDDING_ENDPOINT_PATH=models/gemini-embedding-001:embedContent
EMBEDDING_DIM=1536
EMBEDDING_API_KEY=...
```

如果没有配置 embedding，代码会降级使用本地 `HashEmbedder`，便于普通单测不依赖外部服务。
