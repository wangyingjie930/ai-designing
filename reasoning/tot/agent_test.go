package tot

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestThinkNodeTrajectorySnapshotAndBackpropagate 验证节点轨迹、快照恢复和奖励回传语义。
func TestThinkNodeTrajectorySnapshotAndBackpropagate(t *testing.T) {
	root := NewThinkNode("如何安排两天旅行？", nil)
	child := NewThinkNode("先确认预算", root)
	output := "预算为 3000 元"
	child.Output = &output
	child.Backpropagate(0.8)

	if root.Visits != 1 || child.Visits != 1 {
		t.Fatalf("visits root=%d child=%d", root.Visits, child.Visits)
	}
	if !strings.Contains(child.Trajectory(), "Output: 预算为 3000 元") {
		t.Fatalf("trajectory missing output: %s", child.Trajectory())
	}
	restored := ThinkNodeFromSnapshot(root.ToSnapshot(), nil)
	if len(restored.Children) != 1 || restored.Children[0].Parent != restored {
		t.Fatalf("restored parent/children broken: %+v", restored)
	}
}

// TestExtractDatasets 验证 SFT 和 RLHF 数据抽取与 AG2 的叶子/兄弟节点策略一致。
func TestExtractDatasets(t *testing.T) {
	root := NewThinkNode("选择方案", nil)
	a := NewThinkNode("方案 A", root)
	b := NewThinkNode("方案 B", root)
	a.Value = 0.9
	b.Value = 0.3
	sft := ExtractSFTDataset(root)
	if len(sft) != 1 || sft[0].Instruction != "选择方案" || !strings.Contains(sft[0].Response, "方案 A") {
		t.Fatalf("unexpected sft samples: %+v", sft)
	}
	pairs := ExtractRLHFPreferenceDataset(root, 0.2)
	if len(pairs) != 1 || !strings.Contains(pairs[0].PreferredResponse, "方案 A") {
		t.Fatalf("unexpected preference pairs: %+v", pairs)
	}
}

// TestParserExtractsReflectionOptionsAndRatings 覆盖 thinker/grader 文本解析边界。
func TestParserExtractsReflectionOptionsAndRatings(t *testing.T) {
	reply := `REFLECTION:
已有轨迹需要继续。

**Possible Options:**
Option 1: 先拆问题
Option 2: TERMINATE
结束`
	if got := parseReflection(reply); !strings.Contains(got, "继续") {
		t.Fatalf("reflection = %q", got)
	}
	options := parseOptions(reply)
	if len(options) != 2 || options[1] != "TERMINATE\n结束" {
		t.Fatalf("options = %#v", options)
	}
	if reward := parseReward("Rating: 9\n理由", 10); reward < 0.88 || reward > 0.90 {
		t.Fatalf("reward = %f", reward)
	}
	rewards, details, ok := parseBatchRewards("Option 1: 好\nRating: 8\n\nOption 2: 差\nRating: 2", 2, 10)
	if !ok || len(rewards) != 2 || len(details) != 2 || rewards[0] <= rewards[1] {
		t.Fatalf("batch rewards=%v details=%v ok=%v", rewards, details, ok)
	}
}

// TestBeamSearchGeneratesPooledAnswer 验证 beam search 会扩展、评分、收束并生成最终答案。
func TestBeamSearchGeneratesPooledAnswer(t *testing.T) {
	ctx := context.Background()
	fake := &scriptedReasoningModel{}
	agent, err := NewReasoningAgent(ctx, Config{
		Model: fake,
		ReasonConfig: ReasonConfig{
			Method:         MethodBeamSearch,
			MaxDepth:       1,
			BeamSize:       1,
			AnswerApproach: AnswerApproachPool,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := agent.GenerateText(ctx, "如何安排周末学习？")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Answer, "最终答案") {
		t.Fatalf("answer = %q", resp.Answer)
	}
	if resp.Root == nil || len(resp.Root.Children) != 4 {
		t.Fatalf("root children = %+v", resp.Root)
	}
	if fake.Count() < 3 {
		t.Fatalf("model calls = %d, want at least 3", fake.Count())
	}
}

// TestMCTSBackpropagatesOutcomeReward 验证 MCTS/LATS 共用路径会把 outcome reward 回传到根节点。
func TestMCTSBackpropagatesOutcomeReward(t *testing.T) {
	ctx := context.Background()
	fake := &scriptedReasoningModel{}
	agent, err := NewReasoningAgent(ctx, Config{
		Model: fake,
		ReasonConfig: ReasonConfig{
			Method:   MethodMCTS,
			MaxDepth: 1,
			NSim:     2,
		},
		Rand: rand.New(rand.NewSource(1)),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := agent.GenerateText(ctx, "怎么选晚餐？")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Root == nil || resp.Root.Visits != 2 {
		t.Fatalf("root visits = %+v", resp.Root)
	}
	if !strings.Contains(resp.Answer, "最终答案") {
		t.Fatalf("answer = %q", resp.Answer)
	}
}

// TestADKRunnerEmitsAssistantResponse 验证 reasoning/tot 可以作为 Eino ADK Agent 被 Runner 调度。
func TestADKRunnerEmitsAssistantResponse(t *testing.T) {
	ctx := context.Background()
	runner, core, err := NewRunner(ctx, Config{
		Model: &scriptedReasoningModel{},
		ReasonConfig: ReasonConfig{
			Method:   MethodDFS,
			MaxDepth: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	iter := runner.Query(ctx, "请推理一个时间安排")
	var final string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		msg, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			t.Fatal(err)
		}
		if msg.Role == schema.Assistant {
			final = msg.Content
		}
		if _, ok := event.Output.CustomizedOutput.(*Response); !ok {
			t.Fatalf("customized output type = %T", event.Output.CustomizedOutput)
		}
	}
	if core.Root() == nil || !strings.Contains(final, "最终答案") {
		t.Fatalf("final=%q root=%+v", final, core.Root())
	}
}

// scriptedReasoningModel 根据 prompt 形态返回固定回复，用来验证控制流而不是模型能力。
type scriptedReasoningModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

// Generate 记录输入并按 thinker/grader/final answer 三类 prompt 返回响应。
func (m *scriptedReasoningModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]*schema.Message, len(input))
	copy(copied, input)
	m.inputs = append(m.inputs, copied)
	user := ""
	system := ""
	if len(input) > 0 {
		system = input[0].Content
		user = input[len(input)-1].Content
	}
	switch {
	case strings.Contains(user, "How should the thinking process continue?"):
		return schema.AssistantMessage(strings.Join([]string{
			"REFLECTION:",
			"当前问题适合拆成可执行判断。",
			"",
			"**Possible Options:**",
			"Option 1: 先拆解关键约束",
			"Option 2: 比较两个候选路线",
			"Option 3: 检查边界条件",
			"Option 4: TERMINATE",
		}, "\n"), nil), nil
	case strings.Contains(user, "Rate:") || strings.Contains(system, "评价"):
		return schema.AssistantMessage("Rating: 9\n理由：轨迹有效。", nil), nil
	case strings.Contains(user, "Final Answer:"):
		return schema.AssistantMessage("最终答案：先确认约束，再执行最小可行方案。", nil), nil
	case strings.Contains(user, "Messages:") || strings.Contains(user, "**Messages:**"):
		return schema.AssistantMessage("QUESTION: 汇总问题\nCURRENT_QUESTION: 请继续回答。", nil), nil
	default:
		return nil, errors.New("unexpected prompt: " + user)
	}
}

// Stream 当前单测不依赖流式输出，只满足 Eino BaseChatModel 接口。
func (m *scriptedReasoningModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}

// Count 返回模型调用次数，确认推理过程确实走过多个内部角色。
func (m *scriptedReasoningModel) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs)
}

var _ adk.Agent = (*ADKAgent)(nil)
