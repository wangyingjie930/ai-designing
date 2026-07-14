# Research Swarm Claude Code 风格完成协议设计

## 目标

让 `action/research_swarm` 的完成通知语义与 Claude Code teammate 保持一致，同时保留当前 SQLite、外部 worker 进程和研究产物结构。

核心语义必须拆开：

- `update_task` 只维护共享任务状态。
- `send_message` 是 teammate 向 leader 汇报业务结果的唯一入口。
- `idle_notification` 只表达 teammate 已结束当前一轮并可继续接收消息。

## 非目标

- 不实现 Claude Code 的 plan approval、广播、完整 UI 状态和任务抢占。
- 不把 shutdown 改造成完整的 request/approve/reject 协议。
- 不重写 SQLite mailbox，也不改变外部进程 worker 结构。
- 不新增测试文件；只允许为新协议调整已有断言，并运行已有定向测试。

## 当前问题

当前 worker 在 ADK 一轮结束后由 runtime 强制把任务改成 `completed`，随后自动向 leader 发送 `task_completed/artifact_ready`。这把三个事实混在了一起：任务状态、业务结果和 teammate 生命周期。

Claude Code 的 teammate 不会自动把 assistant 结果转交给 leader。teammate 需要显式调用消息工具汇报；runtime 只在当前一轮结束后更新 idle 状态并发送 idle notification。

## 协议设计

### 任务状态

模型通过 `update_task` 写入 `pending`、`in_progress`、`completed` 或 `failed`。worker runtime 不再强制补写 `completed`。

worker runtime 在一轮结束后读取当前任务，生成 idle notification 中的可选完成摘要：

- task 为 `completed`：`completed_status = "resolved"`
- task 为 `failed`：`completed_status = "failed"`
- task 尚未结束：不填写 `completed_status`

### 显式结果消息

`send_message` 的模型可见参数简化为：

```json
{
  "to": "report_director",
  "summary": "已保存两张资料卡",
  "message": "source_cards 已就绪，ID 为 1、2"
}
```

模型只使用 teammate 名称。runtime 在内部把 `report_director` 解析成 `report_director@<team>`，SQLite 仍保存稳定 agent ID。

leader 和 worker 都可以使用 `send_message`。worker 的角色提示必须要求：先保存产物、更新任务，再向 leader 显式汇报结果。

### Idle notification

删除 `MessageKindTaskCompleted` 和 `TaskCompletionEvent`。worker 每次从 running 转到 idle 时向 leader mailbox 写一条普通 notification，内容为：

```json
{
  "type": "idle_notification",
  "agent_name": "researcher",
  "idle_reason": "available",
  "completed_task_id": 1,
  "completed_status": "resolved",
  "summary": "已完成当前任务，等待下一步"
}
```

idle notification 不携带完整产物内容，也不能被 leader 当成业务结果。worker 发送后保持运行，继续等待下一条 mailbox 消息或 shutdown。

### Leader 输入

leader mailbox 消息分成两类输入：

- 普通 teammate message：作为显式业务汇报交给 director，允许触发下一步 spawn 或追问。
- idle notification：只更新 teammate 可用性，并让 director 获得当前任务状态；不能单独证明结果已经汇报。

`report_director` 的工具面调整为 `spawn_teammate + send_message`。leader 可以向已经存在的 teammate 追问或分配补充说明。

默认离线 director 仍保留 searcher、analyst、writer 的演示链路，但推进信号改为三名 teammate 的显式 message，不再读取 `artifact_ready`。

## 完成条件

research swarm 只有同时满足以下条件才结束：

1. `最终报告` section 已写入 SQLite。
2. writer 对应任务状态为 `completed`。
3. leader 已消费 writer 的 `idle_notification`。

writer 的显式 message 用于 director 决策，但不能代替任务状态和 idle 生命周期门槛。

## 异常处理

- worker Agent 执行失败时，将当前任务标为 `failed`，写入 `idle_reason = "failed"` 的通知，然后返回错误。
- `send_message` 接收者必须是当前 team 的已注册成员；不存在时向模型返回错误，避免消息写入无人消费的 mailbox。
- 同一 worker 只有在 running 到 idle 的状态迁移时发送一次 idle notification。
- leader 收到无法解析的 notification 时保留普通消息内容，但不把它当成 idle 事件。

## 验证范围

不新增测试文件。实现完成后运行现有不依赖监听端口的定向测试，并检查旧协议标识已经删除：

```bash
env GOCACHE=/private/tmp/ai-designing-gocache go test ./action/research_swarm -run 'Test(Leader|RunLeader|CreateTeam|SpawnTeammate|Worker|RoleTools|SendMessageToolSchema|Store|FakeSearchClient)' -count=1
rg -n 'task_completed|artifact_ready|MessageKindTaskCompleted|TaskCompletionEvent' action/research_swarm
git diff --check
```
