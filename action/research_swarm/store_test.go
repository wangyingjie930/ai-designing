package researchswarm

import (
	"context"
	"path/filepath"
	"testing"
)

// TestStoreMailboxConsumesEachMessageOnce 验证 mailbox 是外部进程协作的唯一通信边界，消息被消费后不会重复投递。
func TestStoreMailboxConsumesEachMessageOnce(t *testing.T) {
	store := openTestStore(t)
	msg, err := store.EnqueueMessage(context.Background(), MailboxMessage{
		TeamName:    "team-a",
		FromAgent:   "leader@team-a",
		ToAgent:     "searcher@team-a",
		Kind:        MessageKindTask,
		ContentJSON: `{"topic":"agent search"}`,
	})
	requireNoError(t, err)

	got, err := store.ConsumeMessages(context.Background(), "team-a", "searcher@team-a", 10)
	requireNoError(t, err)
	if len(got) != 1 || got[0].ID != msg.ID {
		t.Fatalf("got messages = %#v, want id %d", got, msg.ID)
	}
	again, err := store.ConsumeMessages(context.Background(), "team-a", "searcher@team-a", 10)
	requireNoError(t, err)
	if len(again) != 0 {
		t.Fatalf("message consumed twice: %#v", again)
	}
}

// TestStoreKeepsTeamDataIsolated 验证多次 demo 运行通过 team_name 隔离共享 SQLite 数据。
func TestStoreKeepsTeamDataIsolated(t *testing.T) {
	store := openTestStore(t)
	_, err := store.SaveSourceCard(context.Background(), SourceCard{
		TeamName:    "team-a",
		Query:       "agent search",
		Title:       "Agent Search A",
		URL:         "https://example.com/a",
		Snippet:     "A",
		Source:      "fake",
		Credibility: "medium",
		CreatedBy:   "searcher@team-a",
	})
	requireNoError(t, err)
	_, err = store.SaveSourceCard(context.Background(), SourceCard{
		TeamName:    "team-b",
		Query:       "agent search",
		Title:       "Agent Search B",
		URL:         "https://example.com/b",
		Snippet:     "B",
		Source:      "fake",
		Credibility: "medium",
		CreatedBy:   "searcher@team-b",
	})
	requireNoError(t, err)

	cards, err := store.ListSourceCards(context.Background(), "team-a")
	requireNoError(t, err)
	if len(cards) != 1 || cards[0].URL != "https://example.com/a" {
		t.Fatalf("team-a cards = %#v", cards)
	}
}

// TestStorePersistsReportSectionsWithEvidence 验证 writer 可以把报告章节和 source card 引用一起落库。
func TestStorePersistsReportSectionsWithEvidence(t *testing.T) {
	store := openTestStore(t)
	section, err := store.SaveReportSection(context.Background(), ReportSection{
		TeamName:    "team-a",
		Section:     "结论",
		Content:     "外部搜索要通过资料卡和审查链路进入报告。",
		EvidenceIDs: []int64{1, 2},
		CreatedBy:   "writer@team-a",
	})
	requireNoError(t, err)

	got, err := store.ListReportSections(context.Background(), "team-a")
	requireNoError(t, err)
	if len(got) != 1 || got[0].ID != section.ID || len(got[0].EvidenceIDs) != 2 {
		t.Fatalf("sections = %#v, want saved section with evidence ids", got)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "research_swarm.sqlite"))
	requireNoError(t, err)
	t.Cleanup(func() {
		requireNoError(t, store.Close())
	})
	return store
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
