package compose

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ParseRouteDecision 从模型输出中解析复杂度路由结果。
func ParseRouteDecision(text string) (RouteDecision, error) {
	jsonText, err := extractJSONObject(text)
	if err != nil {
		return RouteDecision{}, err
	}
	var decision RouteDecision
	if err := json.Unmarshal([]byte(jsonText), &decision); err != nil {
		return RouteDecision{}, fmt.Errorf("parse route json: %w", err)
	}
	return normalizeRouteDecision(decision)
}

// ParseBestPathDecision 从模型输出中解析最佳路径确认结果。
func ParseBestPathDecision(text string) (BestPathDecision, error) {
	jsonText, err := extractJSONObject(text)
	if err != nil {
		return BestPathDecision{}, err
	}
	var decision BestPathDecision
	if err := json.Unmarshal([]byte(jsonText), &decision); err != nil {
		return BestPathDecision{}, fmt.Errorf("parse best path json: %w", err)
	}
	decision.Confidence = normalizeProbability(decision.Confidence)
	decision.Reason = strings.TrimSpace(decision.Reason)
	return decision, nil
}

// normalizeRouteDecision 规整复杂度、路径和置信度，避免模型大小写差异影响状态机。
func normalizeRouteDecision(decision RouteDecision) (RouteDecision, error) {
	decision.Complexity = Complexity(strings.ToLower(strings.TrimSpace(string(decision.Complexity))))
	switch decision.Complexity {
	case ComplexitySimple:
		if decision.RecommendedPath == "" {
			decision.RecommendedPath = PathDirect
		}
	case ComplexityModerate:
		if decision.RecommendedPath == "" {
			decision.RecommendedPath = PathCOT
		}
	case ComplexityComplex:
		if decision.RecommendedPath == "" {
			decision.RecommendedPath = PathTOT
		}
	default:
		return RouteDecision{}, fmt.Errorf("invalid complexity %q", decision.Complexity)
	}
	decision.RecommendedPath = PathKind(strings.ToLower(strings.TrimSpace(string(decision.RecommendedPath))))
	if decision.RecommendedPath == "" {
		decision.RecommendedPath = pathForComplexity(decision.Complexity)
	}
	if !validRecommendedPath(decision.RecommendedPath) {
		return RouteDecision{}, fmt.Errorf("invalid recommended path %q", decision.RecommendedPath)
	}
	decision.Confidence = normalizeProbability(decision.Confidence)
	decision.Reason = strings.TrimSpace(decision.Reason)
	return decision, nil
}

// heuristicRouteDecision 在路由 JSON 不可解析时给出保守兜底，避免整个 Agent 卡死在格式问题上。
func heuristicRouteDecision(query string, reason string) RouteDecision {
	runes := len([]rune(strings.TrimSpace(query)))
	complexity := ComplexityModerate
	if runes <= 28 && !containsAny(query, []string{"比较", "诊断", "权衡", "风险", "原因", "方案"}) {
		complexity = ComplexitySimple
	}
	if runes >= 120 || containsAny(query, []string{"多个", "冲突", "不确定", "诊断", "根因", "权衡", "风险", "预算", "优先级"}) {
		complexity = ComplexityComplex
	}
	return RouteDecision{
		Complexity:      complexity,
		Confidence:      0.45,
		Reason:          strings.TrimSpace(reason),
		RecommendedPath: pathForComplexity(complexity),
	}
}

// pathForComplexity 返回每个复杂度默认对应的处理路径。
func pathForComplexity(complexity Complexity) PathKind {
	switch complexity {
	case ComplexitySimple:
		return PathDirect
	case ComplexityComplex:
		return PathTOT
	default:
		return PathCOT
	}
}

// validRecommendedPath 限制路由器只能选择图里已有的三条入口路径。
func validRecommendedPath(path PathKind) bool {
	switch path {
	case PathDirect, PathCOT, PathTOT:
		return true
	default:
		return false
	}
}

// normalizeProbability 把概率或置信度限制在 0 到 1。
func normalizeProbability(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// containsAny 判断文本是否命中任一关键词，供兜底路由使用。
func containsAny(text string, keywords []string) bool {
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

// extractJSONObject 只抽取第一个完整 JSON object，兼容模型偶发前后缀。
func extractJSONObject(text string) (string, error) {
	trimmed := strings.TrimSpace(stripJSONFence(text))
	if trimmed == "" {
		return "", errors.New("empty json response")
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed, nil
	}
	start := strings.Index(trimmed, "{")
	if start < 0 {
		return "", errors.New("response does not contain json object")
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

// stripJSONFence 去掉模型可能包上的 ```json 代码块。
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
