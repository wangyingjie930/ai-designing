# Chain-of-Thought Verifier Agent

这个包是一个线性 CoT 校验模式：先生成可审计的显性推理步骤，再逐步校验每一步是否被原问题和前序步骤支持。

核心 Agent 流程：

```mermaid
flowchart TD
    A["用户问题"] --> B["ADK Runner"]
    B --> C["CoT Verifier Agent"]
    C --> D["ReasonWithCOT<br/>生成显性推理链"]
    D --> E["ChainOfThought<br/>steps + final_answer"]
    E --> F["WeakestStep<br/>找最低 confidence 步骤"]
    E --> G["VerifyChain<br/>逐步校验每个 step"]
    G --> H["Verifier 输入<br/>原问题 + 前序步骤 + 当前步骤"]
    H --> I{"Verifier 输出"}
    I -->|VALID| J["步骤通过"]
    I -->|INVALID| K["记录 VerificationIssue"]
    J --> L{"还有下一步?"}
    K --> L
    L -->|有| G
    L -->|没有| M{"issues=0?"}
    M -->|是| N["verified=true<br/>输出 final_answer"]
    M -->|否| O["verified=false<br/>输出 final_answer + issues"]
```

记忆点：这个模式不是直接相信第一轮推理链，而是把 `steps` 拆开逐步复核；每一步都必须能被“原问题 + 前序步骤”支撑，否则进入 `issues`。

命令入口：

```bash
go run ./cmd/cot-verifier-agent -prepare-only
go run ./cmd/cot-verifier-agent -json
```
