## 先说结论

Claude Code 不能保证压缩摘要百分之百不丢信息。

它真正保证的是：

> 原始事实尽量不被压缩过程破坏；即使模型当前上下文丢了细节，仍有 Transcript、工具结果文件、TaskList 等独立事实源可以回查。

也就是“允许暂时忘记，但尽量保证还能找回来”。

## Claude Code 的六道防线

### 1. Compact 不删除磁盘上的完整 Transcript

压缩主要替换模型下一轮看到的上下文，完整 transcript 仍然保存在磁盘。

压缩后的提示还会明确告诉模型：

> 如果需要压缩前的精确代码、错误信息或生成内容，去读取完整 transcript。

对应代码在 [prompt.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/compact/prompt.ts:337)。

这解决的是“摘要遗漏后还能回查”，但不能保证模型一定会主动回查。

### 2. 摘要 Prompt 强制覆盖关键内容

Compact Prompt 要求保留：

- 用户的所有明确要求
- 所有用户消息
- 修改过的文件和代码
- 错误及修复过程
- 待办任务
- 当前工作
- 下一步
- 最近用户原话

见 [prompt.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/compact/prompt.ts:61)。

这是通过结构化 Prompt 降低遗漏概率，但摘要仍由模型生成，所以不是确定性保证。

### 3. 不只保留摘要，还保留最近原文

压缩后的上下文实际是：

```text
Compact Boundary
+ 摘要
+ 最近若干条原始消息
+ 文件、Plan、Skill 等附件
+ Hook 结果
```

见 [compact.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/compact/compact.ts:325)。

保留最近原文时还会避免切断 `tool_use/tool_result` 配对，防止压缩后出现“调用了工具却没有结果”或者相反的情况：[sessionMemoryCompact.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/compact/sessionMemoryCompact.ts:568)。

### 4. 用 UUID 保证摘要边界不漏消息

Session Memory 会记录：

```text
lastSummarizedMessageId
```

它表示“摘要已经覆盖到哪条消息”。

这个 ID 只有在摘要成功生成之后才会推进；如果最后一轮还有工具调用，则不会贸然推进，避免产生孤立的 `tool_result`：[sessionMemory.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/SessionMemory/sessionMemory.ts:485)。

压缩时如果找不到这个 UUID，Claude Code 不会猜测边界，而是放弃 Session Memory 方案，回退到传统 compact：[sessionMemoryCompact.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/compact/sessionMemoryCompact.ts:548)。

这是比较接近确定性保障的一层：主要防止消息覆盖范围出现空洞。

### 5. 精确状态从摘要中拆出去

Claude Code 不完全依赖自然语言摘要记录任务状态。

TaskList 独立保存为 JSON：

```text
~/.claude/tasks/<task-list-id>/<task-id>.json
```

它记录：

- `pending / in_progress / completed`
- `owner`
- `blocks`
- `blockedBy`

见 [tasks.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/tasks.ts:199)。

任务被标记为完成时，还可以执行 `TaskCompleted` Hook；Hook 返回阻断错误，状态就不能改成 `completed`：[TaskUpdateTool.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/TaskUpdateTool/TaskUpdateTool.ts:229)。

不过这个保障有两个限制：

- TaskList 仍依赖模型主动创建和更新。
- `TaskCompleted` Hook 是扩展点，不是默认强制运行测试和验证。

所以默认情况下，Claude Code 仍可能错误判断完成。

### 6. 大工具结果不会只剩摘要

超大工具结果会写入独立文件，模型上下文只保留预览和文件引用：[toolResultStorage.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/toolResultStorage.ts:267)。

如果因为整个消息的工具结果预算超限而进行了替换，替换记录也会写入 transcript，恢复会话时重新构造同样的替换状态：[toolResultStorage.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/toolResultStorage.ts:924)。

因此类似下面的精确内容通常仍可恢复：

- 完整测试输出
- 超长日志
- 错误堆栈
- 请求 ID
- 大段搜索结果

## 哪些能保证，哪些不能

| 问题 | 保障程度 |
|---|---|
| Compact 后原始历史仍可回查 | 较强，Transcript 保留 |
| 摘要边界不漏掉一段消息 | 较强，使用 UUID 校验和回退 |
| 工具调用和结果不被切断 | 较强，压缩时维护 API 配对约束 |
| 超大工具输出仍能找回来 | 较强，持久化文件和替换记录 |
| 所有关键细节都进入摘要 | 不能保证 |
| 模型发现缺失后一定读取 Transcript | 不能保证 |
| TaskList 永远符合真实进度 | 不能保证 |
| 任务不会发生目标漂移 | 不能保证 |
| 完成前一定经过证据验证 | 只有配置 Hook 等验证机制后才成立 |

## 一个具体例子

假设早期日志里出现：

```text
request_id=abc-123
原因是数据库字段 status=4
```

压缩后摘要可能只剩：

```text
排查到数据库状态异常。
```

Claude Code 的策略不是保证摘要一定保留 `abc-123`，而是：

1. 原日志仍在 transcript 或工具结果文件中。
2. 摘要告诉模型精确信息需要回查 transcript。
3. 如果任务还没解决，TaskList 应继续保持 `in_progress`。
4. 如果配置了验证 Hook，可以阻止没有证据的 `completed`。

问题在于第 2、3 步仍可能依赖模型主动执行。

## 和 ai-designing 的关系

这正是 `ai-designing/memory/progress_tracking v2` 比 Claude Code 默认实现更强的地方。

Claude Code 的核心思路是：

```text
历史可回查 + 摘要维持连续性 + TaskList 保存状态
```

`progress_tracking v2` 进一步增加：

```text
GoalContract
+ EvidenceRefs
+ MechanicalValue
+ append-only ProgressEvent
+ DriftSignal
+ deterministic completion gate
```

所以更准确的判断是：

> Claude Code 解决了“压缩后如何继续工作和恢复细节”，但没有完全解决“如何确定目标没有漂移、完成判断一定有证据”。后一个问题正是 `ai-designing/progress_tracking v2` 要补的。