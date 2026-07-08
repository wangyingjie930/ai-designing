package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	jsonschema "github.com/eino-contrib/jsonschema"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	cotv2 "ai-designing/reasoning/cot_v2"
)

func TestRunAgentUsesJSONSchemaModelAndEvidenceCallback(t *testing.T) {
	oldFactory := newChatModel
	fakeModel := &cotV2FakeModel{}
	var capturedSchema string
	newChatModel = func(_ context.Context, _ modelConfig, js *jsonschema.Schema) (model.BaseChatModel, error) {
		raw, err := json.Marshal(js)
		if err != nil {
			t.Fatalf("marshal schema: %v", err)
		}
		capturedSchema = string(raw)
		return fakeModel, nil
	}
	defer func() { newChatModel = oldFactory }()

	source := installFakeEvidenceCallback(t)
	setRequiredEnv(t)

	output, err := runAgent(context.Background(), []string{
		"-env", filepath.Join(t.TempDir(), "missing.env"),
		"-message", "判断员工 P 的奖金异常是否可以自动放行。",
		"-scenario", "薪酬异常审核",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capturedSchema, `"steps"`) || !strings.Contains(capturedSchema, `"additionalProperties":false`) {
		t.Fatalf("schema missing strict StepDraftList contract: %s", capturedSchema)
	}
	if fakeModel.Count() != 1 {
		t.Fatalf("model calls = %d, want 1", fakeModel.Count())
	}
	if output.Mode != "agent" || output.Drafts != 4 || output.Steps != 4 || !output.Verified || output.FinalDecision != "auto_release" {
		t.Fatalf("output = %+v", output)
	}
	for _, sourceKind := range []evidenceSourceKind{evidenceSourceRetrieval, evidenceSourceTool, evidenceSourceLog} {
		if !source.called(sourceKind) {
			t.Fatalf("evidence callback %s was not called", sourceKind)
		}
	}
}

func TestDeriveEvidenceUsesPriorEvidenceRefs(t *testing.T) {
	oldFactory := newChatModel
	newChatModel = func(context.Context, modelConfig, *jsonschema.Schema) (model.BaseChatModel, error) {
		return &cotV2FakeModel{}, nil
	}
	defer func() { newChatModel = oldFactory }()

	source := installFakeEvidenceCallback(t)
	setRequiredEnv(t)

	if _, err := runAgent(context.Background(), []string{
		"-env", filepath.Join(t.TempDir(), "missing.env"),
		"-message", "判断员工 P 的奖金异常是否可以自动放行。",
	}); err != nil {
		t.Fatal(err)
	}
	toolRequest, ok := source.firstCall(evidenceSourceTool)
	if !ok {
		t.Fatal("tool source was not called")
	}
	if len(toolRequest.PriorEvidenceRefs) < 2 {
		t.Fatalf("tool request missing prior evidence refs: %+v", toolRequest)
	}
	if toolRequest.PriorEvidenceRefs[0].SourceID != "payroll_snapshot:P:2026-05" {
		t.Fatalf("prior refs = %+v", toolRequest.PriorEvidenceRefs)
	}
}

func TestRunAgentUsesDefaultEvidenceCallbackFromQuestionInput(t *testing.T) {
	oldFactory := newChatModel
	newChatModel = func(context.Context, modelConfig, *jsonschema.Schema) (model.BaseChatModel, error) {
		return &cotV2FakeModel{content: `{
  "steps": [
    {"kind":"observe","claim_text":"员工 P 属于上海市场部 6 月薪资快照的审核对象。","suggested_subject":"employee:P","suggested_predicate":"in_audit_scope","suggested_object":true,"suggested_evidence_query":"检索上海市场部 6 月薪资快照审核对象"},
    {"kind":"observe","claim_text":"员工 P 的 6 月应发工资比 5 月高 18%。","suggested_subject":"employee:P","suggested_predicate":"pay_delta_percent","suggested_object":18,"suggested_evidence_query":"读取 P 的 5 月和 6 月薪资快照"},
    {"kind":"derive","claim_text":"员工 P 的 6 月应发工资 18% 涨幅超过政策规定的 15% 核查阈值。","suggested_subject":"employee:P","suggested_predicate":"pay_delta_exceeds_threshold","suggested_object":true,"suggested_evidence_query":"比较 18% 与 15% 阈值"},
    {"kind":"verify","claim_text":"审批单 BONUS-18472 的金额、审批人和生效月份均匹配。","suggested_subject":"approval:BONUS-18472","suggested_predicate":"approval_matches_bonus_delta","suggested_object":true,"suggested_evidence_query":"核查审批单 BONUS-18472"},
    {"kind":"decide","claim_text":"员工 P 的 6 月 18% 薪资异常可以自动放行。","suggested_subject":"payroll_exception:P:2026-06","suggested_predicate":"release_decision","suggested_object":"auto_release"}
  ]
}`}, nil
	}
	defer func() { newChatModel = oldFactory }()

	setRequiredEnv(t)
	output, err := runAgent(context.Background(), []string{
		"-env", filepath.Join(t.TempDir(), "missing.env"),
		"-message", "帮我看一下上海市场部 6 月薪资快照里的异常。员工 P 的 6 月应发工资比 5 月高 18%，系统已经取回以下背景：5 月和 6 月薪资快照均为 v3；差异计算显示基本工资和补贴没有变化，增量主要来自季度奖金；租户当前薪酬异常政策为 payroll_anomaly v7，2026-05-01 起生效；政策要求单月应发涨幅超过 15% 时核查审批；审批单 BONUS-18472 存在，金额、审批人和生效月份均匹配。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !output.Verified || output.FinalDecision != "auto_release" {
		t.Fatalf("output = %+v", output)
	}
}

func TestRunAgentFailsWithoutEvidenceCallbackForModelGeneratedSteps(t *testing.T) {
	oldFactory := newChatModel
	newChatModel = func(context.Context, modelConfig, *jsonschema.Schema) (model.BaseChatModel, error) {
		return &cotV2FakeModel{}, nil
	}
	defer func() { newChatModel = oldFactory }()

	setRequiredEnv(t)
	oldCallback := newEvidenceCallback
	newEvidenceCallback = func(string) evidenceCallback { return nil }
	defer func() { newEvidenceCallback = oldCallback }()

	_, err := runAgent(context.Background(), []string{
		"-env", filepath.Join(t.TempDir(), "missing.env"),
		"-message", "判断员工 P 的奖金异常是否可以自动放行。",
	})
	if err == nil || !strings.Contains(err.Error(), "evidence callback is not configured") {
		t.Fatalf("runAgent() error = %v", err)
	}
}

func TestRunAgentFailsWhenObserveCallbackReturnsNoEvidenceRefs(t *testing.T) {
	oldFactory := newChatModel
	newChatModel = func(context.Context, modelConfig, *jsonschema.Schema) (model.BaseChatModel, error) {
		return &cotV2FakeModel{}, nil
	}
	defer func() { newChatModel = oldFactory }()

	setRequiredEnv(t)
	oldCallback := newEvidenceCallback
	newEvidenceCallback = func(string) evidenceCallback {
		return func(context.Context, evidenceCallbackRequest) (evidenceCallbackResponse, error) {
			return evidenceCallbackResponse{}, nil
		}
	}
	defer func() { newEvidenceCallback = oldCallback }()

	_, err := runAgent(context.Background(), []string{
		"-env", filepath.Join(t.TempDir(), "missing.env"),
		"-message", "判断员工 P 的奖金异常是否可以自动放行。",
	})
	if err == nil || !strings.Contains(err.Error(), "missing evidence refs for observe") {
		t.Fatalf("runAgent() error = %v", err)
	}
}

func TestPrepareOnlyDoesNotNeedModelOrEvidenceCallback(t *testing.T) {
	output, err := runAgent(context.Background(), []string{
		"-env", filepath.Join(t.TempDir(), "missing.env"),
		"-message", "判断员工 P 的奖金异常是否可以自动放行。",
		"-prepare-only",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Mode != "prepare-only" || output.QuestionChars == 0 {
		t.Fatalf("output = %+v", output)
	}
}

type fakeEvidenceCallback struct {
	mu    sync.Mutex
	calls []evidenceCallbackRequest
}

func installFakeEvidenceCallback(t *testing.T) *fakeEvidenceCallback {
	t.Helper()
	callback := &fakeEvidenceCallback{}
	oldCallback := newEvidenceCallback
	newEvidenceCallback = func(string) evidenceCallback { return callback.Resolve }
	t.Cleanup(func() { newEvidenceCallback = oldCallback })
	return callback
}

func (s *fakeEvidenceCallback) Resolve(_ context.Context, sourceReq evidenceCallbackRequest) (evidenceCallbackResponse, error) {
	s.mu.Lock()
	s.calls = append(s.calls, sourceReq)
	s.mu.Unlock()

	switch sourceReq.Source {
	case evidenceSourceRetrieval:
		return fakeRetrievalResponse(sourceReq.Step.Predicate), nil
	case evidenceSourceLog:
		return fakeLogResponse(sourceReq.Step.Predicate), nil
	case evidenceSourceTool:
		if len(sourceReq.PriorEvidenceRefs) == 0 {
			return evidenceCallbackResponse{}, fmt.Errorf("missing prior evidence refs")
		}
		return evidenceCallbackResponse{
			Action:       "payroll_delta_calculator",
			EvidenceRefs: []cotv2.EvidenceRef{{SourceID: "tool_run:payroll_delta_calculator:P:2026-06", SourceType: "tool_result", Version: "run-1"}},
		}, nil
	default:
		return evidenceCallbackResponse{}, fmt.Errorf("unknown source %s", sourceReq.Source)
	}
}

func (s *fakeEvidenceCallback) called(source evidenceSourceKind) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, call := range s.calls {
		if call.Source == source {
			return true
		}
	}
	return false
}

func (s *fakeEvidenceCallback) firstCall(source evidenceSourceKind) (evidenceCallbackRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, call := range s.calls {
		if call.Source == source {
			return call, true
		}
	}
	return evidenceCallbackRequest{}, false
}

func fakeRetrievalResponse(predicate string) evidenceCallbackResponse {
	switch predicate {
	case "pay_delta_percent":
		return evidenceCallbackResponse{EvidenceRefs: []cotv2.EvidenceRef{
			{SourceID: "payroll_snapshot:P:2026-05", SourceType: "payroll_snapshot", Version: "v3"},
			{SourceID: "payroll_snapshot:P:2026-06", SourceType: "payroll_snapshot", Version: "v4"},
		}}
	case "requires_approval_over_percent":
		return evidenceCallbackResponse{EvidenceRefs: []cotv2.EvidenceRef{{SourceID: "payroll_policy:acme:anomaly:v7", SourceType: "policy", Version: "v7"}}}
	default:
		return evidenceCallbackResponse{}
	}
}

func fakeLogResponse(predicate string) evidenceCallbackResponse {
	if predicate == "pay_delta_percent" {
		return evidenceCallbackResponse{EvidenceRefs: []cotv2.EvidenceRef{{SourceID: "audit_log:payroll_snapshot_read:P:2026-06", SourceType: "audit_log", Version: "2026-06"}}}
	}
	return evidenceCallbackResponse{}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "test-model")
	t.Setenv("LLM_OPENAI_BASE_URL", "http://model.local")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_API_BASE", "")
	t.Setenv("OPENAI_API_BASE_URL", "")
	t.Setenv("BASE_URL", "")
}

type cotV2FakeModel struct {
	mu      sync.Mutex
	inputs  [][]*schema.Message
	content string
}

func (m *cotV2FakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]*schema.Message, len(input))
	copy(copied, input)
	m.inputs = append(m.inputs, copied)
	content := m.content
	if strings.TrimSpace(content) == "" {
		content = `{
  "steps": [
    {"kind":"observe","claim_text":"P 的 6 月应发工资比 5 月高 18%。","suggested_subject":"employee:P","suggested_predicate":"pay_delta_percent","suggested_object":18,"suggested_evidence_query":"读取 P 的 5 月和 6 月薪资快照"},
    {"kind":"derive","claim_text":"18% 的增量主要来自季度奖金。","suggested_subject":"employee:P","suggested_predicate":"bonus_delta_dominates","suggested_object":true,"suggested_evidence_query":"执行薪资差异计算"},
    {"kind":"verify","claim_text":"该租户规则要求应发涨幅超过 15% 时核查审批。","suggested_subject":"tenant:acme","suggested_predicate":"requires_approval_over_percent","suggested_object":15,"suggested_evidence_query":"读取 payroll_anomaly v7 政策"},
    {"kind":"decide","claim_text":"本次异常属于有审批支撑的季度奖金，可自动放行。","suggested_subject":"payroll_exception:P:2026-06","suggested_predicate":"release_decision","suggested_object":"auto_release"}
  ]
}`
	}
	return schema.AssistantMessage(content, nil), nil
}

func (m *cotV2FakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}

func (m *cotV2FakeModel) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs)
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
