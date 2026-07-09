// Package claude_cot 用 OpenAI Responses API 复刻 Claude Code 的 native thinking/redacted thinking 边界。
//
// OpenAI 不暴露原始 reasoning tokens；本包把 reasoning.summary 映射为可见 thinking，
// 把 reasoning.encrypted_content 映射为 redacted_thinking，并保留 output item 供下一轮请求续接。
package claude_native_thinking
