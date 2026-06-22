package cot

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ParseChain 从模型输出中解析 ChainOfThought，优先接受纯 JSON，也兼容被包在代码块里的 JSON。
func ParseChain(text string) (ChainOfThought, error) {
	jsonText, err := extractJSONObject(text)
	if err != nil {
		return ChainOfThought{}, err
	}
	var chain ChainOfThought
	if err := json.Unmarshal([]byte(jsonText), &chain); err != nil {
		return ChainOfThought{}, fmt.Errorf("parse chain json: %w", err)
	}
	return normalizeParsedChain(chain)
}

// normalizeParsedChain 补齐稳定 step number，并拒绝空步骤或空最终答案。
func normalizeParsedChain(chain ChainOfThought) (ChainOfThought, error) {
	if len(chain.Steps) == 0 {
		return ChainOfThought{}, errors.New("chain has no steps")
	}
	normalized := ChainOfThought{
		Steps:       make([]ReasoningStep, 0, len(chain.Steps)),
		FinalAnswer: strings.TrimSpace(chain.FinalAnswer),
	}
	if normalized.FinalAnswer == "" {
		return ChainOfThought{}, errors.New("chain final_answer is empty")
	}
	for index, step := range chain.Steps {
		content := strings.TrimSpace(step.Content)
		if content == "" {
			return ChainOfThought{}, fmt.Errorf("step %d content is empty", index+1)
		}
		normalized.Steps = append(normalized.Steps, ReasoningStep{
			StepNumber: index + 1,
			Content:    content,
			Confidence: normalizeConfidence(step.Confidence),
		})
	}
	return normalized, nil
}

// extractJSONObject 只抽取第一个完整 JSON object，避免模型偶发前后缀影响结构化解析。
func extractJSONObject(text string) (string, error) {
	trimmed := strings.TrimSpace(stripJSONFence(text))
	if trimmed == "" {
		return "", errors.New("empty chain response")
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed, nil
	}
	start := strings.Index(trimmed, "{")
	if start < 0 {
		return "", errors.New("chain response does not contain json object")
	}
	inString := false
	escaped := false
	depth := 0
	for i := start; i < len(trimmed); i++ {
		ch := trimmed[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				candidate := strings.TrimSpace(trimmed[start : i+1])
				if !json.Valid([]byte(candidate)) {
					return "", errors.New("extracted json object is invalid")
				}
				return candidate, nil
			}
		}
	}
	return "", errors.New("json object is incomplete")
}

// stripJSONFence 处理模型把 JSON 放进 ```json 代码块的情况。
func stripJSONFence(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return trimmed
	}
	if strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		lines = lines[1 : len(lines)-1]
	} else {
		lines = lines[1:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// marshalChainForPrompt 把当前链路压成 verifier 可读的稳定文本。
func marshalChainForPrompt(steps []ReasoningStep) string {
	lines := make([]string, 0, len(steps))
	for _, step := range steps {
		lines = append(lines, fmt.Sprintf("Step %d: %s", step.StepNumber, step.Content))
	}
	return strings.Join(lines, "\n")
}
