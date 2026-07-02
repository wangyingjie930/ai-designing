package guardrail

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Policy 是预编译后的安全策略，运行时只做快速匹配和裁决。
type Policy struct {
	allowedTools      map[string]bool
	blockedPatterns   []*regexp.Regexp
	autoApprove       map[RiskLevel]bool
	requireHuman      map[RiskLevel]bool
	sensitivePatterns []*regexp.Regexp
}

// DefaultSafetyPolicy 返回与 Python GuardrailSandwich 等价的保守默认策略。
func DefaultSafetyPolicy() SafetyPolicy {
	return SafetyPolicy{
		AutoApprove: []RiskLevel{RiskLevelLow},
		RequireHuman: []RiskLevel{
			RiskLevelHigh,
			RiskLevelCritical,
		},
		SensitivePatterns: []string{
			`(?i)(password|secret|api[_-]?key|token)\s*[:=]\s*\S+`,
			`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`,
			`\b\d{3}-\d{2}-\d{4}\b`,
		},
	}
}

// NewPolicy 编译策略中的正则和集合，提前暴露配置错误。
func NewPolicy(policy SafetyPolicy) (*Policy, error) {
	policy = normalizePolicy(policy)
	blockedPatterns, err := compilePatterns("blocked pattern", policy.BlockedPatterns)
	if err != nil {
		return nil, err
	}
	sensitivePatterns, err := compilePatterns("sensitive pattern", policy.SensitivePatterns)
	if err != nil {
		return nil, err
	}
	return &Policy{
		allowedTools:      buildStringSet(policy.AllowedTools),
		blockedPatterns:   blockedPatterns,
		autoApprove:       buildRiskSet(policy.AutoApprove),
		requireHuman:      buildRiskSet(policy.RequireHuman),
		sensitivePatterns: sensitivePatterns,
	}, nil
}

// Evaluate 执行输入过滤，返回本次工具调用是否允许继续。
func (p *Policy) Evaluate(ctx context.Context, toolName string, argumentsInJSON string, classifier RiskClassifier, approver Approver) (Verdict, error) {
	if p == nil {
		return Verdict{}, fmt.Errorf("policy is required")
	}
	if classifier == nil {
		classifier = DefaultRiskClassifier{}
	}
	risk := classifier.ClassifyRisk(toolName, argumentsInJSON)
	normalizedName := normalizeToolName(toolName)
	if len(p.allowedTools) > 0 && !p.allowedTools[normalizedName] {
		return Verdict{Approved: false, RiskLevel: risk, Reason: "tool is not in allowed_tools"}, nil
	}
	inspectionText := normalizedName + "\n" + argumentsInJSON
	for _, pattern := range p.blockedPatterns {
		if pattern.MatchString(inspectionText) {
			return Verdict{Approved: false, RiskLevel: risk, Reason: "blocked pattern matched: " + pattern.String()}, nil
		}
	}
	if p.autoApprove[risk] {
		return Verdict{Approved: true, RiskLevel: risk, Reason: "risk level is auto approved"}, nil
	}
	if p.requireHuman[risk] {
		if approver == nil {
			return Verdict{Approved: false, RiskLevel: risk, Reason: "human approval is required but no approver is configured"}, nil
		}
		decision, err := approver.Approve(ctx, ApprovalRequest{
			ToolName:        toolName,
			ArgumentsInJSON: argumentsInJSON,
			RiskLevel:       risk,
			Reason:          "risk level requires human approval",
		})
		if err != nil {
			return Verdict{}, err
		}
		if !decision.Approved {
			return Verdict{Approved: false, RiskLevel: risk, Reason: firstNonEmpty(decision.Reason, "human approval denied")}, nil
		}
		return Verdict{Approved: true, RiskLevel: risk, Reason: firstNonEmpty(decision.Reason, "human approval granted")}, nil
	}
	return Verdict{Approved: false, RiskLevel: risk, Reason: "risk level is not auto approved"}, nil
}

// Redact 对工具输出或最终回复做敏感信息脱敏。
func (p *Policy) Redact(output string) string {
	if p == nil {
		return output
	}
	for _, pattern := range p.sensitivePatterns {
		output = pattern.ReplaceAllString(output, "[REDACTED]")
	}
	return output
}

// normalizePolicy 用默认值补齐未显式设置的策略字段。
func normalizePolicy(policy SafetyPolicy) SafetyPolicy {
	defaults := DefaultSafetyPolicy()
	if policy.AutoApprove == nil {
		policy.AutoApprove = defaults.AutoApprove
	}
	if policy.RequireHuman == nil {
		policy.RequireHuman = defaults.RequireHuman
	}
	if policy.SensitivePatterns == nil {
		policy.SensitivePatterns = defaults.SensitivePatterns
	}
	return policy
}

// compilePatterns 预编译正则，配置错误在启动期暴露。
func compilePatterns(label string, patterns []string) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("%s %q: %w", label, pattern, err)
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}

// buildStringSet 统一用小写工具名做匹配，避免大小写漂移。
func buildStringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		normalized := normalizeToolName(value)
		if normalized != "" {
			set[normalized] = true
		}
	}
	return set
}

// buildRiskSet 把风险列表转成集合，便于运行时快速判断。
func buildRiskSet(values []RiskLevel) map[RiskLevel]bool {
	set := make(map[RiskLevel]bool, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = true
		}
	}
	return set
}

// normalizeToolName 统一工具名比较规则。
func normalizeToolName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
