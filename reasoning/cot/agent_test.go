package cot

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestSolveRunsReasonAndVerify 验证完整链路会先生成 CoT，再逐步调用 verifier。
func TestSolveRunsReasonAndVerify(t *testing.T) {
	fake := &fakeModel{}
	agent, err := NewReasoningAgent(context.Background(), Config{Model: fake, Scenario: "家庭照护排班"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := agent.Solve(context.Background(), Request{Question: "请安排周三照护排班。"})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Verified || len(response.Chain.Steps) != 3 || response.FinalAnswer == "" {
		t.Fatalf("response = %+v", response)
	}
	if fake.Count() != 4 {
		t.Fatalf("model calls = %d, want 4", fake.Count())
	}
}

// fakeModel 用固定文本模拟 reasoner 和 verifier，避免单元测试依赖真实模型。
type fakeModel struct {
	mu     sync.Mutex
	inputs [][]*schema.Message
}

// Generate 按 prompt 类型返回结构化推理链或校验结果。
func (m *fakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
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
	if strings.Contains(system, "显性推理链 Agent") {
		return schema.AssistantMessage(`{
  "steps": [
    {"step_number": 1, "content": "客户会议不能改期，因此本人不能承担 09:30-11:00 的陪诊主责。", "confidence": 0.94},
    {"step_number": 2, "content": "妹妹上午可请假 2 小时，适合覆盖父亲到院和母亲自助机协助。", "confidence": 0.82},
    {"step_number": 3, "content": "下午孩子接送不与妹妹上午请假冲突，应由本人或备用接送人处理。", "confidence": 0.76}
  ],
  "final_answer": "推荐让妹妹上午陪父亲到院并协助母亲完成关键流程，我保留客户会议，下午负责孩子接送并准备备用打车方案。"
}`, nil), nil
	}
	if strings.Contains(user, "校验规则") {
		if !strings.Contains(user, "原问题：") || !strings.Contains(user, "请安排周三照护排班。") {
			return schema.AssistantMessage("INVALID\n未提供原问题，当前步骤缺少可核验依据。", nil), nil
		}
		return schema.AssistantMessage("VALID\n步骤与硬约束一致。", nil), nil
	}
	return schema.AssistantMessage("VALID\n默认通过。", nil), nil
}

// Stream 当前测试不使用流式输出，只满足 Eino BaseChatModel 接口。
func (m *fakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("stream", nil)}), nil
}

// Count 返回模型调用次数，用来证明 verifier 逐步执行。
func (m *fakeModel) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs)
}
