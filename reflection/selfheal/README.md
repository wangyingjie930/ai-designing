# Self-Heal Loop with Eino ADK

`reflection/selfheal` 把用户贴的 Python `SelfHealLoop` 翻成 Go + Eino ADK 形态。核心原则是：循环、回滚、回归判断由 Go 确定执行；模型只负责诊断、修复生成和风险评审。

## Eino 结合方式

- `Loop`：确定性状态机，负责 `fixed`、`blocked`、`rolled_back`、`human_handoff` 分支。
- `ModelDiagnoser`：用 Eino `BaseChatModel` 做失败根因诊断。
- `ModelFixGenerator`：用 Eino `BaseChatModel` 输出结构化 `FixProposal`。
- `ModelCritic`：用 Eino `BaseChatModel` 判断补丁是否应该阻断。
- `Applier` / `Verifier` / `Rollbacker`：受控副作用边界，可以接配置中心、审批系统、回放测试或业务规则校验。
- `ADKAgent`：把核心 loop 包成 Eino ADK Agent，Runner 的 `CustomizedOutput` 会返回完整 `Response`。

## 非 coding 示例

`cmd/self-heal-agent` 演示客服补偿 SOP 自愈：

1. 初始失败：客服补偿 SOP 缺少补偿窗口和升级边界，一线客服反复询问主管。
2. 诊断节点：判断根因是 SOP 配置缺少可执行规则。
3. 修复节点：生成 `refund_window_hours=24; escalation_enabled=true; compensation_limit=200` 这类业务配置补丁。
4. 应用工具：把补丁应用到内存 SOP 策略版本。
5. 验证工具：回放业务规则，确认补偿窗口、升级开关和补偿上限都满足；若补丁放开无审核高额赔付，则触发回滚。

## 运行

普通单测不依赖真实模型：

```bash
GOCACHE=/private/tmp/ai-designing-gocache go test ./reflection/selfheal ./cmd/self-heal-agent -count=1
```

真实模型运行：

```bash
go run ./cmd/self-heal-agent
```

可用环境变量：

- `OPENAI_API_KEY` / `LLM_OPENAI_API_KEY` / `LLM_API_KEY`
- `LLM_MODEL` / `OPENAI_MODEL`
- `LLM_OPENAI_BASE_URL` / `OPENAI_BASE_URL` / `OPENAI_API_BASE` / `OPENAI_API_BASE_URL`
- `COZELOOP_*`，沿用仓库现有 `observability/cozeloop`
- `SELF_HEAL_MAX_ITERATIONS`

## Trace 边界

`cmd/self-heal-agent/trace.go` 只上报场景名、失败类型、严重度、受影响面数量、迭代上限和最终策略摘要。不把完整 SOP 失败文本、模型补丁正文或客户材料塞进命令级 root trace。
