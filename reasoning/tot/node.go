package tot

import (
	"fmt"
	"strings"
)

// ThinkNode 是推理树中的一个节点，保存思考步骤、评分、父子关系和访问统计。
type ThinkNode struct {
	Content       string       `json:"content"`
	Value         float64      `json:"value"`
	Parent        *ThinkNode   `json:"-"`
	Reflection    string       `json:"reflection,omitempty"`
	RatingDetails string       `json:"rating_details,omitempty"`
	Output        *string      `json:"output,omitempty"`
	Depth         int          `json:"depth"`
	Children      []*ThinkNode `json:"children,omitempty"`
	Visits        int          `json:"visits"`
}

// NodeSnapshot 是 ThinkNode 的无 parent 版本，用于稳定序列化和反序列化。
type NodeSnapshot struct {
	Content       string         `json:"content"`
	Value         float64        `json:"value"`
	Depth         int            `json:"depth"`
	Reflection    string         `json:"reflection,omitempty"`
	RatingDetails string         `json:"rating_details,omitempty"`
	Output        *string        `json:"output,omitempty"`
	Visits        int            `json:"visits"`
	Children      []NodeSnapshot `json:"children,omitempty"`
}

// NewThinkNode 创建一个推理节点，并自动挂到 parent.Children 上。
func NewThinkNode(content string, parent *ThinkNode) *ThinkNode {
	node := &ThinkNode{
		Content:  content,
		Parent:   parent,
		Children: []*ThinkNode{},
	}
	if parent != nil {
		node.Depth = parent.Depth + 1
		parent.Children = append(parent.Children, node)
	}
	return node
}

// TrajectoryArr 返回从根节点到当前节点的完整轨迹数组。
func (n *ThinkNode) TrajectoryArr() []string {
	if n == nil {
		return nil
	}
	step := "Content: " + n.Content
	if n.Output != nil {
		step += "\nOutput: " + *n.Output
	}
	if n.Parent != nil {
		return append(n.Parent.TrajectoryArr(), step)
	}
	return []string{"# Question:\n" + step + "\n---\n"}
}

// Trajectory 返回 AG2 风格的可读推理轨迹文本。
func (n *ThinkNode) Trajectory() string {
	traj := n.TrajectoryArr()
	if len(traj) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString(traj[0])
	builder.WriteString("# Trajectory:\n")
	for i, step := range traj[1:] {
		builder.WriteString(fmt.Sprintf("\nStep %d:\n%s", i+1, step))
	}
	return builder.String()
}

// Backpropagate 使用移动平均把叶子奖励回传到当前节点和所有祖先节点。
func (n *ThinkNode) Backpropagate(reward float64) {
	for node := n; node != nil; node = node.Parent {
		node.Visits++
		node.Value = (node.Value*float64(node.Visits-1) + reward) / float64(node.Visits)
	}
}

// String 返回包含深度、分数和访问次数的调试文本。
func (n *ThinkNode) String() string {
	if n == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s -> Depth: %d Value: %.4f Visits: %d", n.Content, n.Depth, n.Value, n.Visits)
}

// ToSnapshot 把推理树转换成不含 parent 指针的快照，避免 JSON 环形引用。
func (n *ThinkNode) ToSnapshot() NodeSnapshot {
	if n == nil {
		return NodeSnapshot{}
	}
	children := make([]NodeSnapshot, 0, len(n.Children))
	for _, child := range n.Children {
		children = append(children, child.ToSnapshot())
	}
	return NodeSnapshot{
		Content:       n.Content,
		Value:         n.Value,
		Depth:         n.Depth,
		Reflection:    n.Reflection,
		RatingDetails: n.RatingDetails,
		Output:        n.Output,
		Visits:        n.Visits,
		Children:      children,
	}
}

// ThinkNodeFromSnapshot 从序列化快照恢复推理树，并重新补齐 parent 指针。
func ThinkNodeFromSnapshot(snapshot NodeSnapshot, parent *ThinkNode) *ThinkNode {
	node := NewThinkNode(snapshot.Content, parent)
	node.Value = snapshot.Value
	node.Depth = snapshot.Depth
	node.Reflection = snapshot.Reflection
	node.RatingDetails = snapshot.RatingDetails
	node.Output = snapshot.Output
	node.Visits = snapshot.Visits
	for _, child := range snapshot.Children {
		ThinkNodeFromSnapshot(child, node)
	}
	return node
}
