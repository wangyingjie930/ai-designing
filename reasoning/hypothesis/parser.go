package hypothesis

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// plannerOutput 是 planner JSON 的外层结构。
type plannerOutput struct {
	Hypotheses []Proposal `json:"hypotheses"`
}

// generatorOutput 是 generator JSON 的外层结构。
type generatorOutput struct {
	Evidence []EvidenceCandidate `json:"evidence"`
}

// ParsePlannerResponse 从模型输出中解析候选假设列表。
func ParsePlannerResponse(text string) ([]Proposal, error) {
	jsonText, err := extractJSONObject(text)
	if err != nil {
		return nil, err
	}
	var output plannerOutput
	if err := json.Unmarshal([]byte(jsonText), &output); err != nil {
		return nil, fmt.Errorf("parse planner json: %w", err)
	}
	return normalizeProposals(output.Hypotheses), nil
}

// ParseGeneratorResponse 从模型输出中解析证据候选列表。
func ParseGeneratorResponse(text string) ([]EvidenceCandidate, error) {
	jsonText, err := extractJSONObject(text)
	if err != nil {
		return nil, err
	}
	var output generatorOutput
	if err := json.Unmarshal([]byte(jsonText), &output); err != nil {
		return nil, fmt.Errorf("parse generator json: %w", err)
	}
	return normalizeEvidenceCandidates(output.Evidence), nil
}

// ParseEvaluatorResponse 从模型输出中解析证据评估结果。
func ParseEvaluatorResponse(text string) (Evaluation, error) {
	jsonText, err := extractJSONObject(text)
	if err != nil {
		return Evaluation{}, err
	}
	var evaluation Evaluation
	if err := json.Unmarshal([]byte(jsonText), &evaluation); err != nil {
		return Evaluation{}, fmt.Errorf("parse evaluator json: %w", err)
	}
	effect, ok := normalizeEffect(evaluation.Effect)
	if !ok {
		return Evaluation{}, fmt.Errorf("invalid effect %q", evaluation.Effect)
	}
	evaluation.Effect = effect
	if evaluation.PosteriorDelta > 1 {
		evaluation.PosteriorDelta = 1
	}
	if evaluation.PosteriorDelta < -1 {
		evaluation.PosteriorDelta = -1
	}
	return evaluation, nil
}

// normalizeProposals 清理空描述并规整 prior。
func normalizeProposals(proposals []Proposal) []Proposal {
	normalized := make([]Proposal, 0, len(proposals))
	for _, proposal := range proposals {
		description := strings.TrimSpace(proposal.Description)
		if description == "" {
			continue
		}
		normalized = append(normalized, Proposal{
			Description: description,
			Prior:       normalizeProbability(proposal.Prior),
		})
	}
	return normalized
}

// normalizeEvidenceCandidates 清理空证据，并给缺失来源的证据补默认来源。
func normalizeEvidenceCandidates(candidates []EvidenceCandidate) []EvidenceCandidate {
	normalized := make([]EvidenceCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		description := strings.TrimSpace(candidate.Description)
		if description == "" {
			continue
		}
		source := strings.TrimSpace(candidate.Source)
		if source == "" {
			source = "model_generated_check"
		}
		normalized = append(normalized, EvidenceCandidate{
			Description: description,
			Source:      source,
		})
	}
	return normalized
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
