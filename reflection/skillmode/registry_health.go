package skillmode

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const defaultSkillHealthWindow = 24 * time.Hour

// SkillOutcome 表示一次 Skill 调用结果，后续成功率统计只依赖这个稳定枚举。
type SkillOutcome string

const (
	SkillOutcomeSuccess          SkillOutcome = "success"
	SkillOutcomeToolError        SkillOutcome = "tool_error"
	SkillOutcomeAPIContractError SkillOutcome = "api_contract_error"
	SkillOutcomeLowQuality       SkillOutcome = "low_quality"
	SkillOutcomeNotApplicable    SkillOutcome = "not_applicable"
)

// SkillHealthStatus 表示 Skill 当前健康状态，registry 会据此决定是否暴露给 planner。
type SkillHealthStatus string

const (
	SkillHealthStatusHealthy    SkillHealthStatus = "healthy"
	SkillHealthStatusWatch      SkillHealthStatus = "watch"
	SkillHealthStatusDegraded   SkillHealthStatus = "degraded"
	SkillHealthStatusDeprecated SkillHealthStatus = "deprecated"
)

// SkillHealthEvent 是一次 Skill 调用的健康留证，不包含完整用户输入或 Skill 正文。
type SkillHealthEvent struct {
	SkillName string
	Version   string
	Alias     string
	Outcome   SkillOutcome
	At        time.Time
}

// SkillHealthQuery 描述一次健康统计范围，默认看最近 24 小时。
type SkillHealthQuery struct {
	SkillName string
	Version   string
	Now       time.Time
	Window    time.Duration
}

// SkillHealthMetrics 是窗口聚合后的健康指标，方便 trace 或命令行展示。
type SkillHealthMetrics struct {
	TotalEvents       int
	EvaluableSamples  int
	Successes         int
	NotApplicable     int
	SuccessRate       float64
	MinimumSamplesMet bool
}

// SkillHealthStore 抽象健康事件存取，第一版用内存实现，后续可替换成 SQLite 或指标系统。
type SkillHealthStore interface {
	Record(ctx context.Context, event SkillHealthEvent) error
	List(ctx context.Context, query SkillHealthQuery) ([]SkillHealthEvent, error)
}

// MemorySkillHealthStore 保存进程内健康事件，适合 demo 和单元测试。
type MemorySkillHealthStore struct {
	mu     sync.RWMutex
	events []SkillHealthEvent
}

// NewMemorySkillHealthStore 创建一个空的内存健康事件仓库。
func NewMemorySkillHealthStore() *MemorySkillHealthStore {
	return &MemorySkillHealthStore{}
}

// Record 追加一次 Skill 调用结果，并做最小字段归一化。
func (s *MemorySkillHealthStore) Record(_ context.Context, event SkillHealthEvent) error {
	if s == nil {
		return fmt.Errorf("skill health memory store is nil")
	}
	event.SkillName = strings.TrimSpace(event.SkillName)
	event.Version = strings.TrimSpace(event.Version)
	event.Alias = strings.TrimSpace(event.Alias)
	if event.SkillName == "" {
		return fmt.Errorf("skill name is required")
	}
	if event.Outcome == "" {
		return fmt.Errorf("skill outcome is required")
	}
	if event.At.IsZero() {
		event.At = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

// List 返回符合 Skill、版本和时间窗口的健康事件。
func (s *MemorySkillHealthStore) List(_ context.Context, query SkillHealthQuery) ([]SkillHealthEvent, error) {
	if s == nil {
		return nil, fmt.Errorf("skill health memory store is nil")
	}
	query = normalizeSkillHealthQuery(query)
	s.mu.RLock()
	defer s.mu.RUnlock()
	events := make([]SkillHealthEvent, 0, len(s.events))
	for _, event := range s.events {
		if query.SkillName != "" && event.SkillName != query.SkillName {
			continue
		}
		if query.Version != "" && event.Version != query.Version {
			continue
		}
		if event.At.Before(query.Now.Add(-query.Window)) || event.At.After(query.Now) {
			continue
		}
		events = append(events, event)
	}
	return events, nil
}

// SkillHealthEvaluator 根据窗口事件计算 Skill 当前健康状态。
type SkillHealthEvaluator struct {
	Window          time.Duration
	MinimumSamples  int
	WatchBelow      float64
	DegradedBelow   float64
	DeprecatedBelow float64
}

// DefaultSkillHealthEvaluator 返回保守阈值：样本少时不自动惩罚，样本足够且很差才废弃。
func DefaultSkillHealthEvaluator() SkillHealthEvaluator {
	return SkillHealthEvaluator{
		Window:          defaultSkillHealthWindow,
		MinimumSamples:  5,
		WatchBelow:      0.90,
		DegradedBelow:   0.75,
		DeprecatedBelow: 0.50,
	}
}

// Evaluate 聚合窗口事件并返回健康状态；not_applicable 不进入成功率分母。
func (e SkillHealthEvaluator) Evaluate(ctx context.Context, store SkillHealthStore, query SkillHealthQuery) (SkillHealthStatus, SkillHealthMetrics, error) {
	if store == nil {
		return "", SkillHealthMetrics{}, fmt.Errorf("skill health store is required")
	}
	e = normalizeSkillHealthEvaluator(e)
	query = e.normalizeQuery(query)
	events, err := store.List(ctx, query)
	if err != nil {
		return "", SkillHealthMetrics{}, err
	}
	metrics := buildSkillHealthMetrics(events, e.MinimumSamples)
	if !metrics.MinimumSamplesMet {
		return SkillHealthStatusHealthy, metrics, nil
	}
	switch {
	case metrics.SuccessRate < e.DeprecatedBelow:
		return SkillHealthStatusDeprecated, metrics, nil
	case metrics.SuccessRate < e.DegradedBelow:
		return SkillHealthStatusDegraded, metrics, nil
	case metrics.SuccessRate < e.WatchBelow:
		return SkillHealthStatusWatch, metrics, nil
	default:
		return SkillHealthStatusHealthy, metrics, nil
	}
}

// buildSkillHealthMetrics 将原始事件压成成功率指标，保持状态判断逻辑更直观。
func buildSkillHealthMetrics(events []SkillHealthEvent, minimumSamples int) SkillHealthMetrics {
	metrics := SkillHealthMetrics{TotalEvents: len(events)}
	for _, event := range events {
		switch event.Outcome {
		case SkillOutcomeSuccess:
			metrics.Successes++
			metrics.EvaluableSamples++
		case SkillOutcomeNotApplicable:
			metrics.NotApplicable++
		default:
			metrics.EvaluableSamples++
		}
	}
	if metrics.EvaluableSamples > 0 {
		metrics.SuccessRate = float64(metrics.Successes) / float64(metrics.EvaluableSamples)
	}
	metrics.MinimumSamplesMet = metrics.EvaluableSamples >= minimumSamples
	return metrics
}

// normalizeSkillHealthEvaluator 用默认值补齐未配置阈值，调用方只需要覆盖关心的字段。
func normalizeSkillHealthEvaluator(evaluator SkillHealthEvaluator) SkillHealthEvaluator {
	defaults := DefaultSkillHealthEvaluator()
	if evaluator.Window > 0 {
		defaults.Window = evaluator.Window
	}
	if evaluator.MinimumSamples > 0 {
		defaults.MinimumSamples = evaluator.MinimumSamples
	}
	if evaluator.WatchBelow > 0 {
		defaults.WatchBelow = evaluator.WatchBelow
	}
	if evaluator.DegradedBelow > 0 {
		defaults.DegradedBelow = evaluator.DegradedBelow
	}
	if evaluator.DeprecatedBelow > 0 {
		defaults.DeprecatedBelow = evaluator.DeprecatedBelow
	}
	return defaults
}

// normalizeSkillHealthQuery 补齐查询默认值，避免调用方每次都传窗口和当前时间。
func normalizeSkillHealthQuery(query SkillHealthQuery) SkillHealthQuery {
	query.SkillName = strings.TrimSpace(query.SkillName)
	query.Version = strings.TrimSpace(query.Version)
	if query.Now.IsZero() {
		query.Now = time.Now()
	}
	if query.Window <= 0 {
		query.Window = defaultSkillHealthWindow
	}
	return query
}

// normalizeQuery 使用 evaluator 的窗口配置覆盖通用默认值。
func (e SkillHealthEvaluator) normalizeQuery(query SkillHealthQuery) SkillHealthQuery {
	query = normalizeSkillHealthQuery(query)
	if e.Window > 0 {
		query.Window = e.Window
	}
	return query
}
