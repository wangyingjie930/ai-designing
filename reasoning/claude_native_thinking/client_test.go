package claude_native_thinking

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	cotv2 "ai-designing/reasoning/cot_v2"
)

func TestResponsesClientMapsNativeReasoningBlocks(t *testing.T) {
	model := testModelFromDotEnv(t)
	var captured map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %s, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization header = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(bytes.NewBufferString(`{
		  "id": "resp_123",
		  "output": [
		    {
		      "id": "rs_123",
		      "type": "reasoning",
		      "encrypted_content": "enc_reasoning_payload",
		      "summary": [
		        {"type": "summary_text", "text": "先识别薪酬异常，再确认审批证据。"}
		      ]
		    },
		    {
		      "id": "msg_123",
		      "type": "message",
		      "role": "assistant",
		      "content": [
		        {"type": "output_text", "text": "{\"steps\":[{\"kind\":\"observe\",\"claim_text\":\"P 的 6 月应发工资比 5 月高 18%。\",\"suggested_subject\":\"employee:P\",\"suggested_predicate\":\"pay_delta_percent\",\"suggested_object\":18,\"suggested_evidence_query\":\"读取薪资快照\"}]}"}
		      ]
		    }
		  ],
		  "usage": {"input_tokens": 10, "output_tokens": 20, "output_tokens_details": {"reasoning_tokens": 7}}
		}`)),
		}, nil
	})

	client := NewResponsesClient(ResponsesClientConfig{
		APIKey:  "test-key",
		Model:   model,
		BaseURL: "https://model.local/v1",
	})
	client.httpClient = &http.Client{Transport: transport}

	result, err := client.GenerateStepDrafts(context.Background(), GenerateRequest{
		SystemPrompt: "你是一个生产版 CoT 命题草稿生成器。",
		UserPrompt:   "判断员工 P 的奖金异常是否可以自动放行。",
		Reasoning: ThinkingConfig{
			Type:               ThinkingAdaptive,
			Effort:             "medium",
			Summary:            "auto",
			IncludeRedactedCOT: true,
		},
		SchemaName: "cot_v2_step_drafts",
		Schema:     cotv2.StepDraftListJSONSchema(),
		MaxTokens:  2048,
	})
	if err != nil {
		t.Fatal(err)
	}

	if captured["model"] != model {
		t.Fatalf("model = %v", captured["model"])
	}
	reasoning := captured["reasoning"].(map[string]any)
	if reasoning["effort"] != "medium" || reasoning["summary"] != "auto" {
		t.Fatalf("reasoning = %#v", reasoning)
	}
	include, ok := captured["include"].([]any)
	if !ok || !slices.ContainsFunc(include, func(v any) bool { return v == "reasoning.encrypted_content" }) {
		t.Fatalf("include = %#v, want reasoning.encrypted_content", captured["include"])
	}
	text := captured["text"].(map[string]any)
	format := text["format"].(map[string]any)
	if format["type"] != "json_schema" || format["strict"] != true || format["name"] != "cot_v2_step_drafts" {
		t.Fatalf("text.format = %#v", format)
	}

	if result.ResponseID != "resp_123" {
		t.Fatalf("response id = %q", result.ResponseID)
	}
	if result.Text != `{"steps":[{"kind":"observe","claim_text":"P 的 6 月应发工资比 5 月高 18%。","suggested_subject":"employee:P","suggested_predicate":"pay_delta_percent","suggested_object":18,"suggested_evidence_query":"读取薪资快照"}]}` {
		t.Fatalf("text = %q", result.Text)
	}
	if result.Usage.ReasoningTokens != 7 {
		t.Fatalf("reasoning tokens = %d", result.Usage.ReasoningTokens)
	}
	if len(result.Blocks) != 3 {
		t.Fatalf("blocks = %#v", result.Blocks)
	}
	if result.Blocks[0].Type != BlockThinking || !strings.Contains(result.Blocks[0].Thinking, "薪酬异常") {
		t.Fatalf("thinking block = %#v", result.Blocks[0])
	}
	if result.Blocks[1].Type != BlockRedactedThinking || result.Blocks[1].EncryptedContent != "enc_reasoning_payload" {
		t.Fatalf("redacted thinking block = %#v", result.Blocks[1])
	}
	if result.Blocks[1].Data != "enc_reasoning_payload" {
		t.Fatalf("redacted thinking data = %#v", result.Blocks[1])
	}
	if result.Blocks[2].Type != BlockText || !strings.Contains(result.Blocks[2].Text, `"steps"`) {
		t.Fatalf("text block = %#v", result.Blocks[2])
	}
}

func TestClientTestModelComesFromDotEnv(t *testing.T) {
	model := testModelFromDotEnv(t)
	body, err := buildResponsesRequest(model, GenerateRequest{UserPrompt: "测试模型来源。"})
	if err != nil {
		t.Fatal(err)
	}
	if body["model"] != model {
		t.Fatalf("model = %v, want .env LLM_MODEL %s", body["model"], model)
	}
}

func TestResponsesClientRealThinkingFromDotEnv(t *testing.T) {
	config := testClientConfigFromDotEnv(t)
	client := NewResponsesClient(config)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := client.GenerateStepDrafts(ctx, GenerateRequest{
		SystemPrompt: "用思维链的方式思考 为什么",
		UserPrompt:   "员工 P 的 6 月应发工资比 5 月高 18%。",
		Reasoning: ThinkingConfig{
			Type:               ThinkingAdaptive,
			Effort:             "medium",
			Summary:            "concise",
			IncludeRedactedCOT: true,
		},
		MaxTokens: 800,
	})
	if err != nil {
		t.Fatalf("real responses api request failed: %v", err)
	}
	if result.ResponseID == "" {
		t.Fatalf("response id is empty: %+v", result)
	}
	if strings.TrimSpace(result.Text) == "" {
		t.Fatalf("response text is empty: %+v", result)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestBuildNextInputPreservesReasoningItems(t *testing.T) {
	items := BuildNextInput([]ResponseOutputItem{
		{ID: "rs_1", Type: "reasoning", EncryptedContent: "enc_1"},
		{ID: "msg_1", Type: "message"},
	}, "新的工具结果")

	if len(items) != 3 {
		t.Fatalf("items = %#v", items)
	}
	if items[0]["type"] != "reasoning" || items[0]["encrypted_content"] != "enc_1" {
		t.Fatalf("reasoning item = %#v", items[0])
	}
	if items[1]["type"] != "message" {
		t.Fatalf("message item = %#v", items[1])
	}
	if items[2]["role"] != "user" || items[2]["content"] != "新的工具结果" {
		t.Fatalf("user item = %#v", items[2])
	}
}

func TestBuildResponsesRequestUsesPreviousOutputItems(t *testing.T) {
	body, err := buildResponsesRequest(testModelFromDotEnv(t), GenerateRequest{
		UserPrompt: "继续判断下一条审批证据。",
		PreviousOutputItems: []ResponseOutputItem{
			{ID: "rs_1", Type: "reasoning", EncryptedContent: "enc_1"},
			{ID: "msg_1", Type: "message", Role: "assistant"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := body["input"].([]map[string]any)
	if len(input) != 3 {
		t.Fatalf("input = %#v", input)
	}
	if input[0]["type"] != "reasoning" || input[0]["encrypted_content"] != "enc_1" {
		t.Fatalf("previous reasoning item = %#v", input[0])
	}
	if input[1]["type"] != "message" || input[1]["role"] != "assistant" {
		t.Fatalf("previous message item = %#v", input[1])
	}
	if input[2]["role"] != "user" || input[2]["content"] != "继续判断下一条审批证据。" {
		t.Fatalf("next user item = %#v", input[2])
	}
}

func TestBuildResponsesRequestUsesInstructionsForSystemPrompt(t *testing.T) {
	body, err := buildResponsesRequest(testModelFromDotEnv(t), GenerateRequest{
		SystemPrompt: "只输出 JSON。",
		UserPrompt:   "生成候选步骤。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["instructions"] != "只输出 JSON。" {
		t.Fatalf("instructions = %#v", body["instructions"])
	}
	input := body["input"].([]map[string]any)
	if len(input) != 1 || input[0]["role"] != "user" || input[0]["content"] != "生成候选步骤。" {
		t.Fatalf("input = %#v", input)
	}
}

// testModelFromDotEnv 让客户端测试跟随仓库根目录 .env 的真实模型配置，避免测试里残留硬编码模型名。
func testModelFromDotEnv(t *testing.T) string {
	t.Helper()
	return testRequiredDotEnvValue(t, "LLM_MODEL")
}

func testClientConfigFromDotEnv(t *testing.T) ResponsesClientConfig {
	t.Helper()
	env := testDotEnvValues(t)
	return ResponsesClientConfig{
		APIKey:  firstTestEnvValue(env, "OPENAI_API_KEY", "LLM_OPENAI_API_KEY", "LLM_API_KEY"),
		Model:   requiredTestEnvValue(t, env, "LLM_MODEL"),
		BaseURL: firstTestEnvValue(env, "LLM_OPENAI_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_BASE", "OPENAI_API_BASE_URL", "BASE_URL"),
	}
}

func testRequiredDotEnvValue(t *testing.T, key string) string {
	t.Helper()
	return requiredTestEnvValue(t, testDotEnvValues(t), key)
}

func testDotEnvValues(t *testing.T) map[string]string {
	t.Helper()
	envPath := testRepoPath(t, ".env")
	file, err := os.Open(envPath)
	if err != nil {
		t.Fatalf("open %s: %v", envPath, err)
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		values[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read %s: %v", envPath, err)
	}
	return values
}

func requiredTestEnvValue(t *testing.T, values map[string]string, key string) string {
	t.Helper()
	if value := firstTestEnvValue(values, key); value != "" {
		return value
	}
	t.Fatalf("%s is required in %s", key, testRepoPath(t, ".env"))
	return ""
}

func firstTestEnvValue(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func hasContentBlock(blocks []ContentBlock, blockType BlockType) bool {
	for _, block := range blocks {
		if block.Type == blockType {
			return true
		}
	}
	return false
}

func logThinkingBlocks(t *testing.T, result *GenerateResult) {
	t.Helper()
	for i, block := range result.Blocks {
		switch block.Type {
		case BlockThinking:
			t.Logf("thinking[%d]: %s", i, block.Thinking)
		case BlockRedactedThinking:
			t.Logf("redacted_thinking[%d]: encrypted data bytes=%d", i, len(block.Data))
		}
	}
	t.Logf("reasoning_tokens=%d response_id=%s", result.Usage.ReasoningTokens, result.ResponseID)
}

func testRepoPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, name)
		}
		next := filepath.Dir(dir)
		if next == dir {
			t.Fatalf("go.mod not found from %s", dir)
		}
		dir = next
	}
}
