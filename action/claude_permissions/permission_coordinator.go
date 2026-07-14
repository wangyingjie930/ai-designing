package claudepermissions

import (
	"context"
	"fmt"
)

// PermissionCoordinator 让人工 UI 和 PermissionRequest Hooks 竞争响应同一审批请求。
type PermissionCoordinator struct {
	broker *PermissionBroker
	hooks  []PermissionRequestHook
}

// NewPermissionCoordinator 创建审批协调器；broker 负责公开待审批请求并恢复原调用。
func NewPermissionCoordinator(broker *PermissionBroker, hooks ...PermissionRequestHook) *PermissionCoordinator {
	return &PermissionCoordinator{broker: broker, hooks: hooks}
}

// Resolve 暂停当前工具调用，首个有效 allow/deny 响应获胜并取消其他等待者。
func (c *PermissionCoordinator) Resolve(ctx context.Context, request PermissionRequest) (PermissionResponse, error) {
	if c == nil || c.broker == nil {
		return PermissionResponse{}, fmt.Errorf("permission coordinator requires a broker")
	}
	request = c.broker.Prepare(request)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	registered := make(chan struct{})
	brokerResult := make(chan PermissionResponse, 1)
	brokerError := make(chan error, 1)
	go func() {
		response, err := c.broker.request(runCtx, request, registered)
		if err != nil {
			brokerError <- err
			return
		}
		brokerResult <- response
	}()
	select {
	case <-ctx.Done():
		return PermissionResponse{}, ctx.Err()
	case err := <-brokerError:
		return PermissionResponse{}, err
	case <-registered:
	}

	for _, candidate := range c.hooks {
		if candidate == nil {
			continue
		}
		go func(hook PermissionRequestHook) {
			response, err := hook.ResolvePermission(runCtx, request)
			if err != nil {
				c.broker.Resolve(PermissionResponse{
					RequestID: request.ID,
					Behavior:  PermissionDeny,
					Reason:    fmt.Sprintf("permission request hook failed: %v", err),
				})
				return
			}
			if response == nil || (response.Behavior != PermissionAllow && response.Behavior != PermissionDeny) {
				return
			}
			response.RequestID = request.ID
			c.broker.Resolve(*response)
		}(candidate)
	}

	select {
	case <-ctx.Done():
		return PermissionResponse{}, ctx.Err()
	case err := <-brokerError:
		return PermissionResponse{}, err
	case response := <-brokerResult:
		return response, nil
	}
}
