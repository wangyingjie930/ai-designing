package tot

import "fmt"

// ExtractSFTDataset 从最高分叶子节点抽取 SFT 样本；并列最高分会全部保留。
func ExtractSFTDataset(root *ThinkNode) []SFTSample {
	if root == nil {
		return nil
	}
	leaves := leafNodes(root)
	if len(leaves) == 0 {
		return nil
	}
	maxValue := leaves[0].Value
	for _, leaf := range leaves[1:] {
		if leaf.Value > maxValue {
			maxValue = leaf.Value
		}
	}
	prefix := rootQuestionPrefix(root)
	samples := make([]SFTSample, 0)
	for _, leaf := range leaves {
		if leaf.Value != maxValue {
			continue
		}
		response := leaf.Trajectory()
		if len(response) >= len(prefix) && response[:len(prefix)] == prefix {
			response = response[len(prefix):]
		}
		samples = append(samples, SFTSample{
			Instruction: root.Content,
			Response:    response,
		})
	}
	return samples
}

// ExtractRLHFPreferenceDataset 通过比较兄弟节点分数差异抽取偏好样本。
func ExtractRLHFPreferenceDataset(root *ThinkNode, contrastiveThreshold float64) []PreferencePair {
	if root == nil {
		return nil
	}
	if contrastiveThreshold <= 0 || contrastiveThreshold >= 1 {
		return nil
	}
	var pairs []PreferencePair
	var traverse func(node *ThinkNode)
	traverse = func(node *ThinkNode) {
		if node == nil || len(node.Children) == 0 {
			return
		}
		for i, childA := range node.Children {
			for j, childB := range node.Children {
				if i == j {
					continue
				}
				if isBetterSibling(childA, childB, contrastiveThreshold) {
					pairs = append(pairs, PreferencePair{
						Instruction:          node.Trajectory(),
						Reflection:           node.Reflection,
						PreferredResponse:    fmt.Sprintf("Step %d: %s", childA.Depth, childA.Content),
						DispreferredResponse: fmt.Sprintf("Step %d: %s", childB.Depth, childB.Content),
					})
				}
			}
		}
		for _, child := range node.Children {
			traverse(child)
		}
	}
	traverse(root)
	return pairs
}

// leafNodes 递归收集所有叶子节点，供数据集抽取复用。
func leafNodes(node *ThinkNode) []*ThinkNode {
	if node == nil {
		return nil
	}
	if len(node.Children) == 0 {
		return []*ThinkNode{node}
	}
	var leaves []*ThinkNode
	for _, child := range node.Children {
		leaves = append(leaves, leafNodes(child)...)
	}
	return leaves
}

// rootQuestionPrefix 构造 Trajectory 里根问题前缀，用于截出纯 response。
func rootQuestionPrefix(root *ThinkNode) string {
	if root == nil {
		return ""
	}
	return "# Question:\nContent: " + root.Content + "\n---\n# Trajectory:\n"
}

// isBetterSibling 复刻 AG2 的兄弟节点偏好判断：MCTS 看 visits 归一化，beam 看直接分数差。
func isBetterSibling(a *ThinkNode, b *ThinkNode, threshold float64) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Visits > 0 && b.Visits > 0 {
		return a.Value/float64(a.Visits)-b.Value/float64(b.Visits) > threshold
	}
	return a.Value-b.Value > threshold
}
