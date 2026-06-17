# Hierarchical Memory

`memory/hierarchical` 是把 Python `HierarchicalMemory` 翻译成 Go 的三层记忆实现：

- `working`: 当前上下文窗口，受 `working_budget` 控制。
- `session`: 从 working 淘汰下来的会话缓冲。
- `longterm`: SQLite 持久化的真实 embedding 向量库。

运行时不提供 hash/fake embedding fallback。`consolidate` 和 `retrieve` 都会调用真实 embedding 服务。

## Agent 场景

非 coding 场景是旅行/家庭行程规划。Agent 会先从 SQLite 召回长期偏好，再查看可见 working memory，然后根据用户的新消息写入重要约束并按需 consolidation。`session` 是内部淘汰缓冲，CLI 会打印给人看，但不会通过 ADK tool 返回给 LLM。

GoLand 直接点 Run、不带任何参数时，会使用 `output/hierarchical-memory-agent.sqlite`、`examples/travel_rounds.txt` 的三轮消息，以及较小的 demo working budget，方便看到 working -> session -> long-term 的迁移：

```bash
go run ./cmd/hierarchical-memory-agent
```

```bash
go run ./cmd/hierarchical-memory-agent \
  -db /private/tmp/hierarchical-travel.sqlite \
  -scope family-trip-2026 \
  -message-file memory/hierarchical/examples/travel_goal.txt
```

多轮观察记忆产生和变迁：

```bash
go run ./cmd/hierarchical-memory-agent \
  -db /private/tmp/hierarchical-travel.sqlite \
  -scope family-trip-2026 \
  -rounds-file memory/hierarchical/examples/travel_rounds.txt
```

`-rounds-file` 可以写 JSON 字符串数组，也可以用单独一行 `---` 分隔多轮自然语言消息。主流程就是同一个 Agent 和同一个 SQLite memory 上的循环：

```text
for 每一轮消息:
    agent.Query(message)
    打印当前 working/session/long-term
```

## 必需环境变量

LLM 走 OpenAI-compatible ChatModel：

```bash
OPENAI_API_KEY=...
LLM_MODEL=...
LLM_OPENAI_BASE_URL=...
```

Embedding 走真实 HTTP 服务，兼容 OpenAI embeddings 和 Gemini embedContent：

```bash
EMBEDDING_API_KEY=...
EMBEDDING_MODEL=...
EMBEDDING_BASE_URL=...
EMBEDDING_ENDPOINT_PATH=embeddings
EMBEDDING_DIM=768
```

Gemini 风格可使用：

```bash
EMBEDDING_MODEL=google:text-embedding-004
EMBEDDING_BASE_URL=https://generativelanguage.googleapis.com/v1beta
```

## 只验证记忆层

`-prepare-only` 不调用 LLM，但只要执行 `-retrieve` 或 `-consolidate`，仍会调用真实 embedding。

```bash
go run ./cmd/hierarchical-memory-agent \
  -prepare-only \
  -db /private/tmp/hierarchical-travel.sqlite \
  -scope family-trip-2026 \
  -add "孩子晕船，海岛行程要避免长时间坐船。" \
  -add-source user \
  -importance 0.8 \
  -consolidate
```

```bash
go run ./cmd/hierarchical-memory-agent \
  -prepare-only \
  -db /private/tmp/hierarchical-travel.sqlite \
  -scope family-trip-2026 \
  -retrieve "暑假家庭旅行交通限制" \
  -k 5
```
