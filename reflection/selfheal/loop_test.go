package selfheal

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
)

// TestHealFixesSupportSOPFailure 验证核心循环能通过诊断、补丁、评审、应用和验证完成一次自愈。
func TestHealFixesSupportSOPFailure(t *testing.T) {
	initial := FailureSignal{
		Kind:          "support_sop_gap",
		Severity:      2,
		ErrorText:     "客服补偿 SOP 缺少升级边界和补偿窗口",
		AffectedFiles: []string{"support/sop/compensation_policy"},
	}
	loop := newTestLoop(t, testLoopConfig{
		verifierFailures: []*FailureSignal{nil},
	})

	response, err := loop.Heal(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != StatusFixed || response.Iterations != 1 {
		t.Fatalf("response = %+v", response)
	}
	if len(response.Commits) != 1 || response.Commits[0] != "sop-config-v2" {
		t.Fatalf("commits = %+v", response.Commits)
	}
	if len(response.History) != 1 || response.History[0].NewFailure != nil {
		t.Fatalf("history = %+v", response.History)
	}
	if !strings.Contains(response.UserMessage(), "fixed") {
		t.Fatalf("user message = %s", response.UserMessage())
	}
}

// TestHealRollsBackWhenVerifierFindsRegression 验证验证器发现更严重的新问题时会按提交倒序回滚。
func TestHealRollsBackWhenVerifierFindsRegression(t *testing.T) {
	initial := FailureSignal{
		Kind:          "support_sop_gap",
		Severity:      2,
		ErrorText:     "客服补偿 SOP 缺少升级边界和补偿窗口",
		AffectedFiles: []string{"support/sop/compensation_policy"},
	}
	regression := &FailureSignal{
		Kind:          "compliance_overcompensation",
		Severity:      4,
		ErrorText:     "新补偿策略允许无审核自动赔付，合规风险升高",
		AffectedFiles: []string{"support/sop/compensation_policy", "finance/refund_policy", "risk/audit_rule"},
	}
	loop := newTestLoop(t, testLoopConfig{
		fixDiff:          "unsafe auto_refund_no_review=true",
		verifierFailures: []*FailureSignal{regression},
	})

	response, err := loop.Heal(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != StatusRolledBack || response.Iterations != 1 {
		t.Fatalf("response = %+v", response)
	}
	if got, want := strings.Join(loopRollbackOrder(loop), ","), "sop-config-v2"; got != want {
		t.Fatalf("rollback order = %s, want %s", got, want)
	}
	if len(response.History) != 1 || response.History[0].NewFailure == nil || response.History[0].NewFailure.Kind != regression.Kind {
		t.Fatalf("history = %+v", response.History)
	}
}

// TestHealHandsOffAfterMaxIterations 验证同类失败反复出现时不会无限循环，而是交给人工处理。
func TestHealHandsOffAfterMaxIterations(t *testing.T) {
	initial := FailureSignal{
		Kind:          "support_sop_gap",
		Severity:      2,
		ErrorText:     "客服补偿 SOP 缺少升级边界和补偿窗口",
		AffectedFiles: []string{"support/sop/compensation_policy"},
	}
	sameFailure := &FailureSignal{
		Kind:          "support_sop_gap",
		Severity:      2,
		ErrorText:     "客服补偿 SOP 缺少升级边界和补偿窗口",
		AffectedFiles: []string{"support/sop/compensation_policy"},
	}
	loop := newTestLoop(t, testLoopConfig{
		maxIterations:    2,
		verifierFailures: []*FailureSignal{sameFailure, sameFailure},
	})

	response, err := loop.Heal(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != StatusHumanHandoff || response.Iterations != 2 || len(response.History) != 2 {
		t.Fatalf("response = %+v", response)
	}
}

// TestADKRunnerReturnsCustomizedResponse 验证 Eino ADK Runner 能拿到自愈循环的结构化输出。
func TestADKRunnerReturnsCustomizedResponse(t *testing.T) {
	runner, _, err := NewRunner(context.Background(), Config{
		Diagnoser: func(context.Context, FailureSignal, []HealAttempt) (string, error) {
			return "SOP 缺少可执行补偿边界", nil
		},
		FixGenerator: func(context.Context, string, FailureSignal, []HealAttempt) (FixProposal, error) {
			return FixProposal{Summary: "补齐补偿窗口", FixDiff: "refund_window_hours=24 escalation_enabled=true"}, nil
		},
		Critic: func(context.Context, FixProposal, FailureSignal, []HealAttempt) (CriticVerdict, error) {
			return CriticVerdict{}, nil
		},
		Applier: func(context.Context, FixProposal) (string, error) {
			return "sop-config-v2", nil
		},
		Verifier: func(context.Context, FixProposal) (*FailureSignal, error) {
			return nil, nil
		},
		Rollback: func(context.Context, string) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	query := `{"kind":"support_sop_gap","severity":2,"error_text":"客服补偿 SOP 缺少升级边界","affected_files":["support/sop/compensation_policy"]}`
	iter := runner.Query(context.Background(), query)
	var got *Response
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Output == nil {
			continue
		}
		if response, ok := event.Output.CustomizedOutput.(*Response); ok {
			got = response
		}
	}
	if got == nil || got.Status != StatusFixed || got.Iterations != 1 {
		t.Fatalf("customized response = %+v", got)
	}
}

var _ adk.Agent = (*ADKAgent)(nil)

// testLoopConfig 汇总单测 fake 组件的可变行为，避免每个测试重复搭建闭包。
type testLoopConfig struct {
	maxIterations    int
	fixDiff          string
	verifierFailures []*FailureSignal
}

// testLoop 是测试用 Loop 包装，额外记录 apply 和 rollback 的副作用顺序。
type testLoop struct {
	*Loop
	applied       []string
	rollbackOrder []string
	verifyIndex   int
}

// newTestLoop 创建一套固定诊断和修复节点，只让测试专注状态机分支。
func newTestLoop(t *testing.T, cfg testLoopConfig) *testLoop {
	t.Helper()
	wrapped := &testLoop{}
	maxIterations := cfg.maxIterations
	if maxIterations <= 0 {
		maxIterations = 3
	}
	fixDiff := cfg.fixDiff
	if strings.TrimSpace(fixDiff) == "" {
		fixDiff = "refund_window_hours=24 escalation_enabled=true compensation_limit=200"
	}
	loop, err := NewLoop(Config{
		MaxIterations: maxIterations,
		Diagnoser: func(context.Context, FailureSignal, []HealAttempt) (string, error) {
			return "SOP 缺少明确补偿窗口和升级边界", nil
		},
		FixGenerator: func(context.Context, string, FailureSignal, []HealAttempt) (FixProposal, error) {
			return FixProposal{Summary: "补齐客服补偿 SOP", FixDiff: fixDiff}, nil
		},
		Critic: func(context.Context, FixProposal, FailureSignal, []HealAttempt) (CriticVerdict, error) {
			return CriticVerdict{}, nil
		},
		Applier: func(_ context.Context, _ FixProposal) (string, error) {
			wrapped.applied = append(wrapped.applied, "sop-config-v2")
			return "sop-config-v2", nil
		},
		Verifier: func(context.Context, FixProposal) (*FailureSignal, error) {
			if wrapped.verifyIndex >= len(cfg.verifierFailures) {
				wrapped.verifyIndex++
				return nil, nil
			}
			failure := cfg.verifierFailures[wrapped.verifyIndex]
			wrapped.verifyIndex++
			return failure, nil
		},
		Rollback: func(_ context.Context, commitID string) error {
			wrapped.rollbackOrder = append(wrapped.rollbackOrder, commitID)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped.Loop = loop
	return wrapped
}

// loopRollbackOrder 返回测试 loop 中实际发生的回滚顺序。
func loopRollbackOrder(loop *testLoop) []string {
	if loop == nil {
		return nil
	}
	return loop.rollbackOrder
}
