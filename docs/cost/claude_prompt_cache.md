## 先记这个

这个“边界”只是告诉 Claude API：

> 前面这部分系统提示很稳定，可以缓存；后面这部分经常变化，每次重新处理。

它不是说：

```text
边界前权限高
边界后权限低
```

## 具体长什么样

Claude Code 内部先组装：

```text
系统提示数组：

[固定部分]
- 你是 Claude Code
- 安全规范
- 工具使用规则
- 编码规范

[SYSTEM_PROMPT_DYNAMIC_BOUNDARY]

[动态部分]
- 当前工作目录
- 当前日期
- 当前语言设置
- Memory 信息
- 当前连接的 MCP
```

源码组装位置见 [prompts.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/constants/prompts.ts:557)。

随后 Claude Code 找到边界，将其拆成两块：

```ts
staticBlocks  = 边界之前
dynamicBlocks = 边界之后
```

边界标记本身会被删除，不会发送给模型，见 [api.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/api.ts:362)。

最终 API 请求近似为：

```json
{
  "system": [
    {
      "type": "text",
      "text": "Claude Code 身份、安全规范、工具规范……",
      "cache_control": {
        "type": "ephemeral",
        "scope": "global"
      }
    },
    {
      "type": "text",
      "text": "当前目录、日期、Memory、MCP 信息……"
    }
  ],
  "messages": [
    {
      "role": "user",
      "content": "用户的问题"
    }
  ]
}
```

`cache_control` 添加逻辑见 [claude.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/api/claude.ts:3213)，最终两块一起放进 `system` 字段，见 [claude.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/api/claude.ts:1699)。

## 为什么要切开

假设连续调用模型三次：

```text
第一轮：
固定系统提示 A + 当前环境 B1 + 用户问题

第二轮：
固定系统提示 A + 当前环境 B2 + 工具结果

第三轮：
固定系统提示 A + 当前环境 B3 + 用户继续提问
```

`A` 每次完全相同，Claude API 可以复用它的计算结果；只重新处理发生变化的 `B1/B2/B3`。

效果是：

- 减少重复计算
- 降低延迟
- 降低 Prompt 处理成本
- 避免动态内容变化导致整个系统提示缓存失效

模型逻辑上仍然能看到完整的 `A + B`。缓存不是删除内容，也不是长期记忆。

## 为什么说“不影响安全优先级”

因为最终结构是：

```text
system:
  - 固定系统提示
  - 动态系统提示

messages:
  - 用户消息
```

固定部分和动态部分都在 `system` 字段，所以都高于普通用户消息。

如果两部分发生冲突：

```text
固定 system：禁止执行 X
动态 system：允许执行 X
```

缓存边界不会负责裁决谁优先。两段同属 `system`，模型只能根据文本顺序、具体措辞和模型自身冲突处理能力判断。

因此：

> `cache_control` 决定“这段计算能不能复用”，消息角色决定“这段指令是什么级别”。