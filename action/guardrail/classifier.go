package guardrail

import "strings"

// DefaultRiskClassifier 用工具名和参数中的危险词做保守风险分类。
type DefaultRiskClassifier struct{}

// ClassifyRisk 根据工具名和参数内容判断默认风险等级。
func (DefaultRiskClassifier) ClassifyRisk(toolName string, argumentsInJSON string) RiskLevel {
	name := strings.ToLower(strings.TrimSpace(toolName))
	args := strings.ToLower(argumentsInJSON)
	for _, marker := range []string{"rm -rf", "drop table", "truncate", "delete", "cancel_contract", "cancel_service_contract"} {
		if strings.Contains(args, marker) || strings.Contains(name, marker) {
			return RiskLevelCritical
		}
	}
	for _, marker := range []string{"send_email", "post_message", "deploy", "send_customer_notice"} {
		if name == marker {
			return RiskLevelHigh
		}
	}
	if strings.Contains(args, "http://") || strings.Contains(args, "https://") {
		return RiskLevelHigh
	}
	for _, marker := range []string{"write_file", "edit_file", "update_work_order", "write_work_order"} {
		if name == marker {
			return RiskLevelMedium
		}
	}
	return RiskLevelLow
}
