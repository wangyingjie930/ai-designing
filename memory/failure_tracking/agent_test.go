package failuretracking

import (
	"strings"
	"testing"
)

// TestHotelInstructionDoesNotInventRecallKeys 验证提示词不会把不存在的业务工具或机械状态键喂给模型。
func TestHotelInstructionDoesNotInventRecallKeys(t *testing.T) {
	instruction := DefaultHotelRecoveryInstruction()
	for _, fabricatedKey := range []string{
		"assign_room_or_compensation",
		"pms_room_status",
		"housekeeping_room_status",
		"room_inventory",
		"compensation_approval",
	} {
		if strings.Contains(instruction, fabricatedKey) {
			t.Fatalf("instruction should not seed fabricated recall key %q; instruction=%s", fabricatedKey, instruction)
		}
	}
	if !strings.Contains(instruction, "没有真实工具调用或 SessionState 绑定时，不要填写 tool 或 mechanical_keys") {
		t.Fatalf("instruction should tell the model not to invent tool/mechanical_keys; instruction=%s", instruction)
	}
}
