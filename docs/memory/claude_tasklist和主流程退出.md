# Question

claudecode不是用tasklist吗 而claudecode的结束标识是没有tooluse
那会不会出现tasklist中的任务claudecode只完成了一半的情况?

# Answer

会。你的理解基本正确，而且这是 Claude Code 当前架构允许发生的情况。

Claude Code 的主循环大致是：

```text
模型响应包含 tool_use
        ↓
执行工具，返回 tool_result
        ↓
再次调用模型
        ↓
模型响应不包含 tool_use
        ↓
执行 Stop Hooks
        ↓
没有 Hook 阻止 → 当前 turn 结束
```

源码明确把“是否出现 `tool_use`”作为循环继续的主要信号；当 `needsFollowUp=false` 时，如果 Stop Hook 没有阻止，就直接返回 `completed`。[query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:551) [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:1062) [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:1267)

关键在于：这里没有类似下面的硬检查：

```ts
if (taskList.hasPendingOrInProgressTasks()) {
  continue
}
```

所以完全可能出现：

```text
Task 1 completed
Task 2 in_progress   ← 实际只完成一半
Task 3 pending

Claude：输出一段总结，没有调用工具
主循环：认为当前 turn 完成，退出
```

TaskList 更像“模型可读写的持久化进度状态”，不是控制主循环退出的状态机。

Claude Code 有一些软约束：

- TaskUpdate 提示词要求：实现不完整时必须保持 `in_progress`，完成后再调用 TaskList 找下一项。[prompt.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/TaskUpdateTool/prompt.ts:7)
- 长时间不更新任务会注入提醒，但默认要间隔约 10 个 assistant round，而且只是 gentle reminder。[attachments.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/attachments.ts:254)
- 可以通过 `Stop` Hook 检查未完成任务并拒绝结束。
- 可以通过 `TaskCompleted` Hook 阻止任务被错误标记为完成。
- `maxTurns`、用户中断、API 错误也可能让任务提前停止。

甚至还有更危险的第二种情况：模型把只完成一半的任务错误地 `TaskUpdate(status="completed")`。如果没有验证 Hook，TaskList 本身也无法证明代码真的完成。

所以准确结论是：

> `tool_use` 决定“执行循环是否继续”，TaskList 只帮助模型记住“还应该做什么”。默认情况下，两者没有强一致性，因此 Claude Code 确实可能在 TaskList 未完成、甚至实际工作只完成一半时结束当前 turn。

如果要做到严格闭环，应该在“无 `tool_use`、准备结束”时增加工程侧检查：只有所有任务完成，或者剩余任务都被明确标记为 blocked/cancelled，才允许真正退出；否则注入一条继续执行的消息，重新进入模型循环。