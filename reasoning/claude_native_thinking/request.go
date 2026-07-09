package claude_native_thinking

import (
	"errors"
	"strings"
)

// buildResponsesRequest 组装 Responses API 请求体，隐藏 OpenAI 与 Claude Code block 语义的差异。
func buildResponsesRequest(modelName string, req GenerateRequest) (map[string]any, error) {
	input := req.Input
	if len(input) == 0 {
		if len(req.PreviousOutputItems) > 0 {
			input = BuildNextInput(NormalizeTrajectoryOutput(req.PreviousOutputItems), req.UserPrompt)
		} else {
			input = buildPromptInput(req.UserPrompt)
		}
	}
	if len(input) == 0 {
		return nil, errors.New("responses input is required")
	}
	body := map[string]any{
		"model": modelName,
		"input": input,
		"store": false,
	}
	if instructions := strings.TrimSpace(req.SystemPrompt); instructions != "" {
		body["instructions"] = instructions
	}
	if req.PreviousResponseID != "" {
		body["previous_response_id"] = req.PreviousResponseID
	}
	if req.MaxTokens > 0 {
		body["max_output_tokens"] = req.MaxTokens
	}
	if reasoning := buildReasoning(req.Reasoning); len(reasoning) > 0 {
		body["reasoning"] = reasoning
	}
	if req.Reasoning.IncludeRedactedCOT {
		body["include"] = []string{"reasoning.encrypted_content"}
	}
	if req.Schema != nil {
		if strings.TrimSpace(req.SchemaName) == "" {
			return nil, errors.New("schema name is required when schema is provided")
		}
		body["text"] = map[string]any{
			"format": map[string]any{
				"type":   "json_schema",
				"name":   strings.TrimSpace(req.SchemaName),
				"schema": req.Schema,
				"strict": true,
			},
		}
	}
	return body, nil
}

func buildPromptInput(userPrompt string) []map[string]any {
	input := make([]map[string]any, 0, 1)
	if strings.TrimSpace(userPrompt) != "" {
		input = append(input, map[string]any{
			"role":    "user",
			"content": strings.TrimSpace(userPrompt),
		})
	}
	return input
}

func buildReasoning(config ThinkingConfig) map[string]any {
	if config.Type == ThinkingDisabled {
		return nil
	}
	reasoning := make(map[string]any, 2)
	if effort := strings.TrimSpace(config.Effort); effort != "" {
		reasoning["effort"] = effort
	}
	if summary := strings.TrimSpace(config.Summary); summary != "" {
		reasoning["summary"] = summary
	}
	return reasoning
}

// BuildNextInput 把上一轮 output item 原样转回下一轮 input，保留 encrypted reasoning 状态。
func BuildNextInput(previous []ResponseOutputItem, nextUserContent string) []map[string]any {
	items := make([]map[string]any, 0, len(previous)+1)
	for _, item := range previous {
		mapped := map[string]any{"type": item.Type}
		if item.ID != "" {
			mapped["id"] = item.ID
		}
		if item.Role != "" {
			mapped["role"] = item.Role
		}
		if item.Status != "" {
			mapped["status"] = item.Status
		}
		if item.EncryptedContent != "" {
			mapped["encrypted_content"] = item.EncryptedContent
		}
		if len(item.Summary) > 0 {
			mapped["summary"] = item.Summary
		}
		if len(item.Content) > 0 {
			mapped["content"] = item.Content
		}
		items = append(items, mapped)
	}
	if strings.TrimSpace(nextUserContent) != "" {
		items = append(items, map[string]any{
			"role":    "user",
			"content": strings.TrimSpace(nextUserContent),
		})
	}
	return items
}
