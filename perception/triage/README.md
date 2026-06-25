# 多租户 SaaS 客服分诊 Agent

这个包实现的是 Context Triage 模式：原始客服请求先进入 ADK Agent，由 Agent 调用工具完成上下文分诊，再基于被选中的证据回答。

核心 Agent 流程：

```mermaid
flowchart TD
    A["原始 SaaS 客服请求<br/>runtime + tenant profile + message"] --> B["ADK Runner"]
    B --> C["SaaS Triage Agent<br/>ChatModelAgent"]
    C --> D["必须先调用工具<br/>prepare_saas_triage_context"]
    D --> E["SaaSTriagePlanner<br/>校验 runtime + tenant 隔离"]
    E --> F["Context Items<br/>映射成 P0 / P1 / P2 / P3"]
    F --> G["ContextTriage<br/>按优先级 + 错误保护 + token budget 装箱"]
    G --> H["PreparedTriageContext<br/>selected / deferred / dropped / health / decision"]
    H --> I["Agent 基于 selected 证据回答"]
    I --> J{"需要 P3 原文?"}
    J -->|否| K["输出分诊结论<br/>证据 + 处置建议 + 升级判断"]
    J -->|是| L["read_tenant_handle<br/>同 tenant_id 校验后读取 handle"]
    L --> K
```

记忆点：这个模式不是在外部 helper 里先替 Agent 做感知和分类；raw 请求先进入 ADK loop，Agent 必须通过 `prepare_saas_triage_context` 得到可用上下文。P0/P1/P2 才能直接进回答，P3 默认只暴露租户级 handle，需要时再用 `read_tenant_handle` 同租户读取。

命令入口：

```bash
env CMD_E2E=1 GOCACHE=/private/tmp/ai-designing-gocache go test ./cmd/triage-agent -run TestTriageAgentEndToEnd -count=1 -v
```
