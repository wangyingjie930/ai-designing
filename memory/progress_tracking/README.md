# Progress Tracking

这个目录把 Python `ProgressTracker` 翻译成 Go + Eino ADK + SQLite 版本，并把它接到一个非 coding 场景 Agent：线下活动筹备。

核心对应关系：

- `TaskStatus` -> [types.go](types.go)
- `TaskItem` -> [types.go](types.go)
- `ProgressTracker.create_plan` -> [tracker.go](tracker.go) 的 `CreatePlan`
- `ProgressTracker.complete` -> [tracker.go](tracker.go) 的 `Complete`
- `ProgressTracker.fail` -> [tracker.go](tracker.go) 的 `Fail`
- `ProgressTracker.resumption_context` -> [tracker.go](tracker.go) 的 `ResumptionContext`
- JSON 文件 checkpoint -> [sqlite_store.go](sqlite_store.go) 的 SQLite 表 `progress_plans` / `progress_items`
- Eino ADK tools / Agent -> [agent.go](agent.go)

Agent 场景是线下活动筹备，不是 coding。用户只给自然语言目标，例如“筹备 80 人读书会”，Agent 必须先调用 `progress_resumption_context` 看当前进度；如果还没有计划，再调用 `progress_create_plan` 自己拆任务。后续用户说某项已完成或失败时，Agent 调用 `progress_complete_task` / `progress_fail_task` 写入 SQLite checkpoint。

确定性验证，不调用模型：

```bash
GOCACHE=/private/tmp/ai-designing-gocache go run ./cmd/progress-tracking-agent \
  -prepare-only \
  -db /tmp/event-progress.sqlite \
  -plan-id event-book-club \
  -items '确认场地;发布报名页;准备签到物料'
```

真实 Agent 运行。main 会先让 Agent 生成计划，再从 SQLite tracker 查询生成了哪些计划项，并按计划 index 自动触发前三轮，所以输出里能看到“计划产生 -> 查询生成计划 -> 遍历 3 轮 -> 编辑完成 -> 看看结果”：

```bash
GOCACHE=/private/tmp/ai-designing-gocache go run ./cmd/progress-tracking-agent \
  -db /tmp/event-progress.sqlite \
  -plan-id event-book-club \
  -message-file memory/progress_tracking/examples/event_goal.txt
```

真实 Agent 模式会安装 `observability/cozeloop` 的 Eino 官方 callback；未配置 `COZELOOP_*` 时自动 no-op，不需要额外 trace flag。命令级 root span 只上报 `db_path`、`plan_id`、触发轮数和结果摘要，具体 ADK/model/tool 调用由 Eino callback 自动形成 trace。

模型配置来自 `.env` 或环境变量：

```bash
OPENAI_API_KEY=...
LLM_MODEL=...
LLM_OPENAI_BASE_URL=...
```

这里没有内置任务答案：DB 路径、plan id、用户消息、任务列表都来自命令行、文件或环境变量；代码只提供 tracker、SQLite checkpoint 和 ADK 工具边界。
