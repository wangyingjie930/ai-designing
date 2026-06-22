package hypothesis

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// IterativeHypothesisLoop 按 propose -> gather evidence -> evaluate 的顺序反复排除候选假设。
type IterativeHypothesisLoop struct {
	planner       PlannerFunc
	generator     GeneratorFunc
	evaluator     EvaluatorFunc
	maxIterations int
}

// NewIterativeHypothesisLoop 创建可复用的假设检验循环。
func NewIterativeHypothesisLoop(
	planner PlannerFunc,
	generator GeneratorFunc,
	evaluator EvaluatorFunc,
	maxIterations int,
) (*IterativeHypothesisLoop, error) {
	if planner == nil {
		return nil, errors.New("planner is required")
	}
	if generator == nil {
		return nil, errors.New("generator is required")
	}
	if evaluator == nil {
		return nil, errors.New("evaluator is required")
	}
	if maxIterations < 1 {
		return nil, errors.New("max_iterations must be >= 1")
	}
	return &IterativeHypothesisLoop{
		planner:       planner,
		generator:     generator,
		evaluator:     evaluator,
		maxIterations: maxIterations,
	}, nil
}

// Run 执行完整反证循环，退出条件是只有一个被确认的幸存假设或达到迭代上限。
func (l *IterativeHypothesisLoop) Run(ctx context.Context, problem string) (*HypothesisTree, LoopOutcome, error) {
	if l == nil {
		return nil, LoopOutcome{}, errors.New("hypothesis loop is nil")
	}
	if strings.TrimSpace(problem) == "" {
		return nil, LoopOutcome{}, errors.New("problem is required")
	}
	tree := NewHypothesisTree()
	for iteration := 1; iteration <= l.maxIterations; iteration++ {
		if err := l.runIteration(ctx, tree, problem, iteration); err != nil {
			return tree, LoopOutcome{}, err
		}
		if outcome, ok := convergenceOutcome(tree, iteration); ok {
			return tree, outcome, nil
		}
		if tree.SurvivorCount() == 0 && iteration < l.maxIterations {
			continue
		}
		if iteration == l.maxIterations {
			return tree, capOutcome(tree, iteration), nil
		}
	}
	return tree, capOutcome(tree, l.maxIterations), nil
}

// runIteration 执行单轮 planner、generator、evaluator，并把证据写回假设树。
func (l *IterativeHypothesisLoop) runIteration(ctx context.Context, tree *HypothesisTree, problem string, iteration int) error {
	proposals, err := l.planner(ctx, problem, tree.All(), iteration)
	if err != nil {
		return fmt.Errorf("plan hypotheses at iteration %d: %w", iteration, err)
	}
	for _, proposal := range proposals {
		tree.Add(proposal.Description, proposal.Prior, iteration)
	}
	active := tree.Active()
	for _, h := range active {
		if h == nil {
			continue
		}
		candidates, err := l.generator(ctx, cloneHypothesis(h))
		if err != nil {
			return fmt.Errorf("generate evidence for %s: %w", h.ID, err)
		}
		for _, candidate := range candidates {
			if strings.TrimSpace(candidate.Description) == "" {
				continue
			}
			evaluation, err := l.evaluator(ctx, cloneHypothesis(h), candidate)
			if err != nil {
				return fmt.Errorf("evaluate evidence for %s: %w", h.ID, err)
			}
			effect, ok := normalizeEffect(evaluation.Effect)
			if !ok {
				return fmt.Errorf("invalid evidence effect %q for %s", evaluation.Effect, h.ID)
			}
			h.RecordEvidence(newEvidence(cloneHypothesis(h), candidate, effect), evaluation.PosteriorDelta)
		}
	}
	return nil
}

// convergenceOutcome 判断是否出现“唯一且已确认的幸存假设”。
func convergenceOutcome(tree *HypothesisTree, iteration int) (LoopOutcome, bool) {
	confirmed := tree.Confirmed()
	if len(confirmed) == 1 && tree.SurvivorCount() == 1 {
		return LoopOutcome{
			Converged:      true,
			NeedsHITL:      false,
			ConfirmedID:    confirmed[0].ID,
			IterationsUsed: iteration,
			Reason:         "single confirmed survivor",
		}, true
	}
	return LoopOutcome{}, false
}

// capOutcome 在迭代上限触发时，根据幸存假设数量决定是否需要人工接管。
func capOutcome(tree *HypothesisTree, iteration int) LoopOutcome {
	survivors := tree.Survivors()
	switch len(survivors) {
	case 0:
		return LoopOutcome{
			Converged:      false,
			NeedsHITL:      true,
			IterationsUsed: iteration,
			Reason:         "cap reached with all hypotheses falsified",
		}
	case 1:
		return LoopOutcome{
			Converged:      false,
			NeedsHITL:      false,
			ConfirmedID:    survivors[0].ID,
			IterationsUsed: iteration,
			Reason:         "cap reached, one hypothesis still standing",
		}
	default:
		return LoopOutcome{
			Converged:      false,
			NeedsHITL:      true,
			IterationsUsed: iteration,
			Reason:         fmt.Sprintf("cap reached with %d hypotheses still standing", len(survivors)),
		}
	}
}
