# Guardrail Sandwich with Eino ADK

这个目录把 Python 版 `GuardrailSandwich` 落到 Go + Eino ADK：

- **Input filter**：`Middleware.BeforeModelRewriteState` 会在模型调用前过滤 `ToolInfos`，模型只能看到策略允许的工具；`WrapInvokableToolCall` / `WrapEnhancedInvokableToolCall` 会在工具真正执行前再次校验工具名、参数、风险等级和人工审批。
- **Sandbox**：这里的 sandbox 不是 OS 沙箱，而是“工具必须经过 guarded endpoint 才能执行”。高风险动作默认不会直接触达外部系统。
- **Output filter**：工具输出会先脱敏再进入下一轮模型上下文；模型回复在 `AfterModelRewriteState` / `AfterAgent` 再做兜底脱敏。

## 场景

非 coding demo 是“家装售后排期外发防护”。

业务痛点：客服 Agent 需要查售后工单、生成上门排期建议，但不能把内部邮箱、token 等敏感字段带给模型或用户，也不能在没有人工确认时直接给客户发短信/企微通知。

工具：

- `lookup_service_case`：低风险，查询售后工单；返回里包含邮箱和内部 token，用来验证脱敏。
- `draft_visit_plan`：低风险，生成上门排期建议。
- `send_customer_notice`：高风险，真实外发动作；默认需要人工审批，未审批时返回 guardrail 拦截报告。

## 运行

只看策略摘要，不调用模型：

```bash
go run ./cmd/guardrail-agent -prepare-only
```

真实运行 ADK Agent：

```bash
go run ./cmd/guardrail-agent
```

默认消息会带上演示工单 `HS-1001`，让 Agent 直接进入查询、排期和外发审批链路；也可以传入：

```bash
go run ./cmd/guardrail-agent -message "客户厨房漏水，希望今天安排师傅并短信通知客户。"
```

演示批准高风险外发动作：

```bash
go run ./cmd/guardrail-agent -approve-external
```

模型配置从 `.env` 或当前环境读取：

- `OPENAI_API_KEY`
- `LLM_MODEL` 或 `OPENAI_MODEL`
- `LLM_OPENAI_BASE_URL`、`OPENAI_BASE_URL`、`OPENAI_API_BASE` 或 `OPENAI_API_BASE_URL`

`cmd/guardrail-agent` 加载 `.env` 时会覆盖当前进程里已有的同名变量，保证本地 demo 一切以仓库根目录 `.env` 为准，避免 shell、IDE 或 Codex 外层环境里的旧 token 污染运行。

本地 OpenAI-compatible 代理如果写成 `http://localhost:8317`，命令会自动归一化成 `http://localhost:8317/v1`，以匹配 Chat Completions 路径。

## Trace

命令入口用 `withRunAgentTrace` 做根 trace，上报 `query_chars`、`approve_external`、最终摘要等低敏字段，不上传完整客户请求、工具原文、邮箱或 token。

如果 `.env` 里配置了 CozeLoop，`cmd/guardrail-agent` 会通过 `observability/cozeloop.InstallFromEnv` 安装官方 Eino callback；未配置时保持 disabled，不影响本地运行。

## 测试

```bash
GOCACHE=/private/tmp/ai-designing-go-cache go test ./action/guardrail ./cmd/guardrail-agent
```
