package selfheal

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
)

// Loop 持有自愈状态机和所有外部节点；循环、安全分支和回滚策略都在 Go 里确定执行。
type Loop struct {
	name          string
	description   string
	maxIterations int
	diagnoser     Diagnoser
	fixGenerator  FixGenerator
	critic        Critic
	applier       Applier
	verifier      Verifier
	rollback      Rollbacker
}

// NewLoop 校验并创建自愈循环，避免运行到一半才发现关键边界缺失。
func NewLoop(config Config) (*Loop, error) {
	if config.Diagnoser == nil {
		return nil, errors.New("diagnoser is required")
	}
	if config.FixGenerator == nil {
		return nil, errors.New("fix generator is required")
	}
	if config.Critic == nil {
		return nil, errors.New("critic is required")
	}
	if config.Applier == nil {
		return nil, errors.New("applier is required")
	}
	if config.Verifier == nil {
		return nil, errors.New("verifier is required")
	}
	if config.Rollback == nil {
		return nil, errors.New("rollback is required")
	}
	name := strings.TrimSpace(config.Name)
	if name == "" {
		name = defaultAgentName
	}
	description := strings.TrimSpace(config.Description)
	if description == "" {
		description = defaultAgentDescription
	}
	maxIterations := config.MaxIterations
	if maxIterations <= 0 {
		maxIterations = defaultMaxIterations
	}
	return &Loop{
		name:          name,
		description:   description,
		maxIterations: maxIterations,
		diagnoser:     config.Diagnoser,
		fixGenerator:  config.FixGenerator,
		critic:        config.Critic,
		applier:       config.Applier,
		verifier:      config.Verifier,
		rollback:      config.Rollback,
	}, nil
}

// Name 返回 ADK 和 trace 展示用的 agent 名称。
func (l *Loop) Name() string {
	if l == nil {
		return ""
	}
	return l.name
}

// Description 返回 ADK agent 描述。
func (l *Loop) Description() string {
	if l == nil {
		return ""
	}
	return l.description
}

// Heal 执行确定性自愈状态机：智能节点只给判断材料，是否继续、回滚或交给人工由代码决定。
func (l *Loop) Heal(ctx context.Context, initial FailureSignal) (*Response, error) {
	if l == nil {
		return nil, errors.New("self-heal loop is nil")
	}
	if err := validateFailureSignal(initial); err != nil {
		return nil, err
	}
	history := make([]HealAttempt, 0, l.maxIterations)
	current := initial
	initialSignature := signature(initial)
	commits := make([]string, 0, l.maxIterations)

	for i := 0; i < l.maxIterations; i++ {
		diagnosis, err := l.diagnoser(ctx, current, history)
		if err != nil {
			return nil, fmt.Errorf("diagnose iteration %d: %w", i, err)
		}
		proposal, err := l.fixGenerator(ctx, diagnosis, current, history)
		if err != nil {
			return nil, fmt.Errorf("generate fix iteration %d: %w", i, err)
		}
		if strings.TrimSpace(proposal.FixDiff) == "" {
			return nil, fmt.Errorf("fix diff is empty at iteration %d", i)
		}
		verdict, err := l.critic(ctx, proposal, current, history)
		if err != nil {
			return nil, fmt.Errorf("critic iteration %d: %w", i, err)
		}
		if verdict.Block {
			return &Response{
				Status:     StatusBlocked,
				Iterations: i + 1,
				History:    history,
			}, nil
		}

		commitID, err := l.applier(ctx, proposal)
		if err != nil {
			return nil, fmt.Errorf("apply iteration %d: %w", i, err)
		}
		commitID = strings.TrimSpace(commitID)
		if commitID == "" {
			return nil, fmt.Errorf("commit id is empty at iteration %d", i)
		}
		commits = append(commits, commitID)

		newFailure, err := l.verifier(ctx, proposal)
		if err != nil {
			return nil, fmt.Errorf("verify iteration %d: %w", i, err)
		}
		attempt := HealAttempt{
			Iteration:  i,
			Diagnosis:  strings.TrimSpace(diagnosis),
			FixDiff:    proposal.FixDiff,
			CommitID:   commitID,
			NewFailure: newFailure,
		}
		history = append(history, attempt)
		if newFailure == nil {
			return &Response{
				Status:     StatusFixed,
				Iterations: i + 1,
				Commits:    commits,
				History:    history,
			}, nil
		}
		if signature(*newFailure) != initialSignature && isRegression(current, *newFailure) {
			if err := l.rollbackCommits(ctx, commits); err != nil {
				return nil, err
			}
			return &Response{
				Status:     StatusRolledBack,
				Iterations: i + 1,
				History:    history,
			}, nil
		}
		current = *newFailure
	}

	return &Response{
		Status:     StatusHumanHandoff,
		Iterations: l.maxIterations,
		History:    history,
	}, nil
}

// UserMessage 把结构化 Response 压成人可读摘要，完整历史仍保留在 CustomizedOutput。
func (r *Response) UserMessage() string {
	if r == nil {
		return ""
	}
	lines := []string{fmt.Sprintf("self-heal status: %s", r.Status)}
	lines = append(lines, fmt.Sprintf("iterations: %d", r.Iterations))
	if len(r.Commits) > 0 {
		lines = append(lines, "commits: "+strings.Join(r.Commits, ", "))
	}
	if len(r.History) > 0 {
		last := r.History[len(r.History)-1]
		if strings.TrimSpace(last.Diagnosis) != "" {
			lines = append(lines, "last diagnosis: "+strings.TrimSpace(last.Diagnosis))
		}
		if last.NewFailure != nil {
			lines = append(lines, "last failure: "+last.NewFailure.Kind)
		}
	}
	return strings.Join(lines, "\n")
}

// rollbackCommits 按提交倒序回滚，保证后应用的变更先撤销。
func (l *Loop) rollbackCommits(ctx context.Context, commits []string) error {
	for i := len(commits) - 1; i >= 0; i-- {
		if err := l.rollback(ctx, commits[i]); err != nil {
			return fmt.Errorf("rollback %s: %w", commits[i], err)
		}
	}
	return nil
}

// validateFailureSignal 做最小输入校验，避免空失败进入模型 prompt。
func validateFailureSignal(f FailureSignal) error {
	if strings.TrimSpace(f.Kind) == "" {
		return errors.New("failure kind is required")
	}
	if strings.TrimSpace(f.ErrorText) == "" {
		return errors.New("failure error_text is required")
	}
	if f.Severity <= 0 {
		return errors.New("failure severity must be positive")
	}
	return nil
}

// signature 生成稳定失败指纹，只看类型和前 200 个字符，避免整段业务文本进入分支判断。
func signature(f FailureSignal) string {
	text := []rune(f.ErrorText)
	if len(text) > 200 {
		text = text[:200]
	}
	key := fmt.Sprintf("%s|%s", strings.TrimSpace(f.Kind), string(text))
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", sum)[:12]
}

// isRegression 判断新失败是否比旧失败更严重，作为自动回滚的确定性边界。
func isRegression(old FailureSignal, new FailureSignal) bool {
	if new.Severity > old.Severity {
		return true
	}
	oldFiles := len(old.AffectedFiles)
	if oldFiles < 1 {
		oldFiles = 1
	}
	return len(new.AffectedFiles) > 2*oldFiles
}
