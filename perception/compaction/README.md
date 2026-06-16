# 长会话客服 Agent 语义压缩

这个包把语义压缩模式落到客服工单场景里：

- Level 1: 对非错误的长工具观察做 ObservationMasking，保留 Action 路径和可回查 handle。
- Level 2: 把旧历史增量合并进五槽位 Anchor：intent、changes、decisions、excluded approaches、next steps。
- Level 3: 极限压缩时导出 handoff summary，把它视为交接信号，而不是普通继续压缩。

客服增强点：

- `SupportContext` 按 `severity` 和 `sla_deadline` 动态下调触发阈值。
- `timeout`、`HTTP 5xx`、`Traceback`、`FAILED` 等错误信号会自动升级为受保护 Turn。
- `CompactionEvent` 记录压缩级别、压缩比、错误保留/表示数量和是否建议交接。

Eino ADK 入口：

- `NewSupportAgent` 内部创建 `adk.NewChatModelAgent` 和 `adk.NewRunner`。
- 每轮 `Query` 先经过 `TokenLimitCompactionMiddleware` 测量 token 压力；只有超过动态阈值时才调用 `SupportCompactor` 生成压缩上下文。
- Runner 事件里的工具调用会作为 `Action` 写回 session，工具结果会作为 `Observation` 写回 session。

## 运行 Demo

项目根目录的 `.env` 需要包含 OpenAI-compatible 配置：

```text
OPENAI_API_KEY=...
LLM_MODEL=gpt-5.5
LLM_OPENAI_BASE_URL=http://localhost:8317

# 可选：扣子罗盘链路追踪
COZELOOP_WORKSPACE_ID=your_workspace_id
COZELOOP_API_TOKEN=your_token
# COZELOOP_API_BASE_URL=https://api.coze.cn
```

命令行运行：

```bash
env CMD_E2E=1 GOCACHE=/private/tmp/ai-designing-gocache go test ./cmd/support-agent-demo -run TestSupportAgentDemoEndToEnd -count=1 -v
```

如果 `.env` 中没有配置扣子罗盘 workspace/token，demo 会保持 no-op，只打印 `cozeloop=disabled`：

```bash
env CMD_E2E=1 GOCACHE=/private/tmp/ai-designing-gocache go test ./cmd/support-agent-demo -run TestSupportAgentDemoEndToEnd -count=1 -v
```

GoLand 运行：

- 打开 Run Configurations。
- 新建或选择 Go Test 配置，测试函数指向 `TestSupportAgentDemoEndToEnd`。
- 环境变量设置 `CMD_E2E=1`，配置会自动从项目根目录 `.env` 读取 API key。

这个 demo 会注入一段长客服历史，稳定触发 compaction，并打印：

- `TokenPressure`
- `CompactionEvent`
- `HandoffSummary`
- Agent 最终回复

实现上 demo 只初始化一个 OpenAI-compatible ChatModel。压缩触发和 Anchor 更新由 `perception/compaction` 内部的 token-limit middleware 与 compactor 负责，避免把压缩实现细节泄漏到 `cmd` 层。

## 通用扣子罗盘接入

`observability/cozeloop` 只做一层薄封装：读取 `.env` 加载后的 `COZELOOP_*` 环境变量，创建官方 `github.com/coze-dev/cozeloop-go` 客户端，并用 `github.com/cloudwego/eino-ext/callbacks/cozeloop` 注册 Eino 全局 callback。

平台级全局接入：

```go
config, shutdown, err := cozeloopobs.InstallFromEnv(ctx)
defer shutdown(ctx)
```

配置项：

- `COZELOOP_WORKSPACE_ID`: 扣子罗盘 workspace id。
- `COZELOOP_API_TOKEN`: 扣子个人访问 token，本地 demo 默认用它接入。
- `COZELOOP_API_BASE_URL`: 可选，默认使用 `https://api.coze.cn`。
- `COZELOOP_ENABLED`: 可选；不设置时，workspace/token 同时存在会自动启用。

接入后不需要包一层业务 Agent wrapper。Eino 的 ChatModel、Tool、Retriever、Prompt、Graph、ADK Agent 等组件会通过官方 callback 自动上传到扣子罗盘；未设置 workspace/token 时接入自动 no-op，demo 仍可正常运行。
