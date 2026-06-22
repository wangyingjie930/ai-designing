package hypothesis

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// RecordEvidence 把证据写入目标假设，并根据评估结果更新后验概率和状态。
func (h *Hypothesis) RecordEvidence(e Evidence, posteriorDelta float64) {
	if h == nil {
		return
	}
	e.Description = strings.TrimSpace(e.Description)
	e.Source = strings.TrimSpace(e.Source)
	if e.Timestamp == "" {
		e.Timestamp = nowISO()
	}
	h.Evidence = append(h.Evidence, e)
	h.Posterior = normalizeProbability(h.Posterior + posteriorDelta)
	switch e.Effect {
	case EffectRefutes:
		h.Status = StatusFalsified
		h.FalsifiedBy = e.Description
	case EffectSupports:
		if h.Posterior >= 0.9 {
			h.Status = StatusConfirmed
			return
		}
		if h.Status == StatusProposed {
			h.Status = StatusTesting
		}
	case EffectNeutral:
		if h.Status == StatusProposed {
			h.Status = StatusTesting
		}
	}
}

// HypothesisTree 保存一次循环中的全部候选假设，并保留插入顺序方便输出和测试。
type HypothesisTree struct {
	hypotheses map[string]*Hypothesis
	order      []string
}

// NewHypothesisTree 创建空假设树。
func NewHypothesisTree() *HypothesisTree {
	return &HypothesisTree{
		hypotheses: map[string]*Hypothesis{},
		order:      []string{},
	}
}

// Add 添加一个新假设；描述相同时复用已有节点，避免同一解释被重复测试。
func (t *HypothesisTree) Add(description string, prior float64, iteration int) *Hypothesis {
	if t == nil {
		return nil
	}
	description = strings.TrimSpace(description)
	if description == "" {
		return nil
	}
	id := hypothesisID(description)
	if existing, ok := t.hypotheses[id]; ok {
		return existing
	}
	h := &Hypothesis{
		ID:               id,
		Description:      description,
		Prior:            normalizeProbability(prior),
		Posterior:        normalizeProbability(prior),
		Status:           StatusProposed,
		Evidence:         []Evidence{},
		CreatedIteration: iteration,
	}
	t.hypotheses[id] = h
	t.order = append(t.order, id)
	return h
}

// Active 返回仍需继续收集证据的假设。
func (t *HypothesisTree) Active() []*Hypothesis {
	if t == nil {
		return nil
	}
	active := make([]*Hypothesis, 0, len(t.order))
	for _, id := range t.order {
		h := t.hypotheses[id]
		if h == nil {
			continue
		}
		if h.Status != StatusFalsified && h.Status != StatusConfirmed {
			active = append(active, h)
		}
	}
	return active
}

// Confirmed 返回已经被证据支持到确认阈值的假设。
func (t *HypothesisTree) Confirmed() []*Hypothesis {
	if t == nil {
		return nil
	}
	confirmed := make([]*Hypothesis, 0, len(t.order))
	for _, id := range t.order {
		h := t.hypotheses[id]
		if h != nil && h.Status == StatusConfirmed {
			confirmed = append(confirmed, h)
		}
	}
	return confirmed
}

// Survivors 返回尚未被反证淘汰的假设，包含 confirmed 和 testing 两类。
func (t *HypothesisTree) Survivors() []*Hypothesis {
	if t == nil {
		return nil
	}
	survivors := make([]*Hypothesis, 0, len(t.order))
	for _, id := range t.order {
		h := t.hypotheses[id]
		if h != nil && h.Status != StatusFalsified {
			survivors = append(survivors, h)
		}
	}
	return survivors
}

// SurvivorCount 返回 Popper 式收敛判断关心的数量：还有几个假设没有被反证。
func (t *HypothesisTree) SurvivorCount() int {
	return len(t.Survivors())
}

// ByID 按假设 ID 查找节点。
func (t *HypothesisTree) ByID(id string) *Hypothesis {
	if t == nil {
		return nil
	}
	return t.hypotheses[strings.TrimSpace(id)]
}

// All 返回全部假设的值拷贝，避免外部 planner 直接修改树。
func (t *HypothesisTree) All() []Hypothesis {
	if t == nil {
		return nil
	}
	result := make([]Hypothesis, 0, len(t.order))
	for _, id := range t.order {
		if h := t.hypotheses[id]; h != nil {
			result = append(result, cloneHypothesis(h))
		}
	}
	return result
}

// Snapshot 生成稳定 JSON 快照，命令行和 trace 都可以只读这个摘要。
func (t *HypothesisTree) Snapshot() TreeSnapshot {
	if t == nil {
		return TreeSnapshot{}
	}
	hypotheses := t.All()
	return TreeSnapshot{
		Hypotheses:     hypotheses,
		ActiveCount:    len(t.Active()),
		ConfirmedCount: len(t.Confirmed()),
		SurvivorCount:  t.SurvivorCount(),
	}
}

// cloneHypothesis 深拷贝 evidence，保证外部只读快照不会污染内部状态。
func cloneHypothesis(h *Hypothesis) Hypothesis {
	if h == nil {
		return Hypothesis{}
	}
	cloned := *h
	cloned.Evidence = append([]Evidence(nil), h.Evidence...)
	return cloned
}

// newEvidence 统一补齐证据元信息，避免各 generator 重复维护时间戳和目标 ID。
func newEvidence(h Hypothesis, candidate EvidenceCandidate, effect EvidenceEffect) Evidence {
	return Evidence{
		Description:        strings.TrimSpace(candidate.Description),
		Source:             strings.TrimSpace(candidate.Source),
		Effect:             effect,
		TargetHypothesisID: h.ID,
		Timestamp:          nowISO(),
	}
}

// normalizeProbability 把概率截断到 0 到 1，防止模型输出异常值破坏状态机。
func normalizeProbability(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// normalizeEffect 把模型输出的 effect 归一化为受控枚举。
func normalizeEffect(effect EvidenceEffect) (EvidenceEffect, bool) {
	switch EvidenceEffect(strings.ToLower(strings.TrimSpace(string(effect)))) {
	case EffectSupports:
		return EffectSupports, true
	case EffectRefutes:
		return EffectRefutes, true
	case EffectNeutral:
		return EffectNeutral, true
	default:
		return "", false
	}
}

// hypothesisID 用描述生成稳定短 ID，便于命令行、JSON 和 trace 关联同一假设。
func hypothesisID(description string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(description)))
	return hex.EncodeToString(sum[:])[:10]
}

// nowISO 返回 UTC 时间，保持证据审计日志跨机器可比较。
func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
