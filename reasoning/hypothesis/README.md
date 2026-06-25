# Iterative Hypothesis Agent

这个包是 `reasoning/d-iterative-hypothesis/pattern.py` 的 Go + Eino ADK 翻译版。

核心结构：

- `Hypothesis`：单个候选解释，包含先验、后验、状态和证据轨迹。
- `HypothesisTree`：保存本轮问题的假设集合，并按“未被反证的幸存假设”判断收敛。
- `IterativeHypothesisLoop`：执行 planner -> generator -> evaluator 循环，直到唯一确认假设幸存或达到迭代上限。

核心 Agent 流程：

```mermaid
flowchart TD
    A["用户问题"] --> B["ADK Runner"]
    B --> C["Hypothesis Agent"]
    C --> D["IterativeHypothesisLoop"]
    D --> E["Planner<br/>提出可反证候选假设"]
    E --> F["HypothesisTree<br/>保存假设和状态"]
    F --> G["Generator<br/>为活跃假设找证据"]
    G --> H["Evaluator<br/>判断 supports / refutes / neutral"]
    H --> I["HypothesisTree<br/>更新后验和状态"]
    I --> P["收敛条件<br/>confirmed=1<br/>survivor=1<br/>survivor 表示未被 falsified"]
    P --> J{"满足?"}
    J -->|是| K["收敛：输出确认假设"]
    J -->|否| L{"达到迭代上限?"}
    L -->|否| E
    L -->|是| M{"还剩几个幸存假设?"}
    M -->|1 个| N["输出仍幸存假设"]
    M -->|0 个或多个| O["需要人工介入"]
```

记忆点：这个模式不是让模型直接给答案，而是先把可能解释并列放进 `HypothesisTree`，再用证据逐轮反证；最终只相信“唯一没有被反证、且已被确认”的幸存假设。

命令入口：

```bash
go run ./cmd/hypothesis-agent -prepare-only
go run ./cmd/hypothesis-agent -json
```

默认场景是社区活动报名下滑诊断，属于非 coding 的现实根因分析任务。
