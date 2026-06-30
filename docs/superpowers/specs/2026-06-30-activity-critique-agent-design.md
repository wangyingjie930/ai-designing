# Activity Critique Agent Design

## 背景

用户给出的 Python `GeneratorCriticLoop` 表达的是固定反思循环：先生成结果，再收集可选工具反馈，再由 critic 输出问题、建议和分数，未达标时带着反馈重新生成。当前仓库已有 `reasoning/cot`、`reasoning/tot` 的 Go + Eino ADK 示例，本次实现沿用“核心包 + ADK 包装 + cmd 薄入口”的结构。

## 范围

核心翻译放在 `reflection/critique/`。Agent 示例放在 `cmd/activity-critique-agent/`。场景固定为“活动方案/运营文案质量迭代 Agent”：根据活动需求生成运营方案或文案，由 critic 从目标匹配、执行完整性、风险、转化动作和工具检查反馈进行评分，循环迭代直到通过或达到最大轮次。

不做命令行参数入口。cmd 默认和 `main_test` 都使用代码内置输入，用户可以在 GoLand 直接点击测试运行。

## 架构

`reflection/critique` 提供纯业务主流程：

- `CritiqueResult` 对齐 Python dataclass，包含 `Approved`、`Issues`、`Suggestions`、`Score` 和 `FeedbackText`。
- `GeneratorCriticLoop` 持有 generator model、critic model、迭代上限、质量阈值和历史记录。
- `Generate` 负责调用 generator 生成活动方案。
- `Critique` 负责调用 critic 并解析结构化 JSON。
- `Refine` 负责执行“生成 -> 工具反馈 -> 批评 -> 带反馈重生成”的闭环。
- `ADKAgent` 将核心 loop 包装成 Eino ADK Agent，便于 cmd 和业务入口通过 `adk.Runner` 调用。

`cmd/activity-critique-agent` 只做运行入口：

- 默认构造活动需求输入。
- 初始化 OpenAI-compatible chat model。
- 内置一个轻量 `activityChecklistTool`，检查预算、目标人群、渠道、时间、风险兜底和 CTA 是否出现。
- 通过 `critique.NewRunner` 执行 agent。
- `main_test.go` 提供可直接点击运行的测试：普通单元测试用 fake model，真实 E2E 继续遵循仓库现有 `CMD_E2E=1` 约定。

## 错误处理

核心包拒绝空 task、空模型、空 critic JSON 和空最终输出。critic JSON 允许被 Markdown 代码块包裹，但解析后必须有分数，且分数会被限制在 0 到 1。达到最大轮次但未通过时返回 `Converged=false`。

## 测试

先写失败测试，再实现：

- `reflection/critique`：验证完整循环顺序、工具反馈注入、达到阈值后收敛、未收敛时返回最后分数。
- `cmd/activity-critique-agent`：验证默认输入可用、main_test 直接调用 fake model 跑完整 ADK Runner、无需传一堆 flags。

## 验收

运行：

```bash
env GOCACHE=/private/tmp/ai-designing-gocache go test ./reflection/critique ./cmd/activity-critique-agent
```

预期两个包测试通过；`cmd/activity-critique-agent` 的测试无需真实模型即可证明 ADK Runner 调到了核心 loop。
