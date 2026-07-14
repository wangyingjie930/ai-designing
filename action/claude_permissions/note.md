三者处于不同层级：

```text
Kind（工具是什么）
        +
Mode（当前怎么管）
        +
Rules / Hooks（特殊限制）
        ↓
Behavior（这一次调用怎么办）
```

可以理解为：

- `Kind`：工具自身的安全属性
- `Mode`：本次 Agent 会话的权限策略
- `Behavior`：某一次具体工具调用的裁决结果

例如 `apply_feature_flag` 的 `Kind` 固定是 `edit`：

```go
ToolPolicy{
    Name: "apply_feature_flag",
    Kind: ToolKindEdit,
}
```

但不同 `Mode` 会产生不同 `Behavior`：

| Kind | Mode | Behavior |
|---|---|---|
| `edit` | `default` | `ask` |
| `edit` | `acceptEdits` | `allow` |
| `edit` | `plan` | `deny` |
| `edit` | `dontAsk` | `deny` |
| `edit` | `bypassPermissions` | `allow` |

再例如：

```text
send_change_notice
Kind = external
Mode = acceptEdits
最终 Behavior = ask
```

因为 `acceptEdits` 只自动接受内部可逆修改，不自动接受对外发送消息。

三个 `Behavior` 的含义是：

```go
PermissionAllow // 直接执行工具
PermissionAsk   // 暂停工具，等待人工审批
PermissionDeny  // 不执行工具，向模型返回拒绝结果
```

其中 `ask` 不是最终结束状态，它会进入审批流程：

```text
Behavior = ask
      ↓
暂停当前工具调用
      ↓
人工或 PermissionRequestHook 响应
      ↓
allow → 恢复并执行原工具
deny  → 不执行，返回结构化拒绝结果
```

另外，`Kind + Mode` 只是默认计算规则，显式规则和 Hook 还可以改变结果：

```text
Kind + Mode 得出默认 Behavior
           ↓
显式 deny/ask、PreToolUse Hook、安全硬限制参与裁决
           ↓
最终 Behavior
```

比如即使：

```text
Kind = edit
Mode = bypassPermissions
默认 Behavior = allow
```

如果存在显式规则：

```go
PermissionRule{
    Behavior: PermissionDeny,
    ToolName: "apply_feature_flag",
}
```

最终仍然是 `deny`。

一句话总结：

```text
Kind 是工具属性，Mode 是管理方式，Behavior 是最终动作。
```