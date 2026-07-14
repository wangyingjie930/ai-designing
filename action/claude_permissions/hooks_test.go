package claudepermissions

import (
	"context"
	"testing"
	"time"
)

// TestPermissionCoordinatorUsesFirstDecision 验证 PermissionRequest Hook 与外层 UI 竞争时只有首个结果生效。
func TestPermissionCoordinatorUsesFirstDecision(t *testing.T) {
	broker := NewPermissionBroker(1)
	releaseHook := make(chan struct{})
	coordinator := NewPermissionCoordinator(broker, PermissionRequestHookFunc(func(ctx context.Context, request PermissionRequest) (*PermissionResponse, error) {
		<-releaseHook
		return &PermissionResponse{Behavior: PermissionAllow, Reason: "审批 Hook 放行"}, nil
	}))

	resultCh := make(chan PermissionResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		response, err := coordinator.Resolve(context.Background(), PermissionRequest{
			ToolName:      "send_change_notice",
			ArgumentsJSON: `{"tenant_id":"TENANT-42"}`,
			Reason:        "外部动作需要确认",
		})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- response
	}()

	var request PermissionRequest
	select {
	case request = <-broker.Requests():
	case <-time.After(time.Second):
		t.Fatal("permission request was not published")
	}
	close(releaseHook)

	select {
	case err := <-errCh:
		t.Fatal(err)
	case response := <-resultCh:
		if response.Behavior != PermissionAllow || response.RequestID != request.ID {
			t.Fatalf("response = %+v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("permission hook did not resolve request")
	}

	if broker.Resolve(PermissionResponse{RequestID: request.ID, Behavior: PermissionDeny}) {
		t.Fatal("late UI response unexpectedly won after hook decision")
	}
}
