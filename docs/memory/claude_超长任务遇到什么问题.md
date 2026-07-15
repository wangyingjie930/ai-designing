## 先记这个

Claude Code 对超长对话采用的是“完整历史落盘，模型上下文按需压缩”，而不是把所有历史一直塞给模型。

所以超长任务最大的风险并不是“历史丢了”，而是：

> 原始历史还在磁盘里，但压缩后的模型上下文可能已经遗失关键细节，导致目标漂移、重复工作或错误判断任务已完成。

## Claude Code 实际怎么管理对话历史

可以拆成五个彼此独立的层次：

| 层 | 实现 | 负责什么 |
|---|---|---|
| 输入历史 | `~/.claude/history.jsonl` | 上下键、搜索以前输入过的提示词，不是完整对话 |
| Transcript | 项目目录下的 `<sessionId>.jsonl` | 保存用户消息、助手消息、工具调用和结果 |
| Compact / Session Memory | 摘要 + 最近原文消息 | 控制真正发送给模型的上下文 |
| TaskList / Plan | 独立任务文件和计划文件 | 保存待办、完成状态和工作计划 |
| Auto Memory | `MEMORY.md` 和主题文件 | 保存跨会话仍有价值的长期知识 |

### 1. Transcript 是原始事实层

Claude Code 会把消息主要以 JSONL 形式追加到当前项目、当前 session 对应的 transcript。每条记录包含 `uuid`、`parentUuid` 等字段，所以它不是简单数组，而是可以根据父子关系恢复某条分支。

恢复会话时，会从所选叶子沿 `parentUuid` 向上构建当前对话链。

相关实现：

- [history.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/history.ts:115)：输入历史
- [sessionStorage.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/sessionStorage.ts:202)：transcript 路径
- [sessionStorage.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/sessionStorage.ts:993)：写入消息链
- [sessionStorage.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/sessionStorage.ts:2069)：按 `parentUuid` 恢复链

用户消息会在进入模型循环前先落盘，因此即使回复过程中进程被杀，也更容易恢复：[QueryEngine.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/QueryEngine.ts:436)。

### 2. 模型看到的不是完整 Transcript

每次请求模型时，Claude Code 会从最近一次 compact boundary 之后取消息，然后依次进行：

```text
最近 compact boundary 后的消息
  → 超大工具结果持久化/替换
  → history snip
  → microcompact
  → context collapse
  → auto compact
  → 摘要 + 最近原文 + 可重建附件
```

入口在 [query.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/query.ts:365)。

Compact 摘要会尽量保留：

- 用户目标和所有用户消息
- 修改过的文件与代码
- 错误和解决过程
- 待完成任务
- 当前工作和下一步

但 compact prompt 自己也明确要求：如果需要精确代码、错误文本或生成内容，应回看完整 transcript。这说明摘要本身就是有损的：[prompt.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/compact/prompt.ts:337)。

### 3. 进度不是只放在 Session Memory 里

Claude Code 把几种状态分开管理：

- TaskList：结构化的 `pending / in_progress / completed`、负责人和依赖关系。
- Plan：准备怎么做。
- Session Memory：当前会话的语义摘要和工作日志。
- Transcript：真正发生过什么。
- Auto Memory：跨会话的稳定知识。

TaskList 默认持久化到 `~/.claude/tasks/<task-list-id>/`，恢复 session 后可以继续读：[tasks.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/tasks.ts:199)。

Session Memory 则由后台 fork agent 定期更新，但它是 feature-gated、异步、best-effort 的，不是事务性事实源：[sessionMemory.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/SessionMemory/sessionMemory.ts:272)。

## 超长任务会遇到什么问题

1. 上下文窗口耗尽  
   工具输出和代码阅读会快速消耗 token。关闭自动压缩后，达到硬限制会直接返回 prompt-too-long。

2. 摘要发生信息损失  
   精确 ID、错误文本、代码片段和隐含约束可能只剩概括。多次摘要再摘要还会累积语义漂移。

3. 目标和完成状态漂移  
   Plan 只表示“准备做什么”；TaskList 也依赖模型主动调用工具更新。如果模型忘记更新，磁盘状态就会落后于真实进度。

4. Compact 自己也可能超长  
   如果压缩请求本身超过限制，Claude Code 会按 API round 丢弃更老的消息组，最多重试三次。这是明确的有损降级：[compact.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/compact/compact.ts:243)。

5. 自动压缩可能熔断  
   连续三次 compact 失败后会触发 circuit breaker，停止继续自动尝试：[autoCompact.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/compact/autoCompact.ts:262)。

6. Transcript 本身会变得很大  
   源码专门处理几十 MB 甚至数 GB transcript 的读取、跳过旧 compact 段和 OOM 风险。历史能落盘，不代表它能被低成本地全部恢复进模型。

