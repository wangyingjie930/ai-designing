package triage

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// ResourceDocument 表示一个 P3 handle 背后的可按需加载资料。
type ResourceDocument struct {
	TenantID string            `json:"tenant_id"`
	Handle   string            `json:"handle"`
	Kind     string            `json:"kind,omitempty"`
	Title    string            `json:"title,omitempty"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ReadTenantHandleRequest 是 ADK 工具读取 P3 handle 的入参。
type ReadTenantHandleRequest struct {
	TenantID string `json:"tenant_id" jsonschema:"description=Current session tenant_id; must match the handle tenant"`
	Handle   string `json:"handle" jsonschema:"description=P3 handle in kind://tenant/resource format"`
}

// ResourceStore 定义 P3 handle 的租户隔离读取接口。
type ResourceStore interface {
	ReadTenantHandle(ctx context.Context, request ReadTenantHandleRequest) (*ResourceDocument, error)
}

// InMemoryResourceStore 是 demo 和测试使用的租户隔离资料库。
type InMemoryResourceStore struct {
	mu   sync.RWMutex
	docs map[string]ResourceDocument
}

// NewInMemoryResourceStore 创建进程内 P3 handle store。
func NewInMemoryResourceStore(docs ...ResourceDocument) *InMemoryResourceStore {
	store := &InMemoryResourceStore{docs: map[string]ResourceDocument{}}
	for _, doc := range docs {
		_ = store.Add(doc)
	}
	return store
}

// Add 写入一个 P3 资料文档，并校验 handle 归属租户。
func (s *InMemoryResourceStore) Add(doc ResourceDocument) error {
	doc.TenantID = strings.TrimSpace(doc.TenantID)
	doc.Handle = strings.TrimSpace(doc.Handle)
	if doc.TenantID == "" {
		return fmt.Errorf("resource tenant_id is required")
	}
	if doc.Handle == "" {
		return fmt.Errorf("resource handle is required")
	}
	if err := ValidateTenantHandle(doc.TenantID, doc.Handle); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[doc.Handle] = doc
	return nil
}

// ReadTenantHandle 读取 P3 资料前先做租户校验，避免把安全边界交给模型判断。
func (s *InMemoryResourceStore) ReadTenantHandle(_ context.Context, request ReadTenantHandleRequest) (*ResourceDocument, error) {
	tenantID := strings.TrimSpace(request.TenantID)
	handle := strings.TrimSpace(request.Handle)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}
	if err := ValidateTenantHandle(tenantID, handle); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	doc, ok := s.docs[handle]
	if !ok {
		return nil, fmt.Errorf("resource handle %q not found", handle)
	}
	if doc.TenantID != tenantID {
		return nil, fmt.Errorf("resource handle %q belongs to tenant %q, want %q", handle, doc.TenantID, tenantID)
	}
	return &doc, nil
}
