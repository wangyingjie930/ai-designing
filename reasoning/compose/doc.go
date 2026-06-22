// Package compose 把直接回答、CoT、ToT 和迭代假设检验组合成复杂度路由 Agent。
//
// 这个包对应“先判断问题复杂度，再选择不同推理/验证路径”的流程图：
// 简单问题直接回答，中等问题走单路径 CoT，复杂问题走多路径 ToT；
// 当答案不够可信时，再进入 hypothesis 反证循环，无法收敛时升级给人。
package compose
