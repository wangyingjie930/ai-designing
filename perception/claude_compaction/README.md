只看 `/Users/wangyingjie/Documents/code/ai-designing/perception/claude_compaction`，现在实现的是一套 **Claude Code 风格 query-loop 压缩还原**，可以分成 7 类：

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

   这里实现的是普通 microcompact，不是 Claude Code 的 cached microcompact / cache editing 路径。后者依赖 Claude API 的服务端 prompt cache 能力：请求里给旧 `tool_result` 标记 `cache_reference`，再附带 `cache_edits` 删除指令，由服务端缓存层删除对应 KV cache 内容。本包没有接真实 Claude API adapter，所以暂时不实现，只保留本地消息改写版。

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

7. **Invoked Skill 状态保活**

   入口：[skill_state.go](/Users/wangyingjie/Documents/code/ai-designing/perception/claude_compaction/skill_state.go:37)

   作用：运行时记录已经调用过的 skill，compact 时自动生成 `invoked_skill` attachment，resume 时再从 attachment 恢复 `SkillState`。

   本质是：**不重复注入完整 `skill_listing`，只保留已经用过、继续工作必须遵守的 skill 内容**。

## 压缩后附件重建

Claude Code 压缩后不是只留下 summary，也不是把旧附件原样全量重放。它会在 `compact boundary -> compact summary` 之后，由代码重新补回必要 attachments，让模型能继续工作。

本包对应的是 [restoreAttachments](/Users/wangyingjie/Documents/code/ai-designing/perception/claude_compaction/compactor.go:220)：

- `compact_file_reference`：补回最近读过、压缩后仍需要参考的文件片段。
- `invoked_skill`：补回已经调用过的 skill 内容；完整 `skill_listing` 不会因为 compact 被重复注入。
- `plan_reference` / `plan_mode`：补回计划内容和计划模式状态。
- `deferred_tool_delta`：补回压缩前后模型仍需要知道的延迟工具变化。
- `mcp_instruction_delta`：补回 MCP 相关指令变化。

这里的关键边界是：**summary 负责保留语义连续性，attachments 负责恢复运行时上下文；可由代码重建的附件不会交给摘要模型硬记。**

总流程在 [pipeline.go](/Users/wangyingjie/Documents/code/ai-designing/perception/claude_compaction/pipeline.go:75)：

```text
MessagesAfterLastCompactBoundary
-> ApplyToolResultBudget
-> HistorySnip
-> Microcompact
-> AutoCompactPolicy
-> Compactor.Compact
-> SkillState 自动补回 invoked_skill attachment
-> postCompactMessages 写回 / 替换 messagesForQuery
-> resume 时从 invoked_skill attachment 恢复 SkillState
```

所以一句话概括：

**`claude_compaction` 实现了三种非模型压缩：tool result budget、history snip、microcompact；两种语义压缩：模型 compact summary、session memory compact；再用 auto compact policy、query pipeline 和 SkillState 把它们串成 Claude Code 风格的上下文压缩流程。**

## 暂不实现：Claude API cache editing

Claude Code 里的 cached microcompact 不是普通 prompt 技巧，而是 Claude API 的服务端缓存编辑能力。它大致做两件事：

1. 在发给 Claude API 的消息块里，为旧 `tool_result` 加上 `cache_reference`。
2. 在同一次或后续请求里插入 `cache_edits` block，例如声明删除某个 `cache_reference`。

这样本地 transcript 不需要被改写，但 Claude 服务端在组装本轮上下文缓存时，可以把指定旧工具结果从 KV cache 里移除。API 返回的 `cache_deleted_input_tokens` 可以用来确认这次删除实际释放了多少缓存 token。

本包当前暂不实现这条路径，原因是它依赖真实 Claude API 的 beta/header、`cache_reference`、`cache_edits` 和服务端缓存语义；如果只在本地模拟，最多只能生成“计划删除哪些 tool result”的结构，不能真的让服务端 KV cache 删除内容。因此当前实现保留语义正确优先的普通 microcompact：直接把本地消息里的旧工具结果内容替换为占位文本。
