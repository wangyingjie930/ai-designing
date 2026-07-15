## 先记这个

Claude Code 不是简单地在“保守”和“积极”之间调一个档位，而是寻找：

> **已授权范围内，能够验证目标完成的最小充分动作集。**

也就是：

- “不越权”规定行动的上限。
- “做够多”规定完成的下限。
- “最小改动”控制中间不要过度发挥。

具体靠三层机制实现：

1. **目标边界：判断该不该做**

   系统提示明确要求不要增加未请求的功能、重构或额外优化，但也不能留下半成品。也就是“完成需求”不等于“顺手把周围都改好”。见 [prompts.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/constants/prompts.ts:199)。

2. **行动边界：判断能不能直接做**

   本地、可逆、低影响的动作，如读代码、修改相关文件、运行测试，可以自主推进；删除数据、覆盖未提交修改、推送代码、发送消息、修改共享系统等高影响动作，需要额外确认。见 [prompts.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/constants/prompts.ts:255)。

   而且这不只靠模型自觉。每次工具调用都会经过运行时权限判断：

   ```text
   deny → 直接禁止
   ask  → 请求用户确认
   allow → 执行
   ```

   显式 `deny`、内容级 `ask` 和敏感路径安全检查，甚至能优先于 `bypassPermissions`。见 [permissions.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/permissions/permissions.ts:1158)。

3. **完成边界：判断是不是已经做完**

   工具失败或权限拒绝不会直接结束任务，而是以 `is_error: true` 的 `tool_result` 回灌模型，让它选择更安全的替代方案、修复后重试或确实需要时再询问用户。见 [toolExecution.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/tools/toolExecution.ts:995) 和 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:1678)。

例如用户说“修复这个失败测试”：

- 可以自主读代码、定位原因、修改相关实现、运行测试。
- 第一次修改失败后应该继续诊断，不能立刻把问题甩给用户。
- 测试真正通过后才算完成，不能只说“代码看起来对”。
- 但不能顺手重构整个模块、修改 CI、推送分支或删除用户未提交代码。

最重要的边界是：

> **“不越权”主要有运行时硬闸门；“做够多”更多依赖模型行为、验证、Stop Hooks，以及部分版本中受 feature gate 控制的 Verification Agent。**

所以 Claude Code 可以显著减少越权，但不能从运行时上绝对证明“任务一定做够了”。它真正采用的是一种**安全优先、证据收尾、范围内持续推进**的设计。

一句话复述：

> Claude Code 把权限判断放在每次行动之前，把完成判断放在验证之后；权限不够就换安全路径或询问，证据不够就继续工作，但始终不扩大用户目标。



## 补充

- Claude Code 采用“确定性权限规则为主、用户确认为兜底、sandbox 做执行隔离”的分层机制；模型只负责提出工具调用，真正的风险裁决由运行时根据工具类型、命令结构、访问路径、权限规则和运行模式输出 allow / ask / deny，无法确定时保守地请求确认。
- 反正都是基于规则判断的
