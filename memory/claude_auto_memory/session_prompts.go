package claudeautomemory

import "strings"

const sessionMemoryUpdateMarker = "[SESSION_MEMORY_UPDATE]"

var sessionMemoryHeaders = []string{
	"# 会话标题",
	"# 当前状态",
	"# 任务要求",
	"# 重要文件与函数",
	"# 工作流程",
	"# 错误与修正",
	"# 系统结构",
	"# 经验结论",
	"# 关键结果",
	"# 工作日志",
}

var defaultSessionMemoryTemplate = strings.TrimSpace(`
# 会话标题
_用 5 到 10 个词概括本次会话。_

# 当前状态
_当前正在处理什么、未完成事项和下一步。_

# 任务要求
_用户要求构建或回答什么，以及关键设计决定。_

# 重要文件与函数
_重要文件、函数及其用途。_

# 工作流程
_常用命令、执行顺序和结果解释。_

# 错误与修正
_遇到的错误、修复方式、用户纠正和失败方案。_

# 系统结构
_重要组件及其协作关系。_

# 经验结论
_有效做法、无效做法和应避免事项。_

# 关键结果
_用户要求的关键输出或结论。_

# 工作日志
_按时间顺序简要记录已执行步骤。_
`) + "\n"

// sessionMemorySystemPrompt 要求模型只维护当前任务连续性，不生成长期偏好记忆。
func sessionMemorySystemPrompt() string {
	return strings.TrimSpace(sessionMemoryUpdateMarker + `
你是独立运行的 Session Memory 更新器，不负责回答用户问题，也不负责长期记忆。

请根据当前摘要和新增真实对话，输出一份完整的 Markdown 会话摘要。必须保留以下十个一级标题，顺序和文字不能改变：
- # 会话标题
- # 当前状态
- # 任务要求
- # 重要文件与函数
- # 工作流程
- # 错误与修正
- # 系统结构
- # 经验结论
- # 关键结果
- # 工作日志

重点更新“当前状态”和“下一步”，保留可以帮助压缩后继续任务的具体文件、函数、命令和错误。删除已经失效或被纠正的状态，不要写入系统提示、长期记忆维护过程、API key、token 或其他凭据。

只输出完整 Markdown 摘要，不要解释，不要使用代码围栏。`)
}
