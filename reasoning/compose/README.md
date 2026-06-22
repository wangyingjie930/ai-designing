# reasoning/compose

这个包把已经实现的 `reasoning/tot`、`reasoning/cot`、`reasoning/hypothesis` 组合成图里的自适应推理流程。

核心节点映射：

- `Complexity Router`：`Agent.route`，输出 `simple/moderate/complex`。
- `Direct Response/System 1`：`solveSimple`，简单问题直接答。
- `Chain-of-Thought/single path`：`solveModerate`，中等问题走 `cot.ReasoningAgent`，低置信度或校验失败时进入假设检验。
- `Parallel Exploration/multiple paths`：`solveComplex`，复杂问题走 `tot.ReasoningAgent`，再做最佳路径确认。
- `Hypothesis Testing`：`verifyWithHypothesis`，复用 `hypothesis.Agent` 做反证循环。
- `Escalate to Human`：当 hypothesis 结果 `NeedsHITL=true` 时，组合层返回人工接管状态。

命令入口：

```bash
go run ./cmd/compose-router-agent -prepare-only
go run ./cmd/compose-router-agent -json
```

默认 demo 是非 coding 的学生支持分流场景，输入来自 `reasoning/compose/examples/student_support_prompt.txt`。
