一句话：Claude Code 把“记忆”当作待验证的历史线索，而不是真相数据库。冲突时通常以当前事实为准，再修改或删除旧记忆，而不是机械地保留两份。

主要分四层处理：

1. 读取时：当前状态优先

源码明确要求：

- 记忆只是某个时间点的观察。
- 如果记忆与当前文件、代码或外部资源冲突，信任当前观察结果。
- 随后更新或删除过期记忆。

参见 [memoryTypes.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/memdir/memoryTypes.ts:197)。

超过一天的记忆还会附带“可能过期，请验证当前代码”的提醒，见 [memoryAge.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/memdir/memoryAge.ts:22)。

2. 写入时：原地更新，不追加冲突版本

保存新记忆前，它会先查看已有记忆：

- 按主题组织，而不是按时间流水记录。
- 优先更新已有主题文件。
- 删除错误或过期内容。
- 避免为同一件事创建重复文件。

见 [memdir.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/memdir/memdir.ts:205) 和 [prompts.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/extractMemories/prompts.ts:29)。

比如旧记忆是：

```text
项目使用 npm
```

后来用户说“已经迁移到 pnpm”，理想处理不是再增加一条相反记录，而是验证当前项目配置，然后把原主题记忆改成：

```text
项目当前使用 pnpm；npm 是迁移前状态
```

如果旧信息已经没有价值，则直接删掉。

3. 定期整理：合并、纠错、剪枝

Auto Dream 会对记忆做一次整理：

- 把新信息合并进现有主题。
- 删除已被当前代码否定的事实。
- 两个记忆文件互相矛盾时，修正错误的那个。
- 删除指向 stale、wrong、superseded 记忆的索引。

见 [consolidationPrompt.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/autoDream/consolidationPrompt.ts:33)。

4. 当前会话：重点刷新 Current State

会话压缩使用结构化 Session Memory，其中专门保留：

- `Current State`
- `Errors & Corrections`
- `Task specification`
- `Worklog`

每次更新都要求 `Current State` 反映最新状态；空间不足时压缩较老、较次要的信息，优先保证当前状态和纠错记录准确。见 [SessionMemory/prompts.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/SessionMemory/prompts.ts:43)。

需要注意：这不是一个严格的“冲突检测算法”。它没有针对每条事实建立版本号、置信度或 CRDT；主要依靠：

```text
相关性召回
→ 时间过期提醒
→ 读取当前代码进行验证
→ 模型修改/删除旧记忆
→ 后台定期合并清理
```

因此它仍可能漏掉隐蔽矛盾。其关键设计思想不是“新记忆一定正确”，而是：

> 当前可验证事实优先；无法验证时保留时间和原因，让未来的 Claude 能判断旧记忆是否仍然成立。

另外，Team Memory 的“同步冲突”属于另一类问题：文件同步时采用服务端优先，并通过 ETag/412 重试；这解决的是并发写入冲突，不是语义矛盾，见 [teamMemorySync/index.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/teamMemorySync/index.ts:1148)。