# hierarchical_v1

这是对你贴的 hierarchical memory 代码的 Go + Eino ADK 翻译版。

核心语义：

- 五层记忆：`policy`、`project`、`user`、`task`、`scratchpad`
- 每条记忆都是 `MemoryEntry`，包含 `source`、`evidence_refs`、`confidence`、`token_estimate` 和有效期
- 写入受 `LayerPolicy` 约束：`policy/project` 不允许 `agent_inference` 直接写，部分层需要证据
- `scratchpad` 可以保存未验证推断，确认后通过 `propose_from_scratchpad` 生成 `verified_trace` 候选
- `assemble_context` 按 `policy -> project -> user -> task -> scratchpad` 顺序、每层 token budget、confidence 和最近访问时间选择上下文
- 持久化默认走 SQLite：`policy/project/user/task` 会 upsert 到 SQLite，`scratchpad` 只保存在进程内
- trace 只在命令入口记录 `db_path`、`rounds`、`mode` 等摘要，不进入核心业务逻辑

文件分工：

- `types.go`：五层枚举、MemoryEntry、LayerPolicy、请求响应和 Agent 类型
- `retention.go`：pattern 翻译本体，包括 write、scratchpad promotion、assemble_context 和 health_report
- `sqlite_store.go`：非 scratchpad 层 SQLite 持久化
- `agent.go`：Eino ADK tools 和家庭膳食规划 Agent
- `examples/meal_rounds.txt`：非 coding 多轮场景输入

本地验证：

```bash
env GOCACHE=/private/tmp/ai-designing-gocache go test ./memory/hierarchical_v1
```

确定性命令验证：

```bash
env GOCACHE=/private/tmp/ai-designing-gocache go test ./cmd/hierarchical-v1-agent
```

确定性写入验证：

```bash
go run ./cmd/hierarchical-v1-agent -prepare-only \
  -db output/hierarchical-v1-memory.sqlite \
  -write-layer user \
  -write-source human \
  -write-key avoid \
  -write-value '{"text":"不吃香菜"}' \
  -token-estimate 5 \
  -read-key avoid
```

真实模型运行会读取 `.env` / 环境变量里的 `OPENAI_API_KEY`、`LLM_MODEL` 和可选 `LLM_OPENAI_BASE_URL`：

```bash
go run ./cmd/hierarchical-v1-agent
```
