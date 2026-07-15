结论：Claude Code 的需求澄清不是独立的“需求分析模块”，而是：

`Prompt 约束模型何时提问 → AskUserQuestion 工具暂停主循环 → 用户回答 → 答案作为 tool_result 回灌 → 模型继续执行`

### 1. 怎么发起需求澄清

系统提示词先约束模型：

- 普通模式：只有经过调查仍然卡住时才询问，不能一遇到问题就问用户。[constants/prompts.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/constants/prompts.ts:233)
- Plan 模式：先读代码；只有遇到“代码无法回答、必须由用户决定”的需求、偏好和取舍才提问；相关问题应批量询问。[messages.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/messages.ts:3342)

模型需要澄清时，输出：

```text
tool_use: AskUserQuestion
```

它支持：

- 一次 1～4 个问题
- 每题 2～4 个选项
- 单选或多选
- 自动提供 `Other`
- 选项描述和方案预览

定义在 [AskUserQuestionTool.tsx](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/AskUserQuestionTool/AskUserQuestionTool.tsx:14)。

### 2. 它怎么“停止执行，等待用户澄清”

`AskUserQuestion` 有两个关键属性：

```ts
requiresUserInteraction() {
  return true
}

async checkPermissions(input) {
  return {
    behavior: 'ask',
    message: 'Answer questions?',
    updatedInput: input,
  }
}
```

见 [AskUserQuestionTool.tsx](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/AskUserQuestionTool/AskUserQuestionTool.tsx:155)。

因此运行到这里时：

1. `runTools()` 执行 `AskUserQuestion`。
2. 权限层返回一个尚未完成的 Promise。
3. TUI 显示问题选择界面。
4. 在用户回答前，`runTools()` 无法完成。
5. `query` 主循环也就不会发起下一轮模型请求。

所以它并没有把整个 Agent 状态机“停机”，而是挂起在工具权限 Promise 上。

用户提交答案后，界面调用 `onAllow(updatedInput)`，把答案补进工具输入。[AskUserQuestionPermissionRequest.tsx](/Users/wangyingjie/Documents/code/claude-code-source-study/src/components/permissions/AskUserQuestionPermissionRequest/AskUserQuestionPermissionRequest.tsx:398)

然后工具生成：

```text
tool_result:
User has answered your questions: ...
You can now continue with the user's answers in mind.
```

见 [AskUserQuestionTool.tsx](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/AskUserQuestionTool/AskUserQuestionTool.tsx:224)。

`query` 收集这个 `tool_result`，重新进入下一轮模型调用。[query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:1678)

### 3. 它怎么判断“不需要继续澄清了”

没有硬编码的：

```ts
clarificationDone = true
```

正常情况下，是模型根据以下信息自己收敛：

- 用户原始需求
- 已读取的代码
- 用户刚才的答案
- Plan 模式的完成标准

Plan 模式定义的完成标准是：

- 歧义已经解决
- 明确改什么
- 明确修改哪些文件
- 明确复用哪些已有实现
- 明确如何验证

满足后，不再调用 `AskUserQuestion`，转而调用 `ExitPlanMode` 请求用户审批计划。[messages.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/messages.ts:3368)

### 4. 用户主动要求“不要再问了”

Plan 模式的提问界面有一个选项：

```text
Skip interview and plan immediately
```

见 [QuestionView.tsx](/Users/wangyingjie/Documents/code/claude-code-source-study/src/components/permissions/AskUserQuestionPermissionRequest/QuestionView.tsx:370)。

选择它后，Claude Code 会结束当前提问，并向模型回灌：

```text
The user has indicated they have provided enough answers...
Stop asking clarifying questions and proceed to finish the plan
with the information you have.
```

见 [AskUserQuestionPermissionRequest.tsx](/Users/wangyingjie/Documents/code/claude-code-source-study/src/components/permissions/AskUserQuestionPermissionRequest/AskUserQuestionPermissionRequest.tsx:341)。

但要注意：这也没有永久禁用 `AskUserQuestion`。它只是当前上下文里一条优先级很高的用户反馈，要求模型用现有信息完成计划。

另外，直接按 `Esc/Cancel` 是另一种语义：它会拒绝当前工具调用，并中止当前执行链，等待用户下一条消息；不是“按已有信息继续”。[PermissionContext.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/hooks/toolPermission/PermissionContext.ts:154)

一句话概括：

```text
是否提问：由 Prompt 引导模型判断
如何暂停：等待 AskUserQuestion 的交互权限 Promise
如何恢复：答案作为 tool_result，递归进入下一轮
何时不再问：模型按完成标准收敛，或用户显式 Skip interview
```