# reasoning/tot

Go + Eino ADK 版 Tree-of-Thought ReasoningAgent，对齐 AG2 `ReasoningAgent` 的核心语义。

## 结构映射

- `ThinkNode`：推理树节点，包含 content/value/parent/reflection/rating/output/depth/children/visits。
- `ReasoningAgent`：内部拆成 thinker、grader、executor、prompt rewriter 四类模型调用边界。
- `ADKAgent`：把 `ReasoningAgent` 包装成 Eino ADK `Agent`，可直接交给 `adk.NewRunner`。
- `ReasonConfig`：支持 `beam_search`、`dfs`、`mcts`、`lats`、`forest_size`、`rating_scale`、`interim_execution` 等配置。
- `ExtractSFTDataset` / `ExtractRLHFPreferenceDataset`：从最近生成的推理树抽取训练数据。

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

有真实模型配置后可运行，默认会触发 `mctsReply`：

```bash
go run ./cmd/tot-decision-agent -max-depth 2 -nsim 3
```

如果要切回 beam search，再显式传 `-method beam_search`。
