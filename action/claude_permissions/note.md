Claude Code 风格的权限关系分为三个层级：

```text
工具自己的 CheckPermissions(input, mode)
                    +
Rules / Hooks（全局限制与扩展）
                    ↓
Behavior（这一次调用怎么办）
```

可以理解为：

- `CheckPermissions`：每个工具根据自己的参数和业务语义判断风险
- `Mode`：本次 Agent 会话的权限策略
- `Behavior`：某一次具体工具调用的裁决结果

例如 `apply_feature_flag` 自己检查租户、开关名和当前 Mode：

```go
ToolPolicy{
    Name: "apply_feature_flag",
    Checker: ToolPermissionCheckerFunc(checkApplyFeatureFlagPermission),
}
```

工具可以针对同一调用给出不同 `Behavior`：

| 工具判断 | Mode | Behavior |
|---|---|---|
| 普通可逆开关变更 | `default` | `ask` |
| 普通可逆开关变更 | `acceptEdits` | `allow` |
| 任何开关变更 | `plan` | `deny` |
| 安全策略开关变更 | 任意模式 | `ask`，且不可 bypass |
| 受保护租户变更 | 任意模式 | `deny` |

再例如：

```text
send_change_notice
CheckPermissions = ask + BypassImmune
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

另外，工具自己的判断仍会和显式规则、Hook 一起参与最终裁决：

```text
CheckPermissions 得出工具意见
              ↓
显式 deny/ask、PreToolUse Hook、安全检查参与裁决
              ↓
最终 Behavior
```

例如即使工具自己返回 `allow`，显式 deny 仍可覆盖：

```go
PermissionRule{
    Behavior: PermissionDeny,
    ToolName: "apply_feature_flag",
}
```

最终仍然是 `deny`。

一句话总结：

```text
工具自己判断具体风险，Mode 和 Rules 调整边界，Behavior 是最终动作。
```

## Claude Code 如何决定命令是否进入 Sandbox

只要沙箱启用且命令没有被排除，Bash 会尝试在沙箱中运行；如果失败，再根据配置决定是否走沙箱外执行和正常权限流程。


## 显式规则配置现状

当前支持通过 Go 代码传入 `[]PermissionRule`，默认只有 `delete_tenant → deny`。尚不支持直接读取 Claude Code 风格配置：

```json
{"permissions":{"allow":["Bash(npm test:*)"],"ask":[],"deny":["Read(.env)"]}}
```

缺少的能力包括：JSON 配置加载、`Tool(pattern)` 解析、`*`/`**` 匹配和工具专属参数匹配。现有 `ArgumentContains` 只是对原始参数 JSON 做子串判断，不能视为完整兼容。

## 文件权限

File Read：
目录内默认 allow；目录外显式 allow，否则 ask。

File Edit/Write：
目录内也不默认 allow；
acceptEdits + 安全路径，或者显式 allow，才允许；
否则 ask。

Bash/PowerShell：
额外还可以参考 Sandbox write allowlist。