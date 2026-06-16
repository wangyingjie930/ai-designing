package failuretracking

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRecordAndConsultHotelFailure 验证酒店场景失败经验可以写入并被相似上下文召回。
func TestRecordAndConsultHotelFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "failure.sqlite")
	journal, err := NewFailureJournal(ctx, Config{
		DBPath:   path,
		Embedder: NewHashEmbedder(32),
		Now: func() time.Time {
			return time.Unix(1710000000, 0).UTC()
		},
	})
	if err != nil {
		t.Fatalf("NewFailureJournal() error = %v", err)
	}
	defer journal.Close()

	recorded, err := journal.Record(ctx, RecordRequest{
		Context: "酒店客人到店后房态未同步，PMS dirty 但清洁系统 clean，前台反复承诺等待。",
		Error:   "RoomStatusMismatch: PMS dirty 与 housekeeping clean 不一致",
		Fix:     "先核对两个系统更新时间戳，5 分钟内不同步就安排同房型空房或升级房型，并给补偿。",
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if recorded.Entry.ErrorType != "UnclassifiedError" {
		t.Fatalf("ErrorType = %q", recorded.Entry.ErrorType)
	}
	if len(recorded.Entry.Tags) == 0 {
		t.Fatal("expected auto tags")
	}

	consulted, err := journal.Consult(ctx, ConsultRequest{
		CurrentContext: "酒店客诉：PMS 还是 dirty，housekeeping 显示 clean，客人带小孩已经等很久，需要换房或补偿。",
		TopK:           3,
	})
	if err != nil {
		t.Fatalf("Consult() error = %v", err)
	}
	if len(consulted.Matches) != 1 {
		t.Fatalf("matches len = %d, want 1; matches=%+v", len(consulted.Matches), consulted.Matches)
	}
	if consulted.Matches[0].Score < defaultScoreThreshold {
		t.Fatalf("score = %.3f, want >= %.3f", consulted.Matches[0].Score, defaultScoreThreshold)
	}
	if !strings.Contains(consulted.Matches[0].Entry.Fix, "升级房型") {
		t.Fatalf("fix = %q", consulted.Matches[0].Entry.Fix)
	}
}

// TestSQLiteJournalReloadKeepsEntries 验证 SQLite 持久化后，新实例仍能召回已有经验。
func TestSQLiteJournalReloadKeepsEntries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "failure.sqlite")
	journal, err := NewFailureJournal(ctx, Config{
		DBPath:   path,
		Embedder: NewHashEmbedder(32),
	})
	if err != nil {
		t.Fatalf("NewFailureJournal() error = %v", err)
	}
	for _, req := range []RecordRequest{
		{Context: "账单核对失败，发票金额和用量页不一致", Error: "ValueError: invoice amount drift", Fix: "先核对折扣快照和税费，再给审计记录。"},
		{Context: "酒店房态失败，PMS dirty 但清洁系统 clean", Error: "RoomStatusMismatch", Fix: "切换同房型空房并同步房态。"},
	} {
		if _, err := journal.Record(ctx, req); err != nil {
			t.Fatalf("Record() error = %v", err)
		}
	}
	count, err := journal.Count(ctx)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("Count() = %d, want 2", count)
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
	resp, err := reloaded.Consult(ctx, ConsultRequest{
		CurrentContext: "发票和用量页金额不一致，需要核对折扣快照",
		TopK:           1,
	})
	if err != nil {
		t.Fatalf("Consult() error = %v", err)
	}
	if len(resp.Matches) != 1 {
		t.Fatalf("reload matches len = %d, want 1", len(resp.Matches))
	}
	if !strings.Contains(resp.Matches[0].Entry.Context, "账单") {
		t.Fatalf("unexpected match = %+v", resp.Matches[0])
	}
}

// TestSQLiteJournalStoresEmbeddings 验证 SQLite 模式会持久化 entry 与 embedding，并可直接召回。
func TestSQLiteJournalStoresEmbeddings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "failure.sqlite")
	journal, err := NewFailureJournal(ctx, Config{
		DBPath:   path,
		Embedder: NewHashEmbedder(32),
	})
	if err != nil {
		t.Fatalf("NewFailureJournal() error = %v", err)
	}
	defer journal.Close()

	if _, err := journal.Record(ctx, RecordRequest{
		Context: "酒店房态同步失败，PMS dirty 但客房系统 clean。",
		Error:   "RoomStatusMismatch",
		Fix:     "核对两个系统时间戳，超时后切换同房型或升级房型。",
	}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	count, err := journal.Count(ctx)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("Count() = %d, want 1", count)
	}
	resp, err := journal.Consult(ctx, ConsultRequest{
		CurrentContext: "PMS dirty housekeeping clean 客人等待，需要升级房型",
		TopK:           1,
	})
	if err != nil {
		t.Fatalf("Consult() error = %v", err)
	}
	if len(resp.Matches) != 1 {
		t.Fatalf("matches len = %d, want 1", len(resp.Matches))
	}
	if !strings.Contains(resp.Matches[0].Entry.Fix, "升级房型") {
		t.Fatalf("unexpected fix = %q", resp.Matches[0].Entry.Fix)
	}
}
