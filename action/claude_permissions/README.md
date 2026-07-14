# Claude Code 风格的非 Coding 权限 Agent

这个目录把 Claude Code 的权限执行链迁移到 SaaS 生产变更场景。它不是关键词 guardrail，也不会连接真实生产系统；所有业务动作都由内存执行器模拟。

## 对应关系

| Claude Code 行为 | 本实现 |
| --- | --- |
| 工具整体 deny 后不暴露给模型 | `BeforeModelRewriteState` 过滤 `ToolInfo` |
| `PreToolUse` | `PreToolUseHook` 检查、拒绝或更新参数 |
| 权限规则与 permission mode | `PermissionEngine` + `default/plan/acceptEdits/dontAsk/bypassPermissions` |
| 权限确认暂停当前工具调用 | `PermissionBroker.Request` 阻塞，`Resolve` 恢复同一次调用 |
| `PermissionRequest` Hook / 用户确认 | `PermissionCoordinator` 让 Hook 和 UI 竞争，首个响应生效 |
| `PostToolUse` | `PostToolUseHook` 审计或改写成功输出 |
| `Stop` | `StopHook` 检查最终助手回复 |

安全硬限制始终优先：`delete_tenant` 即使在 `bypassPermissions` 下也会被拒绝。显式 `deny/ask` 规则也不能被 `PreToolUse allow` 绕过。

## 运行

先在仓库根目录配置 `.env`：

```dotenv
OPENAI_API_KEY=...
LLM_MODEL=...
LLM_OPENAI_BASE_URL=http://localhost:8317
CLAUDE_PERMISSION_MODE=default
```

查看配置但不调用模型：

```bash
go run ./cmd/claude-permission-agent -prepare-only
```

交互运行：

```bash
go run ./cmd/claude-permission-agent \
  -message '检查 TENANT-42，然后关闭 invoice_v2 开关'
```

写操作或外部动作出现时，当前工具调用会暂停。终端可选择：

- `y`：按原参数允许；
- `n`：拒绝，模型收到结构化拒绝结果；
- `e`：输入一行新 JSON，按更新后的参数恢复原调用。

`-approve-all` 只用于本地演示。`delete_tenant` 仍不会被执行。

## 测试

```bash
GOCACHE=/private/tmp/ai-designing-gocache \
  go test ./action/claude_permissions ./cmd/claude-permission-agent -count=1
```
