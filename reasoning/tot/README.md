# reasoning/tot

Go + Eino ADK 版 Tree-of-Thought ReasoningAgent，对齐 AG2 `ReasoningAgent` 的核心语义。

## 结构映射

- `ThinkNode`：推理树节点，包含 content/value/parent/reflection/rating/output/depth/children/visits。
- `ReasoningAgent`：内部拆成 thinker、grader、executor、prompt rewriter 四类模型调用边界。
- `ADKAgent`：把 `ReasoningAgent` 包装成 Eino ADK `Agent`，可直接交给 `adk.NewRunner`。
- `ReasonConfig`：支持 `beam_search`、`dfs`、`mcts`、`lats`、`forest_size`、`rating_scale`、`interim_execution` 等配置。
- `ExtractSFTDataset` / `ExtractRLHFPreferenceDataset`：从最近生成的推理树抽取训练数据。

## 搜索策略理解

- `beamReply` 更像“广度优先 + 剪枝”：每轮扩展当前层的 `prevLeaves`，给新候选打分排序，只保留 `BeamSize` 个继续向下走。
- `dfs` 在当前实现里是 `beamReply` 的特例，相当于 `beam_size=1`，每层只保留一个候选。
- `mctsReply` 不是普通深度优先。它每次 simulation 会从 root 沿一条路径往下走，但下一步由 UCT 权重选择，而不是固定顺序遍历。
- `mctsReply` 的核心循环是：UCT 选择已有 child -> 没有 child 时扩展 -> 随机 rollout 到终止 -> 给最终轨迹打分 -> `Backpropagate` 回传 reward。
- 直觉上：`beam_search` 是横向保留多个高分候选；`mcts` 是多次纵向采样路径，并逐渐把访问次数和分数集中到更有希望的分支。

## 最小用法

```go
runner, core, err := tot.NewRunner(ctx, tot.Config{
    Model: chatModel,
    ReasonConfig: tot.ReasonConfig{
        Method:   tot.MethodBeamSearch,
        MaxDepth: 4,
        BeamSize: 3,
    },
})
if err != nil {
    return err
}

iter := runner.Query(ctx, "请推理这个问题...")
_ = core // 可用 core.Root() 查看推理树
```

## Cmd 调用

`cmd/tot-decision-agent` 是一个非 coding 调用入口，默认读取 `reasoning/tot/examples/event_decision_prompt.txt`，然后直接通过 `tot.NewRunner` 和 `runner.Query` 调用 ToT。

```bash
go run ./cmd/tot-decision-agent -prepare-only
```

有真实模型配置后可运行，cmd 默认会用快速验证参数触发 `mctsReply`：`method=mcts`、`max_depth=1`、`nsim=1`、`forest_size=1`。摘要里的 `RootVisits=1` 可以证明 MCTS 至少完成了一轮 simulation。

```bash
go run ./cmd/tot-decision-agent
```

如果要观察更深的搜索过程，再显式放大参数：

```bash
go run ./cmd/tot-decision-agent -max-depth 2 -nsim 3
```

如果要切回 beam search，再显式传 `-method beam_search`。
