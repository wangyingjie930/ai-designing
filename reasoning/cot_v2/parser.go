package cot_v2

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ParseStepDraftList 从模型输出中解析 StepDraftList，兼容纯 JSON 和被代码块包裹的 JSON。
func ParseStepDraftList(text string) (StepDraftList, error) {
	jsonText, err := extractJSONObject(text)
	if err != nil {
		return StepDraftList{}, err
	}
	var drafts StepDraftList
	if err := json.Unmarshal([]byte(jsonText), &drafts); err != nil {
		return StepDraftList{}, fmt.Errorf("parse step draft json: %w", err)
	}
	if len(drafts.Steps) == 0 {
		return StepDraftList{}, errors.New("step draft list has no steps")
	}
	for index := range drafts.Steps {
		drafts.Steps[index].ClaimText = strings.TrimSpace(drafts.Steps[index].ClaimText)
		drafts.Steps[index].SuggestedSubject = strings.TrimSpace(drafts.Steps[index].SuggestedSubject)
		drafts.Steps[index].SuggestedPredicate = strings.TrimSpace(drafts.Steps[index].SuggestedPredicate)
		drafts.Steps[index].SuggestedEvidenceQuery = strings.TrimSpace(drafts.Steps[index].SuggestedEvidenceQuery)
		if drafts.Steps[index].ClaimText == "" {
			return StepDraftList{}, fmt.Errorf("step draft %d claim_text is empty", index+1)
		}
	}
	return drafts, nil
}

func extractJSONObject(text string) (string, error) {
	trimmed := strings.TrimSpace(stripJSONFence(text))
	if trimmed == "" {
		return "", errors.New("empty step draft response")
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed, nil
	}
	start := strings.Index(trimmed, "{")
	if start < 0 {
		return "", errors.New("step draft response does not contain json object")
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
					return "", errors.New("extracted step draft json object is invalid")
				}
				return candidate, nil
			}
		}
	}
	return "", errors.New("step draft json object is incomplete")
}

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
