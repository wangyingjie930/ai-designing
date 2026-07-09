package claude_native_thinking

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNormalizeTrajectoryOutputFiltersOrphanAndTrailingThinking(t *testing.T) {
	items := NormalizeTrajectoryOutput([]ResponseOutputItem{
		{ID: "rs_orphan", Type: "reasoning", EncryptedContent: "orphan"},
	})
	if len(items) != 0 {
		t.Fatalf("orphan reasoning items = %#v, want empty", items)
	}

	items = NormalizeTrajectoryOutput([]ResponseOutputItem{
		{ID: "msg_1", Type: "message", Role: "assistant"},
		{ID: "rs_trailing", Type: "reasoning", EncryptedContent: "trailing"},
	})
	if len(items) != 1 || items[0].ID != "msg_1" {
		t.Fatalf("trailing reasoning was not filtered: %#v", items)
	}
}

func TestTrajectoryPreservesReasoningStateForNextTurn(t *testing.T) {
	trajectory := NewTrajectory(TrajectoryConfig{
		SystemPrompt: "只输出 JSON。",
		Reasoning: ThinkingConfig{
			Type:               ThinkingAdaptive,
			Effort:             "medium",
			Summary:            "concise",
			IncludeRedactedCOT: true,
		},
		MaxTokens: 800,
	})

	trajectory.Record(&GenerateResult{Output: []ResponseOutputItem{
		{ID: "rs_1", Type: "reasoning", EncryptedContent: "enc_1"},
		{
			ID:   "msg_1",
			Type: "message",
			Role: "assistant",
			Content: []ResponseContent{
				{Type: "output_text", Text: "437"},
			},
		},
	}})

	req := trajectory.NextRequest("继续解释这个结果。")
	body, err := buildResponsesRequest("test-model", req)
	if err != nil {
		t.Fatal(err)
	}
	input := body["input"].([]map[string]any)
	if len(input) != 3 {
		t.Fatalf("input = %#v", input)
	}
	if input[0]["type"] != "reasoning" || input[0]["encrypted_content"] != "enc_1" {
		t.Fatalf("reasoning item = %#v", input[0])
	}
	if input[1]["type"] != "message" || input[1]["role"] != "assistant" {
		t.Fatalf("assistant message item = %#v", input[1])
	}
	if input[2]["role"] != "user" || input[2]["content"] != "继续解释这个结果。" {
		t.Fatalf("next user item = %#v", input[2])
	}
	if body["instructions"] != "只输出 JSON。" {
		t.Fatalf("instructions = %#v", body["instructions"])
	}
	include := body["include"].([]string)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v", include)
	}
}

func TestBuildResponsesRequestFiltersUnsafePreviousOutputItems(t *testing.T) {
	body, err := buildResponsesRequest("test-model", GenerateRequest{
		UserPrompt: "继续。",
		PreviousOutputItems: []ResponseOutputItem{
			{ID: "msg_1", Type: "message", Role: "assistant"},
			{ID: "rs_trailing", Type: "reasoning", EncryptedContent: "trailing"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := body["input"].([]map[string]any)
	if len(input) != 2 {
		t.Fatalf("input = %#v", input)
	}
	if input[0]["type"] != "message" || input[0]["id"] != "msg_1" {
		t.Fatalf("message item = %#v", input[0])
	}
	if input[1]["role"] != "user" || input[1]["content"] != "继续。" {
		t.Fatalf("user item = %#v", input[1])
	}
}

func TestTrajectoryRealTwoTurnThinkingFromDotEnv(t *testing.T) {
	client := NewResponsesClient(testClientConfigFromDotEnv(t))
	trajectory := NewTrajectory(TrajectoryConfig{
		SystemPrompt: "你是一个简洁计算助手。最终答案只输出数字。",
		Reasoning: ThinkingConfig{
			Type:    ThinkingAdaptive,
			Effort:  "medium",
			Summary: "concise",
		},
		MaxTokens: 800,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	first, err := trajectory.RunTurn(ctx, client, "计算 19*23。")
	if err != nil {
		t.Fatalf("first real turn failed: %v", err)
	}
	logThinkingBlocks(t, first)
	if !hasContentBlock(first.Blocks, BlockThinking) || !hasContentBlock(first.Blocks, BlockRedactedThinking) {
		t.Fatalf("first turn missing thinking blocks: %+v", first.Blocks)
	}
	if len(trajectory.OutputItems()) < 2 {
		t.Fatalf("trajectory did not preserve first output: %#v", trajectory.OutputItems())
	}

	second, err := trajectory.RunTurn(ctx, client, "把上一轮最终数字加 1，只输出数字。")
	if err != nil {
		t.Fatalf("second real turn failed: %v", err)
	}
	logThinkingBlocks(t, second)
	if !strings.Contains(second.Text, "438") {
		t.Fatalf("second turn did not use trajectory context: %q", second.Text)
	}
}
