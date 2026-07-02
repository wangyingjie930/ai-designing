package selfheal

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseFixProposal 解析模型输出的补丁建议；非 JSON 文本会被保守地作为 FixDiff 使用。
func ParseFixProposal(text string) (FixProposal, error) {
	cleaned := strings.TrimSpace(text)
	if cleaned == "" {
		return FixProposal{}, fmt.Errorf("fix proposal is empty")
	}
	jsonText := extractJSONObject(cleaned)
	if jsonText == "" {
		return FixProposal{FixDiff: cleaned}, nil
	}
	var proposal FixProposal
	if err := json.Unmarshal([]byte(jsonText), &proposal); err != nil {
		return FixProposal{}, fmt.Errorf("parse fix proposal: %w", err)
	}
	proposal.Summary = strings.TrimSpace(proposal.Summary)
	proposal.FixDiff = strings.TrimSpace(proposal.FixDiff)
	if proposal.FixDiff == "" {
		return FixProposal{}, fmt.Errorf("fix_diff is required")
	}
	return proposal, nil
}

// ParseCriticVerdict 解析风险评审结果，评审器必须返回明确 JSON，避免误放行。
func ParseCriticVerdict(text string) (CriticVerdict, error) {
	jsonText := extractJSONObject(strings.TrimSpace(text))
	if jsonText == "" {
		return CriticVerdict{}, fmt.Errorf("critic verdict must be json")
	}
	var verdict CriticVerdict
	if err := json.Unmarshal([]byte(jsonText), &verdict); err != nil {
		return CriticVerdict{}, fmt.Errorf("parse critic verdict: %w", err)
	}
	verdict.Reason = strings.TrimSpace(verdict.Reason)
	return verdict, nil
}

// extractJSONObject 从普通文本或 Markdown 代码块中取第一个 JSON object。
func extractJSONObject(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return ""
	}
	return strings.TrimSpace(text[start : end+1])
}
