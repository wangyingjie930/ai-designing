package claudepermissions

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// PermissionBroker 负责发布待审批请求，并让外层决策恢复原工具调用。
type PermissionBroker struct {
	requests chan PermissionRequest
	nextID   atomic.Uint64
	mu       sync.Mutex
	pending  map[string]chan PermissionResponse
}

// NewPermissionBroker 创建带缓冲的审批队列，缓冲至少为一避免 Hook 抢先时阻塞发布。
func NewPermissionBroker(buffer int) *PermissionBroker {
	if buffer < 1 {
		buffer = 1
	}
	return &PermissionBroker{
		requests: make(chan PermissionRequest, buffer),
		pending:  make(map[string]chan PermissionResponse),
	}
}

// Requests 暴露只读请求通道，CLI、Web UI 或远端控制器都可以消费。
func (b *PermissionBroker) Requests() <-chan PermissionRequest {
	if b == nil {
		return nil
	}
	return b.requests
}

// Prepare 为审批请求分配稳定 id，让 UI 与 Hooks 看到同一个请求标识。
func (b *PermissionBroker) Prepare(request PermissionRequest) PermissionRequest {
	if b == nil {
		return request
	}
	if request.ID == "" {
		request.ID = fmt.Sprintf("permission-%d", b.nextID.Add(1))
	}
	return request
}

// Request 注册并发布审批请求，然后阻塞到首个外层决策或上下文取消。
func (b *PermissionBroker) Request(ctx context.Context, request PermissionRequest) (PermissionResponse, error) {
	return b.request(ctx, request, nil)
}

// request 支持协调器在 pending 注册完成后再启动 PermissionRequest Hooks。
func (b *PermissionBroker) request(ctx context.Context, request PermissionRequest, registered chan<- struct{}) (PermissionResponse, error) {
	if b == nil {
		return PermissionResponse{}, fmt.Errorf("permission broker is required")
	}
	request = b.Prepare(request)
	responseCh := make(chan PermissionResponse, 1)
	b.mu.Lock()
	if _, exists := b.pending[request.ID]; exists {
		b.mu.Unlock()
		return PermissionResponse{}, fmt.Errorf("permission request %q already exists", request.ID)
	}
	b.pending[request.ID] = responseCh
	b.mu.Unlock()
	if registered != nil {
		close(registered)
	}

	select {
	case response := <-responseCh:
		return response, nil
	case b.requests <- request:
	case <-ctx.Done():
		b.removePending(request.ID, responseCh)
		return PermissionResponse{}, ctx.Err()
	}

	select {
	case response := <-responseCh:
		return response, nil
	case <-ctx.Done():
		b.removePending(request.ID, responseCh)
		return PermissionResponse{}, ctx.Err()
	}
}

// IsPending 判断请求是否仍可被响应，宿主可跳过已被 Hook 抢先处理的队列项。
func (b *PermissionBroker) IsPending(requestID string) bool {
	if b == nil || requestID == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.pending[requestID]
	return ok
}

// Resolve 原子认领待审批请求；同一个 request id 只有第一次回应可以生效。
func (b *PermissionBroker) Resolve(response PermissionResponse) bool {
	if b == nil || response.RequestID == "" || (response.Behavior != PermissionAllow && response.Behavior != PermissionDeny) {
		return false
	}
	b.mu.Lock()
	responseCh, ok := b.pending[response.RequestID]
	if ok {
		delete(b.pending, response.RequestID)
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	responseCh <- response
	return true
}

// removePending 只删除当前调用注册的通道，避免误删复用 id 的后续请求。
func (b *PermissionBroker) removePending(requestID string, responseCh chan PermissionResponse) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if current, ok := b.pending[requestID]; ok && current == responseCh {
		delete(b.pending, requestID)
	}
}
