# Iterative Hypothesis Agent

这个包是 `reasoning/d-iterative-hypothesis/pattern.py` 的 Go + Eino ADK 翻译版。

核心结构：

- `Hypothesis`：单个候选解释，包含先验、后验、状态和证据轨迹。
- `HypothesisTree`：保存本轮问题的假设集合，并按“未被反证的幸存假设”判断收敛。
- `IterativeHypothesisLoop`：执行 planner -> generator -> evaluator 循环，直到唯一确认假设幸存或达到迭代上限。

命令入口：

```bash
go run ./cmd/hypothesis-agent -prepare-only
go run ./cmd/hypothesis-agent -json
```

默认场景是社区活动报名下滑诊断，属于非 coding 的现实根因分析任务。
