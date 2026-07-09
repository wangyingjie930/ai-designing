# reasoning/claude_cot

这个包用 OpenAI Responses API 复刻 Claude Code 的 native thinking / redacted thinking 边界。

核心映射：

- Claude Code `thinking` -> OpenAI `reasoning.summary`
- Claude Code `redacted_thinking.data` -> OpenAI `reasoning.encrypted_content`
- Claude Code 保留 thinking block 续接上下文 -> OpenAI 保留上一轮 `output` 里的 reasoning item，并用 `BuildNextInput` 原样带入下一轮 `input`

注意：OpenAI 不暴露原始 reasoning tokens，所以这里不会生成或保存原始 CoT。可见内容只来自 reasoning summary；不可见续接状态只保存 encrypted content。

## 最小用法

```go
client := claude_cot.NewResponsesClient(claude_cot.ResponsesClientConfig{
    APIKey:  os.Getenv("OPENAI_API_KEY"),
    Model:   os.Getenv("LLM_MODEL"),
    BaseURL: os.Getenv("LLM_OPENAI_BASE_URL"),
})

result, err := client.GenerateStepDrafts(ctx, claude_cot.GenerateRequest{
    SystemPrompt: "你是一个生产版 CoT 命题草稿生成器。",
    UserPrompt:   question,
    Reasoning: claude_cot.ThinkingConfig{
        Type:               claude_cot.ThinkingAdaptive,
        Effort:             "medium",
        Summary:            "auto",
        IncludeRedactedCOT: true,
    },
    SchemaName: "cot_v2_step_drafts",
    Schema:     cotv2.StepDraftListJSONSchema(),
    MaxTokens:  2048,
})
```

`result.Blocks` 会按 Claude Code 风格返回 `thinking`、`redacted_thinking` 和 `text`。其中 `redacted_thinking.data` 来自 OpenAI encrypted reasoning item；默认业务输出只应该展示 `thinking` 摘要和 `text`，`redacted_thinking` 用于下一轮上下文，不应该打印到日志或终端。

## Claude Code 风格轨迹编排

如果要像 Claude Code 一样跨轮续接 thinking 状态，优先使用 `Trajectory`，不要手写 input：

```go
trajectory := claude_cot.NewTrajectory(claude_cot.TrajectoryConfig{
    SystemPrompt: "你是一个生产版 CoT 命题草稿生成器。",
    Reasoning: claude_cot.ThinkingConfig{
        Type:    claude_cot.ThinkingAdaptive,
        Effort:  "medium",
        Summary: "concise",
    },
    SchemaName: "cot_v2_step_drafts",
    Schema:     cotv2.StepDraftListJSONSchema(),
    MaxTokens:  2048,
})

first, err := trajectory.RunTurn(ctx, client, "先判断员工 P 的 6 月薪资异常。")
second, err := trajectory.RunTurn(ctx, client, "继续核查审批单。")
```

`Trajectory` 做三件事：

- 记录上一轮 OpenAI `output` item。
- 过滤不能安全续传的尾部/孤儿 reasoning item。
- 下一轮自动把 `reasoning`、`message` 原样放回 `input`，再追加新的 user 消息。

这对应 Claude Code 的 protected thinking 规则：thinking/redacted thinking 是模型轨迹状态，不是业务文本；不能改写，不能解密，只能保留并继续传回模型。
