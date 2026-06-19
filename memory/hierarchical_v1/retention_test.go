package hierarchicalv1

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWriteRejectsAgentInferenceForPolicy 验证 policy/project 这类治理层不允许 agent 推断直接写入。
func TestWriteRejectsAgentInferenceForPolicy(t *testing.T) {
	memory, err := NewHierarchicalMemory(Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = memory.WriteEntry(MemoryEntry{
		Key:           "no_raw_milk",
		Value:         map[string]any{"text": "不使用生乳制品"},
		Layer:         MemoryLayerPolicy,
		Source:        MemorySourceAgentInference,
		EvidenceRefs:  []string{"agent note"},
		TokenEstimate: 10,
	})
	if err == nil || !strings.Contains(err.Error(), "agent cannot write directly") {
		t.Fatalf("expected policy write rejection, got %v", err)
	}
}

// TestWriteRequiresEvidenceForAgentInference 验证需要证据的层不能接受无证据 agent_inference。
func TestWriteRequiresEvidenceForAgentInference(t *testing.T) {
	memory, err := NewHierarchicalMemory(Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = memory.WriteEntry(MemoryEntry{
		Key:           "likes_fish",
		Value:         map[string]any{"text": "用户可能喜欢鱼"},
		Layer:         MemoryLayerUser,
		Source:        MemorySourceAgentInference,
		TokenEstimate: 5,
	})
	if err == nil || !strings.Contains(err.Error(), "requires evidence") {
		t.Fatalf("expected evidence rejection, got %v", err)
	}
}

// TestHumanSourceIsVerified 验证 human/tool 等可信来源即使没有 evidence_refs 也算 verified。
func TestHumanSourceIsVerified(t *testing.T) {
	memory, err := NewHierarchicalMemory(Config{})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := memory.WriteEntry(MemoryEntry{
		Key:           "avoid_spicy",
		Value:         map[string]any{"text": "孩子不吃辣"},
		Layer:         MemoryLayerUser,
		Source:        MemorySourceHuman,
		TokenEstimate: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.IsVerified() {
		t.Fatalf("human entry should be verified: %+v", entry)
	}
}

// TestProposeFromScratchpad 验证 scratchpad 只能生成 verified_trace 候选，不会直接破坏原始临时记忆。
func TestProposeFromScratchpad(t *testing.T) {
	memory, err := NewHierarchicalMemory(Config{})
	if err != nil {
		t.Fatal(err)
	}
	scratch, err := memory.WriteEntry(MemoryEntry{
		Key:           "monday_inventory",
		Value:         map[string]any{"text": "今晚冰箱有番茄、鸡蛋、虾仁"},
		Layer:         MemoryLayerScratchpad,
		Source:        MemorySourceAgentInference,
		EvidenceRefs:  []string{"round-2-user-message"},
		Confidence:    0.7,
		TokenEstimate: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := memory.ProposeFromScratchpad(scratch, MemoryLayerTask)
	if err != nil {
		t.Fatal(err)
	}
	if proposed.Layer != MemoryLayerTask || proposed.Source != MemorySourceVerifiedTrace {
		t.Fatalf("unexpected proposed entry: %+v", proposed)
	}
	if proposed.Key != scratch.Key || len(proposed.EvidenceRefs) != 1 {
		t.Fatalf("proposed entry should preserve key/evidence: %+v", proposed)
	}
	stored, ok := memory.EntryByKey("monday_inventory")
	if !ok || stored.Layer != MemoryLayerScratchpad {
		t.Fatalf("scratchpad source entry should stay unchanged: %+v", stored)
	}
}

// TestAssembleContextAppliesBudgetAndSkipsExpired 验证 assemble_context 按层预算、置信度和过期状态选择条目。
func TestAssembleContextAppliesBudgetAndSkipsExpired(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	memory, err := NewHierarchicalMemory(Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	expired := "2026-06-19T09:00:00Z"
	writes := []MemoryEntry{
		{
			Key:           "policy_low",
			Value:         map[string]any{"text": "低置信规则"},
			Layer:         MemoryLayerPolicy,
			Source:        MemorySourceHuman,
			Confidence:    0.4,
			TokenEstimate: 800,
		},
		{
			Key:           "policy_high",
			Value:         map[string]any{"text": "高置信规则"},
			Layer:         MemoryLayerPolicy,
			Source:        MemorySourceHuman,
			Confidence:    0.9,
			TokenEstimate: 800,
		},
		{
			Key:           "expired_task",
			Value:         map[string]any{"text": "过期任务"},
			Layer:         MemoryLayerTask,
			Source:        MemorySourceHuman,
			TokenEstimate: 10,
			ValidUntil:    &expired,
		},
		{
			Key:           "scratch",
			Value:         map[string]any{"text": "临时库存"},
			Layer:         MemoryLayerScratchpad,
			Source:        MemorySourceAgentInference,
			TokenEstimate: 10,
		},
	}
	for _, entry := range writes {
		if _, err := memory.WriteEntry(entry); err != nil {
			t.Fatal(err)
		}
	}
	selected := memory.AssembleContext()
	if len(selected) != 2 {
		t.Fatalf("selected=%d, want 2: %+v", len(selected), selected)
	}
	if selected[0].Key != "policy_high" {
		t.Fatalf("highest confidence policy should win budget, selected=%+v", selected)
	}
	if selected[1].Layer != MemoryLayerScratchpad {
		t.Fatalf("scratchpad should be selected after policy layers: %+v", selected)
	}
	accessed, ok := memory.EntryByKey("policy_high")
	if !ok || accessed.LastAccessedAt == nil {
		t.Fatalf("selected entry should update last_accessed_at: %+v", accessed)
	}
}

// TestToolsetAndToolsCompile 验证 ADK tool schema 能从新请求类型推断出来。
func TestToolsetAndToolsCompile(t *testing.T) {
	memory, err := NewHierarchicalMemory(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewWriteMemoryTool(memory); err != nil {
		t.Fatal(err)
	}
	if _, err := NewProposeScratchpadTool(memory); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAssembleContextTool(memory); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHealthReportTool(memory); err != nil {
		t.Fatal(err)
	}

	toolset := Toolset{Memory: memory}
	writeResp, err := toolset.WriteMemory(context.Background(), WriteRequest{
		Layer:         MemoryLayerUser,
		Source:        MemorySourceHuman,
		Key:           "avoid_cilantro",
		Value:         map[string]any{"text": "不吃香菜"},
		TokenEstimate: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if writeResp.Entry.Layer != MemoryLayerUser {
		t.Fatalf("write response=%+v", writeResp)
	}
	contextResp, err := toolset.AssembleContext(context.Background(), AssembleContextRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(contextResp.Entries) != 1 {
		t.Fatalf("context entries=%+v", contextResp.Entries)
	}
}

// TestDefaultInstructionRequiresStableKeys 验证默认提示词明确要求复用已有 key 做 upsert。
func TestDefaultInstructionRequiresStableKeys(t *testing.T) {
	instruction := DefaultMealPlanningInstruction()
	for _, want := range []string{"复用", "已有 key", "同一个 key", "不要用 started/completed/progress"} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("instruction should mention %q: %s", want, instruction)
		}
	}
}

// TestSQLitePersistsNonScratchpadOnly 验证非 scratchpad 层默认可落 SQLite，scratchpad 保持进程内临时态。
func TestSQLitePersistsNonScratchpadOnly(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "hierarchical-v1.sqlite")
	memory, err := NewHierarchicalMemory(Config{DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Write(ctx, WriteRequest{
		Layer:         MemoryLayerUser,
		Source:        MemorySourceHuman,
		Key:           "avoid_spicy",
		Value:         map[string]any{"text": "孩子不吃辣"},
		TokenEstimate: 5,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Write(ctx, WriteRequest{
		Layer:         MemoryLayerScratchpad,
		Source:        MemorySourceAgentInference,
		Key:           "today_inventory",
		Value:         map[string]any{"text": "今天冰箱有虾仁"},
		TokenEstimate: 5,
	}); err != nil {
		t.Fatal(err)
	}
	if err := memory.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewHierarchicalMemory(Config{DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if _, ok := reloaded.EntryByKey("avoid_spicy"); !ok {
		t.Fatalf("user memory should be restored from sqlite")
	}
	if _, ok := reloaded.EntryByKey("today_inventory"); ok {
		t.Fatalf("scratchpad memory should not be restored from sqlite")
	}
}
