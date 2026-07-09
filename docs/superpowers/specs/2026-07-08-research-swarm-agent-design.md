# 调查报告 Swarm Agent 设计

## 目标

在 `ai-designing` 中实现一个 Go + Eino ADK 的外部进程多智能体 demo，复现 Claude Code `teammate/swarm` 的核心实践：leader 先创建 team，再启动 report_director ADK agent；需要 teammate 时由模型调用 `spawn_teammate` 工具，工具再启动独立 worker 进程。worker 通过持久化 mailbox 通信，搜索和报告产物落到共享存储，再由 leader 汇总成一份带证据引用的调查报告。

第一版聚焦“调查报告 Agent”，不做 coding agent。默认使用稳定的 fake search 结果，保证本地测试和 demo 不依赖外部网络；同时保留 `HTTPJSONSearchClient`，可以通过环境变量接入 Tavily、Brave、SerpAPI 或内部搜索网关这类外部搜索引擎。

## 场景

默认场景是“围绕一个开放问题生成调查报告”。例如：

```text
调查主题：AI Agent 在客服工单处理中引入外部搜索和多角色审查后，能降低哪些风险？
```

队友分工如下：

- `report_director`：leader 角色，拆解调查目标，创建 team，通过 `spawn_teammate` 工具动态拉起 worker；worker 完成后由 leader 收到 mailbox completion 事件，再把事件作为下一轮输入交给 director。
- `searcher`：搜索员，调用 `web_search` 获取候选资料，把结果保存为 `source_cards`，更新 task completed，并由 worker runtime 回报 leader。
- `analyst`：分析员，读取 `source_cards`，归纳事实、冲突点、证据强弱和待确认问题，保存分析章节。
- `writer`：撰稿员，读取资料卡和分析章节，生成最终报告草稿，并标注引用的 source card id。

这个分工刻意保留 Claude Code teammate 的关键边界：worker 的普通 assistant 文本不是跨 agent 通信；默认同步不走固定 peer notification，而是 worker 完成后回报 leader。`send_message` 只保留给确实需要 teammate 之间临时澄清的 DM 场景。

## 架构

核心包放在 `action/research_swarm/`：

- `types.go`：定义 `Team`、`Member`、`MailboxMessage`、`ResearchTask`、`SourceCard`、`ReportSection`、`WorkerStatus`。
- `store.go`：SQLite 存储，负责 mailbox、成员状态、任务、source cards 和 report sections。
- `search.go`：定义 `SearchClient` 接口、`FakeSearchClient`、`HTTPJSONSearchClient`。
- `tools.go`：把 `send_message`、`update_task`、`web_search`、`save_source_card`、`list_source_cards`、`save_report_section` 暴露成 Eino ADK tools。
- `agent.go`：按角色构建 Eino `ChatModelAgent`，注入中文 instruction 和角色可见工具。
- `leader.go`：提供 `CreateTeam` 和底层 `TeamRuntime.SpawnTeammate` 边界；`RunLeader` 创建 team、运行 director agent、消费 leader mailbox completion 事件、关闭 teammate 和汇总结果。
- `director.go`：构建 report_director ADK agent，并只把 `spawn_teammate` 暴露为模型可见工具；默认离线 model 也通过 leader 事件输入模拟主控决策。
- `worker.go`：worker 主循环，消费自己的 mailbox，调用 ADK Runner，空闲后继续等待，收到 shutdown 后退出。

命令入口放在 `cmd/research-report-agent/`：

- `main.go`：解析 `-role leader|worker`、`-topic`、`-team`、`-agent`、`-db`、`-prepare-only`、`-json` 等参数。
- `trace.go`：按仓库现有约定只记录命令边界摘要，不把完整搜索结果和报告正文塞进 root trace。

## 进程模型

leader 运行时先创建 SQLite 数据库和 team。之后 report_director 模型每次需要 teammate 时，调用类似 Claude Code `AgentTool(name + team_name + description + prompt)` 的 `spawn_teammate` 工具。工具内部再通过 `os/exec` 启动同一个命令的 worker 模式。下面只是默认离线 director model 在三个阶段里触发的示例，不是 team 创建阶段的固定 roster，也不是 `RunLeader` 的显式业务脚本：

```text
research-report-agent -role worker -team research-demo -agent searcher -db /tmp/research-swarm.sqlite
research-report-agent -role worker -team research-demo -agent analyst  -db /tmp/research-swarm.sqlite
research-report-agent -role worker -team research-demo -agent writer   -db /tmp/research-swarm.sqlite
```

为了测试稳定，进程启动通过 `ProcessSpawner` 接口隔离：

- 生产实现使用 `exec.CommandContext` 启动真实 worker。
- 单元测试使用 fake spawner，只验证 leader 生成了正确命令和初始 mailbox。
- `CMD_E2E=1` 时才跑真实多进程端到端测试。

worker 的身份格式固定为：

```text
<agent_name>@<team_name>
```

例如 `searcher@research-demo`。所有 mailbox、task、source card 和 report section 都带 `team_name`，避免多个 demo run 混写。

## SQLite 数据模型

第一版使用 SQLite，不使用 JSON 文件。原因是外部进程需要并发读写，SQLite 比文件锁更稳定，且项目已经依赖 `github.com/mattn/go-sqlite3`。

核心表：

```text
swarm_members(
  agent_id, team_name, name, role, status, pid, last_seen_at, created_at
)

swarm_messages(
  id, team_name, from_agent, to_agent, kind, content_json,
  created_at, consumed_at
)

swarm_tasks(
  id, team_name, assignee, title, status, result_json,
  created_at, updated_at
)

source_cards(
  id, team_name, query, title, url, snippet, source,
  credibility, retrieved_at, created_by
)

report_sections(
  id, team_name, section, content, evidence_ids_json,
  created_by, updated_at
)
```

`content_json` 和 `result_json` 使用 JSON 字符串保存结构化内容，避免为了 demo 过早引入复杂 schema migration。存储层对外只暴露结构体方法，不让命令入口直接拼 SQL。

## 工具边界

不同角色看到不同工具：

- `searcher`：`web_search`、`save_source_card`、`send_message`、`update_task`
- `analyst`：`list_source_cards`、`save_report_section`、`send_message`、`update_task`
- `writer`：`list_source_cards`、`list_report_sections`、`save_report_section`、`send_message`、`update_task`

`web_search` 的输入包含 `query`、`top_k`、`language`，输出包含标题、URL、摘要、来源和检索时间。工具只负责搜索和返回候选资料，不直接写报告；是否保存为 source card 由 searcher 显式调用 `save_source_card`。

`send_message` 是跨 agent DM 的唯一入口。worker 普通 assistant 输出只作为本轮执行日志，不自动广播给其他 worker；默认任务完成同步由 worker runtime 向 `report_director@<team>` 投递 `task_completed` mailbox 消息，payload 中带 `type:"artifact_ready"`、`agent_name`、`artifact`、`section` 和 `task_id`。

## 搜索接入

搜索层定义统一接口：

```go
type SearchClient interface {
    Search(ctx context.Context, req SearchRequest) (*SearchResponse, error)
}
```

默认 provider 是 `fake`，内置若干稳定结果，方便测试和讲解多进程协作路径。

HTTP provider 通过环境变量启用：

```text
SEARCH_PROVIDER=http_json
SEARCH_API_URL=https://example.com/search
SEARCH_API_KEY=...
```

`HTTPJSONSearchClient` 使用 `POST` 发送 query/top_k/language，并兼容常见 JSON 搜索结果字段：`title`、`url`、`snippet`、`source`。如果外部搜索服务字段不同，后续只需要新增 provider adapter，不影响 swarm/mailbox/ADK 主流程。

## 主流程

leader 模式：

1. 读取主题和运行配置。
2. 初始化 SQLite store。
3. 调用 `CreateTeam` 创建 team lead 和 team 上下文；这一步不隐式创建 worker。
4. 构建并运行 report_director ADK agent，并先投递一轮 `start` 输入。
5. director model 调用 `spawn_teammate` 工具启动搜索员，并把 `description/prompt` 写入 mailbox task。
6. 搜索员写入 `source_cards`、更新 task completed，worker runtime 向 leader mailbox 投递 `task_completed/artifact_ready`。
7. leader 消费 completion 事件，把它作为下一轮 user input 喂给 director；director 再决定是否 spawn analyst 或 writer。
8. 最终报告章节落库后，`RunLeader` 读取 `report_sections`，生成最终调查报告。
9. 给已经动态创建的 teammate 投递 shutdown 消息并等待退出。

worker 模式：

1. 读取 `team`、`agent`、`db`。
2. 注册或更新自己的 member 状态。
3. 构建该角色的 Eino ADK `ChatModelAgent`。
4. 轮询自己的 mailbox。
5. 对每条未消费消息调用 `adk.Runner.Query`。
6. 通过工具写回任务、资料卡或报告章节；默认不向下游 teammate 发送固定 notification。
7. task completed 后向 leader mailbox 投递 completion 事件，再进入空闲。
8. 收到 shutdown 消息后标记 `stopped` 并退出。

## 错误处理

- worker 启动失败：leader 记录 member `failed`，并在最终输出里说明缺失角色，不假装报告完整。
- 搜索失败：`web_search` 返回结构化错误，searcher 用 `update_task` 标记失败；leader 可以重试一次或输出“不足证据”。
- mailbox 消费失败：不写 `consumed_at`，下一轮 worker 可以继续处理；工具执行要尽量幂等。
- SQLite busy：store 层设置 `busy_timeout`，写操作失败时返回可观察错误。
- leader 超时：通过 `-timeout` 控制整体等待时间，超时后发送 shutdown，并输出已完成的 source cards 和 report sections。

## Trace

trace 只放在命令边界：

- 输入：角色、team、agent、topic 字符数、search provider、worker 数量。
- 输出：source card 数量、report section 数量、最终报告长度、失败 worker 数量。

不把完整搜索结果、网页摘要、报告正文或 mailbox 全量内容写入 root trace。ADK 内部模型和工具调用继续交给 Eino callback 层展示。

## 测试

包级测试：

```text
env GOCACHE=/private/tmp/ai-designing-gocache go test ./action/research_swarm -count=1
```

覆盖：

- SQLite 初始化和跨 team 隔离。
- mailbox 投递、消费、重复消费保护。
- `FakeSearchClient` 稳定返回。
- source card 和 report section 写入读取。
- leader 在 fake spawner 下生成正确 worker 命令。
- worker 收到 shutdown 后退出。

命令测试：

```text
env GOCACHE=/private/tmp/ai-designing-gocache go test ./cmd/research-report-agent -count=1
```

覆盖：

- `-prepare-only` 不启动模型和 worker。
- leader 默认配置可构建。
- search provider 环境变量读取。
- JSON 输出摘要结构稳定。

真实多进程测试受环境变量保护：

```text
env CMD_E2E=1 GOCACHE=/private/tmp/ai-designing-gocache go test ./cmd/research-report-agent -run TestResearchReportAgentMultiProcessE2E -count=1 -v
```

## 不做的范围

第一版不实现浏览器抓取、网页正文抽取、引用去重评分、长期运行 daemon、tmux/iTerm pane 管理、权限审批 UI，也不让 worker 修改工作区文件。它只证明一件事：Eino ADK 可以用外部进程、SQLite mailbox、显式工具通信和可替换搜索接口，复现 Claude Code teammate/swarm 的核心协作模型。
