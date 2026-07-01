# Eino ADK Skill Middleware 三种模式

这个包演示 CloudWeGo Eino ADK `skill.NewMiddleware` 的三种 Skill 使用模式。官方接口形态是：创建 `skill.Backend`，再用 `skill.NewMiddleware(ctx, &skill.Config{...})` 生成 `adk.ChatModelAgentMiddleware`，最后挂到 `adk.ChatModelAgentConfig.Handlers`。

参考文档：[CloudWeGo Skill Middleware](https://www.cloudwego.io/zh/docs/eino/core_modules/eino_adk/eino_adk_chatmodelagentmiddleware/middleware_skill/)

## 三种场景

| 模式 | Skill | 场景 | 为什么适合 |
| --- | --- | --- | --- |
| inline | `support_reply_inline` | 客服值班长回复规范 | 回复风格、升级边界是短规则，直接进入主 agent 上下文最省成本。 |
| `fork_with_context` | `compensation_review_with_context` | 补偿策略专家复核 | 补偿判断必须看见前文承诺、重复失约等事实，所以要携带主对话上下文给专家子 agent。 |
| `fork` | `compliance_review_isolated` | 合规风险隔离审查 | 合规审查要避免继承主对话里的未验证承诺，因此使用干净上下文。 |

## Skill 文件

三个 Skill 都是真实文件：

```text
reasoning/skillmode/skills/
├── support_reply_inline/SKILL.md
├── compensation_review_with_context/SKILL.md
└── compliance_review_isolated/SKILL.md
```

## fork 模式输入边界

`fork` 模式的子 agent 不会继承主对话历史，只会看到 skill middleware 传入的内容。为了既保持隔离，又让子 agent 有可处理的业务文本，本示例给官方 `skill` 工具追加了 `task` 参数，并通过 `BuildContent` 把它拼到 `SKILL.md` 后面：

```text
当前任务/待处理文本：
<本轮用户请求或待审查客服回复>
```

所以合规隔离审查不是靠读取主会话，而是靠主 agent 在调用 skill 时显式传入本轮审查文本。

## 代码结构

- `skills.go`：定义场景元信息，并把 `reasoning/skillmode/skills` 接入官方 Skill Middleware。
- `local_filesystem.go`：把本地目录适配为 Eino `filesystem.Backend`，供 `skill.NewBackendFromFilesystem` 扫描。
- `agent.go`：创建带 Skill Middleware 的 `adk.ChatModelAgent` 和 runner。
- `hub.go`：实现 `skill.AgentHub`，给 `fork` / `fork_with_context` 创建专家子 agent。
- `cmd/skill-mode-agent`：命令入口，负责 `.env`、模型创建、CozeLoop 安装和简洁 root trace。

## 运行

只看默认输入，不调用模型：

```bash
go run ./cmd/skill-mode-agent -prepare-only -mode inline
go run ./cmd/skill-mode-agent -prepare-only -mode fork_with_context
go run ./cmd/skill-mode-agent -prepare-only -mode fork
```

真实调用模型：

```bash
go run ./cmd/skill-mode-agent -mode fork_with_context
```

可用环境变量：

- `OPENAI_API_KEY` / `LLM_OPENAI_API_KEY` / `LLM_API_KEY`
- `LLM_MODEL` / `OPENAI_MODEL`
- `LLM_OPENAI_BASE_URL` / `OPENAI_BASE_URL` / `OPENAI_API_BASE` / `OPENAI_API_BASE_URL`
- `COZELOOP_*`，沿用仓库现有 `observability/cozeloop` 配置
- `SKILL_MODE_AGENT_MODE`
- `SKILL_MODE_AGENT_MAX_ITERATIONS`

## Trace 边界

`cmd/skill-mode-agent/trace.go` 只上报：

- `skill_mode`
- `scenario`
- `skill_name`
- `query_chars`
- `answer_chars`

完整客诉内容、完整 skill 内容和模型回复正文不进入命令级 root trace。Eino/CozeLoop 仍会通过框架 callback 展示 agent、model、tool 的运行树。
