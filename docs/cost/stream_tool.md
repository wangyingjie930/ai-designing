先说结论：`content_block_stop` 本身几乎什么内容都没有，它只是通知 Claude Code：

> “编号为 `index` 的内容块已经传输完毕，可以处理了。”

## 一次工具调用实际长这样

假设 Claude 要调用：

```json
{
  "name": "Read",
  "input": {
    "file_path": "/tmp/a.txt"
  }
}
```

API 不会一次性返回完整 JSON，而是连续发送事件：

```text
content_block_start
{
  "type": "content_block_start",
  "index": 0,
  "content_block": {
    "type": "tool_use",
    "id": "toolu_01",
    "name": "Read",
    "input": {}
  }
}
```

Claude Code 收到后，先创建缓冲区：

```ts
contentBlocks[0] = {
  type: "tool_use",
  id: "toolu_01",
  name: "Read",
  input: ""
}
```

然后陆续收到 JSON 碎片：

```text
content_block_delta
{
  "type": "content_block_delta",
  "index": 0,
  "delta": {
    "type": "input_json_delta",
    "partial_json": "{\"file_path\":\"/tmp/"
  }
}
```

此时缓冲区变成：

```text
{"file_path":"/tmp/
```

再收到第二段：

```text
content_block_delta
{
  "type": "content_block_delta",
  "index": 0,
  "delta": {
    "type": "input_json_delta",
    "partial_json": "a.txt\"}"
  }
}
```

拼接后得到完整字符串：

```json
{"file_path":"/tmp/a.txt"}
```

最后才收到你问的事件：

```text
content_block_stop
{
  "type": "content_block_stop",
  "index": 0
}
```

注意：完整的工具参数不在 `content_block_stop` 里面。它只有 `index: 0`，相当于告诉程序：

```ts
const contentBlock = contentBlocks[0]

// 现在字符串完整了，可以解析
contentBlock.input = JSON.parse(contentBlock.input)
```

解析完成后，Claude Code 才得到真正可执行的工具调用：

```ts
{
  type: "tool_use",
  id: "toolu_01",
  name: "Read",
  input: {
    file_path: "/tmp/a.txt"
  }
}
```

整体过程可以记成：

```text
content_block_start
        ↓
创建 contentBlocks[0]
        ↓
content_block_delta
        ↓
追加第一段 JSON
        ↓
content_block_delta
        ↓
追加第二段 JSON
        ↓
content_block_stop(index=0)
        ↓
找到 contentBlocks[0]
        ↓
解析完整 JSON
        ↓
交给工具执行器
```

所以，之前“尽早启动工具”的准确含义是：

> 不需要等整条 assistant 消息的 `message_stop`，但必须等当前 `tool_use` 块的 `content_block_stop`。

它不会拿着尚未完整的 `partial_json` 就执行工具。

对应源码分别是：

- 创建工具调用缓冲区：[claude.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/api/claude.ts:1995)
- 追加 `partial_json`：[claude.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/api/claude.ts:2087)
- 收到 `content_block_stop` 后取出完整块：[claude.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/services/api/claude.ts:2171)
- 把字符串解析成工具参数对象：[messages.ts](/Users/wangyingjie/Documents/code/claude-code-source-study/src/utils/messages.ts:2651)

一句话记忆：

> `delta` 负责送货，`stop` 只负责通知“货送完了”。