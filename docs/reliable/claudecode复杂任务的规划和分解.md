## 先说结论

**不能完全解决，只解决了一半。**

当前 Claude Code 已经能处理“任务图执行”：

- 记录串行依赖
- 自动阻止未满足依赖的任务
- 并行执行彼此独立的任务
- 任务失败后让模型重新规划

但它还不能稳定解决“任务图生成”：

- 应该拆多细
- 哪些依赖判断正确
- 条件分支如何表达
- 如何得到成本最低的执行顺序
- 如何避免错误依赖或循环依赖

可以概括为：

> Claude Code 已经有一个简单的 DAG 执行器，但没有可靠的 DAG 规划器。

## 对照图片里的问题

| 图片中的难点 | 当前 Claude Code |
|---|---|
| 分解粒度 | **没有真正解决**，主要靠模型判断和 Prompt 经验规则 |
| 串行依赖 | **基本解决**，支持 `blockedBy/blocks` |
| 独立任务并行 | **基本解决**，支持并行工具、subagent、Agent Team |
| 条件依赖 | **没有原生结构化支持**，靠模型运行时修改任务 |
| 失败后换方案 | **部分支持**，通过结果回灌重新规划 |
| 自动寻找最优执行计划 | **没有** |
| 循环依赖检测 | **源码里没看到完整检测** |
| 调度状态绝对可靠 | **不能保证**，官方也承认任务状态可能滞后 |

### 1. 分解粒度：仍然依赖 LLM

`TaskCreate` 的提示只是给出经验规则：

- 三个以上独立步骤，可以创建任务
- 少于三个简单步骤，不要创建任务

见 [TaskCreate Prompt](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/TaskCreateTool/prompt.ts:19)。

这不是一个能够计算“最佳粒度”的算法。模型仍可能：

- 把任务拆得过粗
- 拆出大量没有价值的小任务
- 漏掉中间依赖
- 过早并行存在隐含依赖的任务

Plan Agent 虽然被要求识别依赖和顺序，但本质上还是 Prompt 指导模型做规划。见 [planAgent.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/tools/AgentTool/built-in/planAgent.ts:50)。

### 2. 普通前置依赖：已经有硬约束

任务结构里明确存在：

```ts
{
  status: "pending" | "in_progress" | "completed",
  blocks: string[],
  blockedBy: string[]
}
```

见 [tasks.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/tasks.ts:69)。

认领任务时，会检查它依赖的任务是否都已完成；没有完成就返回 `blocked`，不会执行。见 [tasks.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/tasks.ts:574)。

Agent Team 中，依赖完成后，其他 teammate 可以自动认领刚刚解锁的任务。官方文档也明确说明了共享任务列表、依赖解锁和文件锁机制，但 Agent Team 仍是实验功能，默认关闭，并存在任务状态滞后等限制。[Claude Code Agent Teams 官方文档](https://code.claude.com/docs/en/agent-teams)

### 3. 独立步骤并行：可以做到

比如图片里的竞品分析，可以表达成：

```text
A 搜索竞品列表
├─ B1 分析竞品 1
├─ B2 分析竞品 2
└─ B3 分析竞品 3
       ↓
C 汇总报告
```

其中：

- `B1/B2/B3 blockedBy A`
- `C blockedBy B1/B2/B3`
- A 完成后，三个分析任务并行
- 三个分析完成后，C 才能开始

系统 Prompt 也明确要求无依赖调用并行、有依赖调用串行。见 [prompts.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/constants/prompts.ts:304)。

Claude Code 可以通过 subagent、Agent Team 或 worktree 并行，但官方也建议：依赖很多、需要修改同一文件的任务更适合单会话或受控 subagent，不适合盲目开 Agent Team。[并行 Agent 官方说明](https://code.claude.com/docs/en/agents)

### 4. 条件依赖：仍然是明显缺口

图片里的：

```text
如果找到竞品信息 → 分析
如果找不到 → 跳过或换一种搜索方式
```

TaskList 目前没有类似下面的原生字段：

```json
{
  "condition": "search_result.count > 0",
  "onSuccess": "analyze",
  "onFailure": "fallback_search",
  "retryPolicy": {},
  "skipPolicy": {}
}
```

它只有三个状态和普通 `blockedBy` 数组。

因此条件分支实际是：

```text
搜索完成
→ 模型读取结果
→ 模型判断有没有数据
→ 临时创建 fallback 任务，或者删除/跳过分析任务
```

也就是“模型动态改图”，不是调度器按结构化条件执行。

## 最准确的判断

当前 Claude Code：

- **能可靠执行模型已经正确建立的简单依赖图。**
- **能对独立任务进行并行调度。**
- **能在失败后通过 ReAct 循环修改计划。**
- **不能保证最初的任务拆分和依赖关系正确。**
- **没有完整的条件 DAG、优先级、关键路径、成本优化和循环检测。**

所以图片最后那句“目前模型还远做不到完美”仍然成立。Claude Code 的提升主要来自：**把模型生成的计划放进结构化 TaskList 中执行和约束**，而不是已经拥有一个确定性的复杂工作流规划器。