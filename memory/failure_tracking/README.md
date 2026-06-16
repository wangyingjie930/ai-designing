# Failure Tracking

这个目录把 Python `FailureJournal` 翻译成 Go + Eino ADK 版本，并作为正常业务 Agent 的工具能力使用。

核心对应关系：

- `FailureEntry` -> [types.go](types.go)
- `FailureJournal.record` -> [journal.go](journal.go) 的 `Record`
- `FailureJournal.consult` -> [journal.go](journal.go) 的 `Consult`
- `llm.generate(...)` -> [generator.go](generator.go) 的 `ModelLessonGenerator`
- `vector_db.upsert/search` -> [sqlite_store.go](sqlite_store.go) 的 SQLite + `embedding_json`
- ADK tools / Agent -> [agent.go](agent.go)

业务复现场景是酒店运营 Agent 的两轮自然语言消息：

1. 第一轮是一条已解决的值班复盘。消息只以自然语言描述失败现象、原因和修复动作；Agent 必须先调用 `failure_tracking_search`，因为没有相似经验，再自主调用 `failure_tracking_record` 沉淀经验。
2. 第二轮是一条新的现场求助。消息不包含最终修复动作；Agent 必须先调用 `failure_tracking_search`，从第一轮沉淀的 journal 里召回经验，再给处置建议。

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
