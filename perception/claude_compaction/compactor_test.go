package claudecompaction

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestCompactConversationSummarizesOnlyModelWorthyContext 验证全量压缩只把需要语义总结的历史交给模型。
func TestCompactConversationSummarizesOnlyModelWorthyContext(t *testing.T) {
	model := &recordingSummarizer{
		summary: "<analysis>内部草稿不应进入压缩后上下文</analysis><summary>1. 当前工作: 正在排查支付网关。\n2. 下一步: 继续检查 order.go。</summary>",
	}
	compactor := NewCompactor(Config{
		Now: func() time.Time { return time.Unix(100, 0).UTC() },
	})

	result, err := compactor.Compact(context.Background(), CompactRequest{
		Messages: []Message{
			TextMessage("old-user", RoleUser, "压缩前旧消息，不应该再次交给模型总结", WithTokens(100)),
			CompactBoundary("previous-boundary", CompactMetadata{Trigger: TriggerAuto, PreTokens: 100}),
			UserMessage("after-user", []Block{
				TextBlock("用户贴了支付失败截图"),
				ImageBlock("base64-screenshot-raw"),
			}, WithTokens(80)),
			AttachmentMessage("stale-skill-listing", Attachment{
				Type:    AttachmentSkillListing,
				Content: "过期 skill listing，压缩后会由代码重新注入，不应该交给总结模型",
			}, WithTokens(50)),
			TextMessage("assistant-last", RoleAssistant, "我正在读 order.go", WithTokens(40)),
		},
		SummaryModel:      model,
		Trigger:           TriggerManual,
		SuppressFollowUp:  true,
		TranscriptPath:    "/tmp/session.jsonl",
		MessagesToKeep:    []Message{TextMessage("slash-compact", RoleUser, "/compact", WithTokens(1))},
		RestoreReferences: sampleRestoreReferences(),
	})
	if err != nil {
		t.Fatalf("compact failed: %v", err)
	}

	if len(model.calls) != 1 {
		t.Fatalf("expected one summarizer call, got %d", len(model.calls))
	}
	modelInput := joinMessages(model.calls[0].Messages)
	if strings.Contains(modelInput, "压缩前旧消息") {
		t.Fatalf("model saw messages before compact boundary: %s", modelInput)
	}
	if strings.Contains(modelInput, "base64-screenshot-raw") {
		t.Fatalf("model saw raw image payload: %s", modelInput)
	}
	if !strings.Contains(modelInput, "[image]") {
		t.Fatalf("model should see image placeholder, got: %s", modelInput)
	}
	if strings.Contains(modelInput, "过期 skill listing") {
		t.Fatalf("model saw reinjected attachment: %s", modelInput)
	}

	if got := result.Messages[0].Subtype; got != SubtypeCompactBoundary {
		t.Fatalf("first post-compact message should be compact boundary, got %q", got)
	}
	if !result.Messages[1].IsCompactSummary {
		t.Fatalf("second post-compact message should be compact summary: %#v", result.Messages[1])
	}
	if strings.Contains(result.Messages[1].Content, "<analysis>") {
		t.Fatalf("analysis scratchpad leaked into summary: %s", result.Messages[1].Content)
	}
	if !strings.Contains(result.Messages[1].Content, "Summary:\n1. 当前工作") {
		t.Fatalf("summary tags were not formatted: %s", result.Messages[1].Content)
	}
	if result.Messages[2].ID != "slash-compact" {
		t.Fatalf("messagesToKeep should be placed before restore attachments: %#v", result.Messages)
	}
	if !containsAttachment(result.Messages, AttachmentCompactFileReference) {
		t.Fatalf("expected restored file attachment in post-compact messages: %#v", result.Messages)
	}
	if !containsAttachment(result.Messages, AttachmentInvokedSkill) {
		t.Fatalf("expected invoked skill attachment in post-compact messages: %#v", result.Messages)
	}
	if !containsAttachment(result.Messages, AttachmentDeferredToolDelta) {
		t.Fatalf("expected deferred tool attachment in post-compact messages: %#v", result.Messages)
	}
	if !containsAttachment(result.Messages, AttachmentMCPInstructionDelta) {
		t.Fatalf("expected MCP instruction attachment in post-compact messages: %#v", result.Messages)
	}
}

// TestSessionMemoryCompactionUsesPreparedNotesWithoutModelCall 验证 session memory 路径不会再现场总结全部历史。
func TestSessionMemoryCompactionUsesPreparedNotesWithoutModelCall(t *testing.T) {
	model := &recordingSummarizer{summary: "<summary>不应该被调用</summary>"}
	compactor := NewCompactor(Config{
		Now: func() time.Time { return time.Unix(200, 0).UTC() },
	})

	result, err := compactor.Compact(context.Background(), CompactRequest{
		Messages: []Message{
			TextMessage("m1", RoleUser, "已经沉淀进 session memory 的旧问题", WithTokens(80)),
			TextMessage("m2", RoleAssistant, "已经沉淀进 session memory 的旧动作", WithTokens(80)),
			TextMessage("m3", RoleUser, "最近新增、需要原文保留的用户反馈", WithTokens(30)),
		},
		SummaryModel: model,
		Trigger:      TriggerAuto,
		SessionMemory: &SessionMemory{
			Enabled:                 true,
			Content:                 "# Current State\n正在排查支付网关 502。\n# Next\n继续检查 order.go。",
			LastSummarizedMessageID: "m2",
		},
	})
	if err != nil {
		t.Fatalf("compact from session memory failed: %v", err)
	}
	if len(model.calls) != 0 {
		t.Fatalf("session memory compact should not call summarizer, got %d calls", len(model.calls))
	}
	if !strings.Contains(result.Messages[1].Content, "正在排查支付网关 502") {
		t.Fatalf("session memory content not inserted into summary: %s", result.Messages[1].Content)
	}
	if !containsMessageID(result.Messages, "m3") {
		t.Fatalf("recent unsummarized message should be kept verbatim: %#v", result.Messages)
	}
	if containsMessageID(result.Messages, "m1") || containsMessageID(result.Messages, "m2") {
		t.Fatalf("already summarized messages should not be kept verbatim: %#v", result.Messages)
	}
}

// TestMicrocompactClearsOldToolResultsWithoutSummaryModel 验证 microcompact 只清旧工具结果，不触发总结模型。
func TestMicrocompactClearsOldToolResultsWithoutSummaryModel(t *testing.T) {
	messages := []Message{
		ToolUseMessage("read-call", "Read", "tool-1", WithTokens(5)),
		ToolResultMessage("read-result", "tool-1", strings.Repeat("old read result ", 20), WithTokens(100)),
		ToolUseMessage("bash-call", "Bash", "tool-2", WithTokens(5)),
		ToolResultMessage("bash-result", "tool-2", "fresh bash result", WithTokens(20)),
	}

	result := Microcompact(messages, MicrocompactConfig{KeepRecentToolResults: 1})
	if !strings.Contains(messageContentByID(result.Messages, "read-result"), ClearedToolResultContent) {
		t.Fatalf("old compactable tool result should be cleared: %#v", result.Messages)
	}
	if got := messageContentByID(result.Messages, "bash-result"); got != "fresh bash result" {
		t.Fatalf("recent tool result should be preserved, got %q", got)
	}
	if result.TokensSaved <= 0 || len(result.ClearedToolUseIDs) != 1 {
		t.Fatalf("expected saved token accounting, got %#v", result)
	}
}

// TestAutoCompactPolicyUsesEffectiveWindowAndRecursionGuards 验证自动压缩阈值和递归保护。
func TestAutoCompactPolicyUsesEffectiveWindowAndRecursionGuards(t *testing.T) {
	policy := AutoCompactPolicy{
		Enabled:                true,
		ContextWindow:          1000,
		MaxSummaryOutputTokens: 200,
		BufferTokens:           100,
	}
	messages := []Message{
		TextMessage("m1", RoleUser, "heavy", WithTokens(500)),
		TextMessage("m2", RoleAssistant, "heavy", WithTokens(250)),
	}

	decision := policy.Decide(messages, QuerySourceMain)
	if decision.EffectiveWindow != 800 || decision.Threshold != 700 {
		t.Fatalf("unexpected threshold decision: %#v", decision)
	}
	if !decision.ShouldCompact {
		t.Fatalf("expected auto compact at 750 tokens: %#v", decision)
	}

	if policy.Decide(messages, QuerySourceCompact).ShouldCompact {
		t.Fatalf("compact summarizer should not recursively auto-compact")
	}
	if policy.Decide(messages, QuerySourceSessionMemory).ShouldCompact {
		t.Fatalf("session memory summarizer should not recursively auto-compact")
	}
}

// TestQueryPipelineAutoCompactReplacesMessagesForQueryAndYieldsWriteback 验证 query 主链会用压缩结果替换本轮模型上下文。
func TestQueryPipelineAutoCompactReplacesMessagesForQueryAndYieldsWriteback(t *testing.T) {
	model := &recordingSummarizer{summary: "<summary>已总结旧历史，继续处理最近问题。</summary>"}
	pipeline := NewQueryPipeline(QueryPipelineConfig{
		Compactor: NewCompactor(Config{
			Now: func() time.Time { return time.Unix(300, 0).UTC() },
		}),
		AutoCompactPolicy: AutoCompactPolicy{
			Enabled:       true,
			ContextWindow: 1000,
			BufferTokens:  100,
		},
		Microcompact: &MicrocompactConfig{KeepRecentToolResults: 1},
	})

	result, err := pipeline.Run(context.Background(), QueryPipelineRequest{
		Messages: []Message{
			TextMessage("very-old", RoleUser, "上一次 compact boundary 之前的旧消息", WithTokens(120)),
			CompactBoundary("previous-boundary", CompactMetadata{Trigger: TriggerAuto, PreTokens: 120}),
			ToolUseMessage("read-call", "Read", "tool-old", WithTokens(10)),
			ToolResultMessage("read-result", "tool-old", strings.Repeat("旧 Read 结果 ", 40), WithTokens(300)),
			ToolUseMessage("bash-call", "Bash", "tool-new", WithTokens(10)),
			ToolResultMessage("bash-result", "tool-new", "最近 Bash 结果", WithTokens(60)),
			TextMessage("user-last", RoleUser, "继续检查 order.go", WithTokens(380)),
		},
		SummaryModel:     model,
		Source:           QuerySourceMain,
		Trigger:          TriggerAuto,
		SuppressFollowUp: true,
		TaskBudget:       &TaskBudget{Total: 2000, Remaining: 1500},
	})
	if err != nil {
		t.Fatalf("query pipeline failed: %v", err)
	}

	if result.Compaction == nil {
		t.Fatalf("expected autocompact result: %#v", result.AutoCompact)
	}
	if len(model.calls) != 1 {
		t.Fatalf("expected one summarizer call, got %d", len(model.calls))
	}
	modelInput := joinMessages(model.calls[0].Messages)
	if strings.Contains(modelInput, "上一次 compact boundary 之前") {
		t.Fatalf("model saw messages before latest compact boundary: %s", modelInput)
	}
	if !strings.Contains(modelInput, ClearedToolResultContent) {
		t.Fatalf("microcompact should run before autocompact, got model input: %s", modelInput)
	}
	if !sameMessageIDs(result.MessagesForQuery, result.Compaction.Messages) {
		t.Fatalf("messagesForQuery should be replaced by post-compact messages")
	}
	if !sameMessageIDs(result.YieldedMessages, result.Compaction.Messages) {
		t.Fatalf("yielded messages should be post-compact messages")
	}
	if !sameMessageIDs(result.WritebackMessages, result.Compaction.Messages) {
		t.Fatalf("writeback messages should be post-compact messages")
	}
	if result.TaskBudget == nil || result.TaskBudget.Remaining >= 1500 {
		t.Fatalf("task budget should record consumed pre-compact context: %#v", result.TaskBudget)
	}
}

// TestQueryPipelineAppliesToolResultBudgetBeforeSummary 验证工具结果预算在 microcompact 和自动压缩前先改写大结果。
func TestQueryPipelineAppliesToolResultBudgetBeforeSummary(t *testing.T) {
	model := &recordingSummarizer{summary: "<summary>工具结果已按预算裁剪。</summary>"}
	pipeline := NewQueryPipeline(QueryPipelineConfig{
		Compactor: NewCompactor(Config{
			Now: func() time.Time { return time.Unix(350, 0).UTC() },
		}),
		ToolResultBudget: &ToolResultBudgetConfig{MaxResultTokens: 80},
		AutoCompactPolicy: AutoCompactPolicy{
			Enabled:                true,
			ContextWindow:          1000,
			MaxSummaryOutputTokens: 200,
			BufferTokens:           100,
		},
		Microcompact: &MicrocompactConfig{KeepRecentToolResults: 10},
	})

	result, err := pipeline.Run(context.Background(), QueryPipelineRequest{
		Messages: []Message{
			ToolUseMessage("read-call", "Read", "tool-big", WithTokens(5)),
			ToolResultMessage("read-result", "tool-big", "敏感且巨大的 Read 原始结果 "+strings.Repeat("payload ", 60), WithTokens(260)),
			TextMessage("user-last", RoleUser, "继续处理", WithTokens(760)),
		},
		SummaryModel: model,
		Source:       QuerySourceMain,
		Trigger:      TriggerAuto,
	})
	if err != nil {
		t.Fatalf("query pipeline failed: %v", err)
	}
	if result.ToolResultBudget.TokensSaved <= 0 || len(result.ToolResultBudget.ReplacedToolUseIDs) != 1 {
		t.Fatalf("expected tool result budget replacement, got %#v", result.ToolResultBudget)
	}
	modelInput := joinMessages(model.calls[0].Messages)
	if strings.Contains(modelInput, "敏感且巨大的 Read 原始结果") {
		t.Fatalf("summary model should not see oversized raw tool result: %s", modelInput)
	}
	if !strings.Contains(modelInput, ToolResultBudgetReplacementContent) {
		t.Fatalf("summary model should see budget replacement marker: %s", modelInput)
	}
	if len(result.Microcompact.ClearedToolUseIDs) != 0 {
		t.Fatalf("microcompact should run after budget and keep recent tool results here: %#v", result.Microcompact)
	}
}

// TestQueryPipelineAppliesHistorySnipBeforeMicrocompactAndAutocompact 验证 HISTORY_SNIP 位于 microcompact 和 autocompact 之前。
func TestQueryPipelineAppliesHistorySnipBeforeMicrocompactAndAutocompact(t *testing.T) {
	pipeline := NewQueryPipeline(QueryPipelineConfig{
		HistorySnip:  &HistorySnipConfig{Enabled: true, MaxTokens: 450, KeepRecentMessages: 2},
		Microcompact: &MicrocompactConfig{KeepRecentToolResults: 1},
		AutoCompactPolicy: AutoCompactPolicy{
			Enabled:                true,
			ContextWindow:          1000,
			MaxSummaryOutputTokens: 200,
			BufferTokens:           100,
		},
	})

	result, err := pipeline.Run(context.Background(), QueryPipelineRequest{
		Messages: []Message{
			ToolUseMessage("old-read-call", "Read", "tool-old", WithTokens(5)),
			ToolResultMessage("old-read-result", "tool-old", "会被 snip 移除的旧工具结果", WithTokens(700)),
			TextMessage("recent-user", RoleUser, "最近问题", WithTokens(120)),
			TextMessage("recent-assistant", RoleAssistant, "最近回答", WithTokens(120)),
		},
		Source: QuerySourceMain,
	})
	if err != nil {
		t.Fatalf("query pipeline failed: %v", err)
	}
	if !result.HistorySnip.Changed || result.HistorySnip.BoundaryMessage == nil {
		t.Fatalf("expected history snip boundary, got %#v", result.HistorySnip)
	}
	if result.AutoCompact.ShouldCompact {
		t.Fatalf("snip should reduce context before autocompact decision: %#v", result.AutoCompact)
	}
	if containsMessageID(result.MessagesForQuery, "old-read-result") {
		t.Fatalf("snipped old tool result should not reach microcompact/autocompact view: %#v", result.MessagesForQuery)
	}
	if got := result.MessagesForQuery[0].Subtype; got != SubtypeHistorySnipBoundary {
		t.Fatalf("messagesForQuery should start with snip boundary, got %q", got)
	}
	if len(result.Microcompact.ClearedToolUseIDs) != 0 {
		t.Fatalf("microcompact should not clear a tool result already removed by snip: %#v", result.Microcompact)
	}
	if len(result.YieldedMessages) != 1 || result.YieldedMessages[0].Subtype != SubtypeHistorySnipBoundary {
		t.Fatalf("snip boundary should be yielded before model call: %#v", result.YieldedMessages)
	}
	if len(result.WritebackMessages) != 1 || result.WritebackMessages[0].Subtype != SubtypeHistorySnipBoundary {
		t.Fatalf("snip boundary should be available for session writeback: %#v", result.WritebackMessages)
	}
}

// TestQuerySessionWritebackMakesResumeStartAtNewBoundary 验证压缩写回 session 后，下一轮只从新 boundary 后恢复。
func TestQuerySessionWritebackMakesResumeStartAtNewBoundary(t *testing.T) {
	model := &recordingSummarizer{summary: "<summary>压缩后的会话摘要。</summary>"}
	session := NewQuerySession([]Message{
		TextMessage("ancient", RoleUser, "很早之前的问题", WithTokens(250)),
		TextMessage("recent", RoleAssistant, "最近还在处理的问题", WithTokens(250)),
	})
	pipeline := NewQueryPipeline(QueryPipelineConfig{
		Compactor: NewCompactor(Config{
			Now: func() time.Time { return time.Unix(400, 0).UTC() },
		}),
		AutoCompactPolicy: AutoCompactPolicy{
			Enabled:       true,
			ContextWindow: 700,
			BufferTokens:  100,
		},
	})

	first, err := session.PrepareNextQuery(context.Background(), pipeline, QueryPipelineRequest{
		SummaryModel: model,
		Source:       QuerySourceMain,
		Trigger:      TriggerAuto,
	})
	if err != nil {
		t.Fatalf("first query preparation failed: %v", err)
	}
	if first.Compaction == nil || len(first.WritebackMessages) == 0 {
		t.Fatalf("first run should compact and write back boundary: %#v", first)
	}

	second, err := session.PrepareNextQuery(context.Background(), NewQueryPipeline(QueryPipelineConfig{
		AutoCompactPolicy: AutoCompactPolicy{Enabled: false},
	}), QueryPipelineRequest{Source: QuerySourceMain})
	if err != nil {
		t.Fatalf("second query preparation failed: %v", err)
	}
	if containsMessageID(second.MessagesForQuery, "ancient") || containsMessageID(second.MessagesForQuery, "recent") {
		t.Fatalf("resume view should start at compact boundary, got %#v", second.MessagesForQuery)
	}
	if got := second.MessagesForQuery[0].Subtype; got != SubtypeCompactBoundary {
		t.Fatalf("resume view should begin with compact boundary, got %q", got)
	}
}

// recordingSummarizer 记录压缩模型收到的请求，方便测试模型边界。
type recordingSummarizer struct {
	calls   []SummaryRequest
	summary string
}

// Summarize 记录请求并返回固定摘要。
func (r *recordingSummarizer) Summarize(_ context.Context, req SummaryRequest) (string, error) {
	r.calls = append(r.calls, req)
	return r.summary, nil
}

// sampleRestoreReferences 构造压缩后由代码补回的上下文引用。
func sampleRestoreReferences() RestoreReferences {
	return RestoreReferences{
		Files: []RestoredFile{{
			Path:    "/repo/order.go",
			Reason:  "压缩后补回最近读过的文件",
			Content: "func Pay() {}",
		}},
		InvokedSkills: []RestoredSkill{{
			Name:    "verify",
			Content: "先运行验证命令",
		}},
		DeferredTools:   []string{"read_recent_file"},
		MCPInstructions: []string{"继续遵守 MCP 工具的权限边界"},
	}
}

// joinMessages 拼接消息内容，方便断言模型输入边界。
func joinMessages(messages []Message) string {
	var parts []string
	for _, msg := range messages {
		parts = append(parts, msg.FlattenContent())
	}
	return strings.Join(parts, "\n")
}

// containsAttachment 判断压缩结果是否包含指定附件类型。
func containsAttachment(messages []Message, typ AttachmentType) bool {
	for _, msg := range messages {
		if msg.Attachment != nil && msg.Attachment.Type == typ {
			return true
		}
	}
	return false
}

// containsMessageID 判断消息列表里是否存在指定 ID。
func containsMessageID(messages []Message, id string) bool {
	for _, msg := range messages {
		if msg.ID == id {
			return true
		}
	}
	return false
}

// messageContentByID 返回指定消息的文本内容。
func messageContentByID(messages []Message, id string) string {
	for _, msg := range messages {
		if msg.ID == id {
			return msg.Content
		}
	}
	return ""
}

// sameMessageIDs 判断两组消息是否按相同顺序引用同一批消息。
func sameMessageIDs(a []Message, b []Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}
