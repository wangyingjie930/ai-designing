package critique

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ParseCritiqueResult 从 critic 输出中解析结构化结果，兼容纯 JSON 和 Markdown JSON 代码块。
func ParseCritiqueResult(text string) (CritiqueResult, error) {
	jsonText, err := extractJSONObject(text)
	if err != nil {
		return CritiqueResult{}, err
	}
	var result CritiqueResult
	if err := json.Unmarshal([]byte(jsonText), &result); err != nil {
		return CritiqueResult{}, fmt.Errorf("parse critique json: %w", err)
	}
	return normalizeCritiqueResult(result), nil
}

// normalizeCritiqueResult 清理 critic 结果里的空白项，并把分数压回可比较范围。
func normalizeCritiqueResult(result CritiqueResult) CritiqueResult {
	result.Score = normalizeScore(result.Score)
	result.Issues = trimStringSlice(result.Issues)
	result.Suggestions = trimStringSlice(result.Suggestions)
	return result
}

// trimStringSlice 去掉空白字符串，避免反馈文本里出现无意义项目。
func trimStringSlice(values []string) []string {
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			trimmed = append(trimmed, value)
		}
	}
	return trimmed
}

// extractJSONObject 只抽取第一个完整 JSON object，避免模型偶发前后缀影响结构化解析。
func extractJSONObject(text string) (string, error) {
	trimmed := strings.TrimSpace(stripJSONFence(text))
	if trimmed == "" {
		return "", errors.New("empty critique response")
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed, nil
	}
	start := strings.Index(trimmed, "{")
	if start < 0 {
		return "", errors.New("critique response does not contain json object")
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
					return "", errors.New("extracted critique json object is invalid")
				}
				return candidate, nil
			}
		}
	}
	return "", errors.New("critique json object is incomplete")
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
