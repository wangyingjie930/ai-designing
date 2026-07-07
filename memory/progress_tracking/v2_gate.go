package progresstracking

import "strings"

// VerificationGateResult 是里程碑验收闸门的确定性检查结果。
type VerificationGateResult struct {
	Passed  bool     `json:"passed"`
	Missing []string `json:"missing,omitempty"`
}

// EvaluateVerificationGate 根据当前恢复包检查里程碑验收条件是否满足。
func EvaluateVerificationGate(packet ResumePacket) VerificationGateResult {
	var missing []string
	for _, acceptance := range normalizeStringList(packet.CurrentMilestone.Acceptance) {
		switch {
		case strings.HasPrefix(acceptance, "STATE."):
			if !packetHasStateRef(packet, acceptance) {
				missing = append(missing, acceptance)
			}
		case strings.HasPrefix(acceptance, "evidence:"):
			required := strings.TrimPrefix(acceptance, "evidence:")
			if !packetHasEvidence(packet, required) {
				missing = append(missing, acceptance)
			}
		}
	}
	return VerificationGateResult{
		Passed:  len(missing) == 0,
		Missing: missing,
	}
}

// EvaluateDrift 用规则版漂移哨兵判断最近事件是否偏离目标边界或缺少证据。
func EvaluateDrift(packet ResumePacket) DriftSignal {
	total := len(packet.RecentLedger)
	if total == 0 {
		return DriftSignal{
			Level:             DriftLevelOK,
			GoalRelevance:     1,
			MilestoneProgress: 1,
			EvidenceHealth:    1,
			ErrorPressure:     0,
			Reason:            "no recent ledger events",
		}
	}

	evidenceCount := 0
	nonGoalHit := ""
	for _, event := range packet.RecentLedger {
		if len(normalizeStringList(event.EvidenceRefs)) > 0 {
			evidenceCount++
		}
		if nonGoalHit == "" {
			nonGoalHit = matchedNonGoal(packet.Goal.NonGoals, event.Event)
		}
	}
	evidenceHealth := float64(evidenceCount) / float64(total)
	if nonGoalHit != "" {
		return DriftSignal{
			Level:             DriftLevelPause,
			GoalRelevance:     0.25,
			MilestoneProgress: evidenceHealth,
			EvidenceHealth:    evidenceHealth,
			ErrorPressure:     0,
			Reason:            "recent event touched non-goal: " + nonGoalHit,
		}
	}
	if evidenceHealth == 0 {
		return DriftSignal{
			Level:             DriftLevelRecenter,
			GoalRelevance:     0.6,
			MilestoneProgress: 0.5,
			EvidenceHealth:    0,
			ErrorPressure:     0,
			Reason:            "recent events have no evidence refs",
		}
	}
	if evidenceHealth < 0.5 {
		return DriftSignal{
			Level:             DriftLevelWatch,
			GoalRelevance:     0.8,
			MilestoneProgress: 0.7,
			EvidenceHealth:    evidenceHealth,
			ErrorPressure:     0,
			Reason:            "recent evidence refs are sparse",
		}
	}
	return DriftSignal{
		Level:             DriftLevelOK,
		GoalRelevance:     1,
		MilestoneProgress: 1,
		EvidenceHealth:    evidenceHealth,
		ErrorPressure:     0,
		Reason:            "recent ledger is aligned",
	}
}

// packetHasStateRef 检查机械态索引或账本 state_delta 是否包含目标状态坐标。
func packetHasStateRef(packet ResumePacket, required string) bool {
	key := strings.TrimPrefix(required, "STATE.")
	for _, stateKey := range packet.MechanicalStateKeys {
		if stateKey == key || "STATE."+stateKey == required {
			return true
		}
	}
	for _, event := range packet.RecentLedger {
		for _, written := range event.StateDelta.Write {
			if written == required || strings.TrimPrefix(written, "STATE.") == key {
				return true
			}
		}
	}
	return false
}

// packetHasEvidence 检查最近账本中是否有目标证据引用。
func packetHasEvidence(packet ResumePacket, required string) bool {
	for _, event := range packet.RecentLedger {
		for _, evidence := range event.EvidenceRefs {
			if evidence == required {
				return true
			}
		}
	}
	return false
}

// matchedNonGoal 判断最近动作是否触碰目标契约声明的非目标边界。
func matchedNonGoal(nonGoals []string, eventText string) string {
	eventText = strings.TrimSpace(eventText)
	for _, nonGoal := range normalizeStringList(nonGoals) {
		if eventText == "" {
			continue
		}
		if strings.Contains(eventText, nonGoal) {
			return nonGoal
		}
		core := normalizeNonGoalCore(nonGoal)
		if core != "" && strings.Contains(eventText, core) {
			return nonGoal
		}
	}
	return ""
}

// normalizeNonGoalCore 去掉常见否定前缀，用于发现“尝试做了不该做的事”。
func normalizeNonGoalCore(nonGoal string) string {
	nonGoal = strings.TrimSpace(nonGoal)
	for _, prefix := range []string{"不直接", "不允许", "不修改", "不改", "不要", "不能", "禁止", "不"} {
		if strings.HasPrefix(nonGoal, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(nonGoal, prefix))
		}
	}
	return nonGoal
}
