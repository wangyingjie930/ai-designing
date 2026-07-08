举个“薪资异常审核”的完整流程：

用户输入：

```text
员工 P 的 6 月应发工资比 5 月高 18%，请判断是否可以自动放行。
```

Agent 实际执行大概是这样：

1. `cot-v2-agent` 读取 `.env`、问题文本、模型配置。

2. 调模型，但模型只能输出 `StepDraftList`，不能说“我已经验证过了”。  
   它大概会产出这些草稿：

```json
[
  {"kind":"observe","predicate":"pay_delta_percent","evidence_query":"读取 5 月和 6 月薪资快照"},
  {"kind":"derive","predicate":"bonus_delta_dominates","evidence_query":"计算薪资增量来源"},
  {"kind":"verify","predicate":"requires_approval_over_percent","evidence_query":"读取薪酬异常政策"},
  {"kind":"verify","predicate":"approval_matches","evidence_query":"读取 BONUS-18472 审批单"},
  {"kind":"decide","predicate":"release_decision","object":"auto_release"}
]
```

3. 程序开始自己收集证据。不是模型编，也不是默认文件。

比如 `observe/pay_delta_percent` 会请求：

```json
{
  "source": "retrieval",
  "step_id": "S1",
  "kind": "observe",
  "subject": "employee:P",
  "predicate": "pay_delta_percent",
  "evidence_query": "读取 5 月和 6 月薪资快照"
}
```

同时也可能请求日志源：

```json
{
  "source": "log",
  "step_id": "S1",
  "kind": "observe",
  "predicate": "pay_delta_percent"
}
```

证据源返回的是引用：

```json
{
  "evidence_refs": [
    {"source_id":"payroll_snapshot:P:2026-05","source_type":"payroll_snapshot","version":"v3"},
    {"source_id":"payroll_snapshot:P:2026-06","source_type":"payroll_snapshot","version":"v4"},
    {"source_id":"audit_log:payroll_snapshot_read:P:2026-06","source_type":"audit_log"}
  ]
}
```

4. 到 `derive/bonus_delta_dominates` 时，agent 会请求工具源，比如薪资差异计算器：

```json
{
  "source": "tool",
  "step_id": "S3",
  "kind": "derive",
  "predicate": "bonus_delta_dominates",
  "evidence_query": "计算薪资增量来源"
}
```

返回：

```json
{
  "action": "payroll_delta_calculator",
  "evidence_refs": [
    {"source_id":"tool_run:payroll_delta_calculator:P:2026-06","source_type":"tool_result"}
  ]
}
```

5. 程序把模型草稿 + 自己收集到的证据合成正式 `ClaimTrace`：

```text
S1 observe  P 6 月工资上涨 18%        evidence_refs=薪资快照+日志
S2 derive   增量主要来自季度奖金        action=payroll_delta_calculator
S3 verify   超过 15% 需要审批          evidence_refs=policy:v7 validator=evidence_exists
S4 verify   审批单匹配                 evidence_refs=approval+日志 validator=evidence_exists
S5 decide   可自动放行                 validator=decision_dependencies_gate
```

6. `TraceRunner` 开始逐步执行闸门：

- `observe`：必须有 `evidence_refs`
- `derive`：必须有 `action`
- `verify`：必须有 `evidence_refs + validator`
- `decide`：必须依赖前面都 passed

只要某一步没证据，比如审批单查不到，就会停在：

```text
stop_reason=step_failed:S4
final_decision=""
verified=false
```

如果全都通过，最后才会是：

```text
stop_reason=all_required_claims_verified
final_decision=auto_release
verified=true
```

本质上：模型只负责“提出要验证哪些命题”，agent 程序负责“查证据、跑工具、收日志、挂 validator、决定能不能通过”。