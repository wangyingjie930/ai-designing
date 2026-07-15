## 先说结论

Claude Code **不能保证第一次工具选择一定正确**。它的做法是：

> 缩小候选工具范围 → 用描述和 Schema 引导选择 → 执行前校验拦截 → 把结果/错误反馈给模型 → 下一轮重新选择。

本质上是一个可纠错的 ReAct 工具循环，不是独立的“工具选择分类器”。

## 具体机制

1. **先减少候选工具**

   禁用、权限明确拒绝、当前模式不可用的工具，会在模型看到之前被过滤，避免工具越多越容易混淆。见 [tools.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools.ts:253)。

2. **靠工具描述和 Schema 引导**

   系统提示明确区分：

    - 找文件名用 `Glob`
    - 搜文件内容用 `Grep`
    - 读文件用 `Read`
    - 有专用工具时不要滥用 `Bash`

   见 [prompts.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/constants/prompts.ts:287)、[Glob 提示](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/GlobTool/prompt.ts:3) 和 [Grep 提示](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/GrepTool/prompt.ts:6)。

   工具的描述和 JSON Schema 会一起发送给模型，但主循环的 `toolChoice` 是 `undefined`，即通常仍由模型自主决定，并没有硬编码路由器。见 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:659) 和 [claude.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/api/claude.ts:1699)。

3. **工具太多时动态加载**

   MCP 工具较多时，只先展示工具名；模型通过 `ToolSearch` 找到候选工具后，再加载完整 Schema，减少大量相似工具同时挤进上下文造成的误选。见 [claude.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/api/claude.ts:1118) 和 [ToolSearch 提示](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/ToolSearchTool/prompt.ts:27)。

4. **执行前做硬校验**

    - 工具不存在：返回 `No such tool available`
    - 参数类型错误：Zod Schema 校验失败
    - 参数值不合法：工具自己的 `validateInput()` 拦截
    - 有风险：权限系统或 `PreToolUse` Hook 可以拒绝

   这些都会生成带 `is_error: true` 的 `tool_result`，见 [toolExecution.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/tools/toolExecution.ts:368) 和 [参数校验逻辑](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/tools/toolExecution.ts:614)。

5. **把错误回灌，让模型重规划**

   工具成功结果或失败信息都会追加进消息，再进入下一轮模型调用：

   ```text
   模型选择工具
   → 执行工具
   → 返回 tool_result
   → 模型读取结果
   → 继续、换工具或修正参数
   ```

   对应递归循环在 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:1678)。工具异常还会触发 `PostToolUseFailure` Hook，见 [toolExecution.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/tools/toolExecution.ts:1589)。

## 最关键的边界

如果模型选了一个“语法合法但语义不合适”的工具，客户端通常识别不出来。

例如用户想找 `*.ts` 文件，模型却调用 `Grep`。只要参数合法，Claude Code 不会直接判定“工具选错”；它只能让模型看到空结果或低质量结果，然后下一轮自己改用 `Glob`。

所以准确地说：

> Claude Code 解决的是“工具选择错误后的可恢复性”，并通过工具过滤、描述、Schema、权限和 Hook 降低错误概率，但没有彻底消灭语义层面的误选。

如果自己实现类似 Agent，最值得复刻的是：**少而清晰的候选工具 + 严格输入校验 + 诊断性错误结果 + 自动回到推理循环**。