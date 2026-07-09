只看 `/Users/wangyingjie/Documents/code/ai-designing/perception/claude_compaction`，现在实现的是一套 **Claude Code 风格 query-loop 压缩还原**，可以分成 6 类：

1. **Tool Result Budget 压缩**

   入口：[tool_result_budget.go](/Users/wangyingjie/Documents/code/ai-designing/perception/claude_compaction/tool_result_budget.go:30)

   作用：在 microcompact 之前，先把超预算的工具结果替换成占位文本。

   本质是：**大工具输出不直接塞进模型上下文，只留一个 replacement marker 和记录**。

2. **HISTORY_SNIP 压缩**

   入口：[history_snip.go](/Users/wangyingjie/Documents/code/ai-designing/perception/claude_compaction/history_snip.go:21)

   作用：上下文太大时，把较早历史裁掉，只保留最近几条消息，并插入 `history_snip_boundary`。

   本质是：**直接剪掉旧历史，用边界消息标记“这里裁过”**。

3. **Microcompact 轻量压缩**

   入口：[microcompact.go](/Users/wangyingjie/Documents/code/ai-designing/perception/claude_compaction/microcompact.go:29)

   作用：只清理旧的可压缩工具结果，比如 `Read`、`Bash`、`Grep`、`Glob`、`WebSearch` 等。

   本质是：**不调用摘要模型，只把旧 tool result 内容换成 `[Old tool result content cleared]`**。

4. **AutoCompact 触发策略**

   入口：[policy.go](/Users/wangyingjie/Documents/code/ai-designing/perception/claude_compaction/policy.go:31)

   它本身不是压缩，而是 gate：判断当前上下文是否超过 `effective window - buffer`，并避免 `compact` / `session_memory` 递归触发。

   本质是：**决定要不要进入真正的语义压缩**。

5. **模型语义摘要压缩**

   入口：[compactor.go](/Users/wangyingjie/Documents/code/ai-designing/perception/claude_compaction/compactor.go:94)

   作用：如果没有可用 session memory，就把裁剪后的消息交给 `SummaryModel.Summarize` 生成 compact summary。

   相关处理：
    - `PrepareMessagesForSummary`：只取最后边界之后的消息，过滤可重建附件，图片/文档转占位。
    - `BuildCompactPrompt`：要求模型输出 `<analysis>` + `<summary>`。
    - `FormatCompactSummary`：去掉 `<analysis>`，只保留可继续工作的 summary。
    - `BuildPostCompactMessages`：组装 `compact boundary -> summary -> kept messages -> attachments`。

   本质是：**模型只负责总结语义，边界和恢复上下文由代码负责**。

6. **Session Memory 压缩**

   入口：[compactor.go](/Users/wangyingjie/Documents/code/ai-designing/perception/claude_compaction/compactor.go:105)

   作用：如果 `SessionMemory.Enabled` 且已有 `Content`，就不再调用模型总结，而是直接把 session notes 包装成 compact summary。

   本质是：**复用已经维护好的短期会话摘要，避免再次总结全部历史**。

总流程在 [pipeline.go](/Users/wangyingjie/Documents/code/ai-designing/perception/claude_compaction/pipeline.go:75)：

```text
MessagesAfterLastCompactBoundary
-> ApplyToolResultBudget
-> HistorySnip
-> Microcompact
-> AutoCompactPolicy
-> Compactor.Compact
-> postCompactMessages 写回 / 替换 messagesForQuery
```

所以一句话概括：

**`claude_compaction` 实现了三种非模型压缩：tool result budget、history snip、microcompact；两种语义压缩：模型 compact summary、session memory compact；再用 auto compact policy 和 query pipeline 把它们串成 Claude Code 风格的上下文压缩流程。**