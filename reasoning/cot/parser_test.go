package cot

import "testing"

// TestAddStepAndWeakestStep 验证 Go 版本保留 Python dataclass 的追加和最弱步骤语义。
func TestAddStepAndWeakestStep(t *testing.T) {
	var chain ChainOfThought
	chain.AddStep("先锁定硬约束")
	chain.AddStep("比较候选方案", 0.42)
	chain.AddStep("输出行动清单", 0.77)

	weakest := chain.WeakestStep()
	if weakest == nil || weakest.StepNumber != 2 || weakest.Confidence != 0.42 {
		t.Fatalf("weakest = %+v", weakest)
	}
	if chain.Steps[0].Confidence != 1 {
		t.Fatalf("default confidence = %v", chain.Steps[0].Confidence)
	}
}

// TestParseChainFromCodeFence 验证解析器能处理模型偶发输出的 JSON 代码块。
func TestParseChainFromCodeFence(t *testing.T) {
	chain, err := ParseChain("```json\n{\"steps\":[{\"step_number\":9,\"content\":\"先处理医院复诊\",\"confidence\":0.8}],\"final_answer\":\"让妹妹上午陪诊。\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if len(chain.Steps) != 1 || chain.Steps[0].StepNumber != 1 || chain.FinalAnswer == "" {
		t.Fatalf("chain = %+v", chain)
	}
}
