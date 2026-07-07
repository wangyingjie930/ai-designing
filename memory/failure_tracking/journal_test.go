package failuretracking

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestJournal(t *testing.T) (*FailureJournal, context.Context) {
	t.Helper()
	ctx := context.Background()
	journal, err := NewFailureJournal(ctx, Config{
		DBPath:   filepath.Join(t.TempDir(), "failure.sqlite"),
		Embedder: NewHashEmbedder(32),
		Now: func() time.Time {
			return time.Unix(1710000000, 0).UTC()
		},
	})
	if err != nil {
		t.Fatalf("NewFailureJournal() error = %v", err)
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	return journal, ctx
}

func payrollBoundaryLeakEntry(status Status) FailureEntry {
	return FailureEntry{
		FailureID:   "fj-payroll-20260612-001",
		TaskFamily:  "payroll_run",
		Boundary:    BoundarySafety,
		Category:    CategoryBoundaryLeak,
		Severity:    "high",
		Status:      status,
		TenantScope: "tenant-A",
		Source:      "verification_gate",
		Symptom: strings.Join([]string{
			"薪资组中 30 名员工的租户归属与当前任务不一致。",
			"payroll_group_id 指向客户 B，当前任务属于客户 A。",
		}, "\n"),
		RootCause: strings.Join([]string{
			"create_payroll_batch 的工具封装允许从自然语言摘要中提取 payroll_group_id。",
			"上下文残留的客户 B 旧说明覆盖了 SessionState 中客户 A 的确定性状态值。",
			"工具调用前也没有校验 tenant scope。",
		}, "\n"),
		Repair: []string{
			"废弃错误薪资组，将 30 名员工恢复到客户 A",
			"禁止工具封装从自然语言中解析 payroll_group_id",
			"payroll_*_id 只允许从 SessionState 绑定",
			"写入前强制校验 tenant / org / month",
		},
		Lesson: []string{
			"跨租户机械参数必须从 SessionState 确定性绑定",
			"机械 id 不得通过摘要、聊天历史或账本正文传递",
			"任何写操作前都要验证 id 的 tenant scope",
		},
		DoNot: []string{
			"不从自然语言摘要复制 payroll_*_id",
			"scope 未通过校验时，不调用写入型薪酬工具",
		},
		Evidence: EvidenceBundle{
			WorkspaceRefs:   []string{"workspace:M3/create_payroll_batch"},
			NarrativeRefs:   []string{"narrative:ledger/event-028"},
			StateRefs:       []string{"state:payroll_group_id@tenant-A/org-shanghai/month-2026-06"},
			ObservationRefs: []string{"gate:tenant_scope_mismatch", "tool:create_payroll_batch#20260612-1421"},
		},
		Recall: RecallTrigger{
			TaskFamilies:   []string{"payroll_run"},
			Tools:          []string{"create_payroll_batch", "create_payroll_snapshot"},
			MechanicalKeys: []string{"payroll_group_id", "payroll_batch_id"},
			Categories:     []string{"boundary_leak", "mechanical_state_mismatch"},
		},
		RetentionTier: "hot",
		ReviewedBy:    "payroll-risk-reviewer",
		ReviewNote:    "验证闸门确认过的跨租户写入事故。",
	}
}

// TestRecordStoresSixLayerFailureEntry 验证失败日记按文档六层结构保存，而不是只存 context/error/fix。
func TestRecordStoresSixLayerFailureEntry(t *testing.T) {
	journal, ctx := newTestJournal(t)

	recorded, err := journal.Record(ctx, RecordRequest{Entry: payrollBoundaryLeakEntry(StatusApproved)})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if recorded.Entry.Boundary != BoundarySafety {
		t.Fatalf("Boundary = %q, want %q", recorded.Entry.Boundary, BoundarySafety)
	}
	if recorded.Entry.Status != StatusApproved {
		t.Fatalf("Status = %q, want %q", recorded.Entry.Status, StatusApproved)
	}
	if len(recorded.Entry.Evidence.StateRefs) != 1 {
		t.Fatalf("state evidence refs = %+v", recorded.Entry.Evidence.StateRefs)
	}
	if len(recorded.Entry.Recall.MechanicalKeys) != 2 {
		t.Fatalf("mechanical recall keys = %+v", recorded.Entry.Recall.MechanicalKeys)
	}
	if recorded.Entry.RecalledCount != 0 {
		t.Fatalf("new entry recalled_count = %d, want 0", recorded.Entry.RecalledCount)
	}
}

// TestRecordRejectsIncompleteFailureJournalShape 验证不能把没有证据和召回条件的故事当失败日记。
func TestRecordRejectsIncompleteFailureJournalShape(t *testing.T) {
	journal, ctx := newTestJournal(t)
	entry := payrollBoundaryLeakEntry(StatusApproved)
	entry.Evidence = EvidenceBundle{}
	entry.Recall = RecallTrigger{}

	_, err := journal.Record(ctx, RecordRequest{Entry: entry})
	if err == nil {
		t.Fatal("Record() error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("error = %v, want evidence validation", err)
	}
}

// TestRecordRejectsUngroundedRecallKeys 验证 tool/mechanical_keys 必须来自证据包，不能由模型凭空编。
func TestRecordRejectsUngroundedRecallKeys(t *testing.T) {
	journal, ctx := newTestJournal(t)
	entry := payrollBoundaryLeakEntry(StatusApproved)
	entry.Evidence = EvidenceBundle{
		WorkspaceRefs:   []string{"workspace:M3/manual_review"},
		NarrativeRefs:   []string{"narrative:ledger/event-028"},
		StateRefs:       []string{"state:tenant_id@tenant-A"},
		ObservationRefs: []string{"gate:tenant_scope_mismatch"},
	}

	_, err := journal.Record(ctx, RecordRequest{Entry: entry})
	if err == nil {
		t.Fatal("Record() error = nil, want grounding validation error")
	}
	if !strings.Contains(err.Error(), "mechanical key") {
		t.Fatalf("error = %v, want mechanical key grounding validation", err)
	}
}

// TestRecallBeforeToolUsesStructuredKeysAndApprovedOnly 验证高风险工具调用前优先按结构化键召回，且只有 approved 条目生效。
func TestRecallBeforeToolUsesStructuredKeysAndApprovedOnly(t *testing.T) {
	journal, ctx := newTestJournal(t)

	draft := payrollBoundaryLeakEntry(StatusDraft)
	draft.FailureID = "fj-payroll-draft"
	draft.ReviewedBy = ""
	draft.ReviewNote = ""
	if _, err := journal.Record(ctx, RecordRequest{Entry: draft}); err != nil {
		t.Fatalf("Record(draft) error = %v", err)
	}
	approved := payrollBoundaryLeakEntry(StatusApproved)
	if _, err := journal.Record(ctx, RecordRequest{Entry: approved}); err != nil {
		t.Fatalf("Record(approved) error = %v", err)
	}
	unrelated := payrollBoundaryLeakEntry(StatusApproved)
	unrelated.FailureID = "fj-expense-20260612-001"
	unrelated.TaskFamily = "expense_approval"
	unrelated.Category = CategoryPolicyViolation
	unrelated.Evidence = EvidenceBundle{
		WorkspaceRefs:   []string{"workspace:M2/approve_expense"},
		NarrativeRefs:   []string{"narrative:ledger/event-018"},
		StateRefs:       []string{"state:expense_report_id@tenant-A/month-2026-06"},
		ObservationRefs: []string{"tool:approve_expense#20260612-1102"},
	}
	unrelated.Recall = RecallTrigger{
		TaskFamilies:   []string{"expense_approval"},
		Tools:          []string{"approve_expense"},
		MechanicalKeys: []string{"expense_report_id"},
		Categories:     []string{"policy_violation"},
	}
	if _, err := journal.Record(ctx, RecordRequest{Entry: unrelated}); err != nil {
		t.Fatalf("Record(unrelated) error = %v", err)
	}

	resp, err := journal.RecallBeforeTool(ctx, RecallRequest{
		TaskFamily:     "payroll_run",
		Tool:           "create_payroll_batch",
		MechanicalKeys: []string{"payroll_group_id"},
		Categories:     []string{"boundary_leak"},
		TopK:           3,
	})
	if err != nil {
		t.Fatalf("RecallBeforeTool() error = %v", err)
	}
	if len(resp.Matches) != 1 {
		t.Fatalf("matches len = %d, want 1; matches=%+v", len(resp.Matches), resp.Matches)
	}
	match := resp.Matches[0]
	if match.Entry.FailureID != "fj-payroll-20260612-001" {
		t.Fatalf("match id = %q, want approved payroll failure", match.Entry.FailureID)
	}
	if match.StructuredScore <= 0 {
		t.Fatalf("structured score = %.3f, want > 0", match.StructuredScore)
	}
	if !strings.Contains(match.Reason, "task_family") || !strings.Contains(match.Reason, "tool") {
		t.Fatalf("reason = %q, want structured reason", match.Reason)
	}

	reloaded, err := journal.Get(ctx, "fj-payroll-20260612-001")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if reloaded.RecalledCount != 1 {
		t.Fatalf("recalled_count = %d, want 1", reloaded.RecalledCount)
	}
}

// TestSQLiteJournalReloadKeepsStructuredEntry 验证 SQLite 重开后仍保留结构化字段和召回行为。
func TestSQLiteJournalReloadKeepsStructuredEntry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "failure.sqlite")
	journal, err := NewFailureJournal(ctx, Config{
		DBPath:   path,
		Embedder: NewHashEmbedder(32),
	})
	if err != nil {
		t.Fatalf("NewFailureJournal() error = %v", err)
	}
	if _, err := journal.Record(ctx, RecordRequest{Entry: payrollBoundaryLeakEntry(StatusApproved)}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reloaded, err := NewFailureJournal(ctx, Config{
		DBPath:   path,
		Embedder: NewHashEmbedder(32),
	})
	if err != nil {
		t.Fatalf("reload NewFailureJournal() error = %v", err)
	}
	defer reloaded.Close()

	resp, err := reloaded.RecallBeforeTool(ctx, RecallRequest{
		TaskFamily:     "payroll_run",
		Tool:           "create_payroll_batch",
		MechanicalKeys: []string{"payroll_group_id"},
		TopK:           1,
	})
	if err != nil {
		t.Fatalf("RecallBeforeTool() error = %v", err)
	}
	if len(resp.Matches) != 1 {
		t.Fatalf("reload matches len = %d, want 1", len(resp.Matches))
	}
	if len(resp.Matches[0].Entry.DoNot) == 0 {
		t.Fatalf("DoNot should survive reload, entry=%+v", resp.Matches[0].Entry)
	}
}
