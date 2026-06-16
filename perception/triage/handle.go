package triage

import (
	"fmt"
	"strings"
)

// TenantHandle 生成带租户前缀的 P3 handle，降低跨租户误取数据的概率。
func TenantHandle(kind string, tenantID string, resourceID string) string {
	kind = sanitizeHandlePart(kind)
	tenantID = sanitizeHandlePart(tenantID)
	resourceID = strings.Trim(strings.ReplaceAll(resourceID, " ", "-"), "/")
	if kind == "" {
		kind = "resource"
	}
	return fmt.Sprintf("%s://%s/%s", kind, tenantID, resourceID)
}

// TenantIDFromHandle 从 kind://tenant/id 格式里解析租户 ID。
func TenantIDFromHandle(handle string) (string, bool) {
	trimmed := strings.TrimSpace(handle)
	_, rest, ok := strings.Cut(trimmed, "://")
	if !ok {
		return "", false
	}
	tenantID, _, ok := strings.Cut(rest, "/")
	if !ok || strings.TrimSpace(tenantID) == "" {
		return "", false
	}
	return tenantID, true
}

// ValidateTenantHandle 确认 handle 属于当前租户，P3 回取工具也复用这条规则。
func ValidateTenantHandle(expectedTenantID string, handle string) error {
	tenantID, ok := TenantIDFromHandle(handle)
	if !ok {
		return fmt.Errorf("handle %q must use kind://tenant/resource format", handle)
	}
	if tenantID != expectedTenantID {
		return fmt.Errorf("handle %q belongs to tenant %q, want %q", handle, tenantID, expectedTenantID)
	}
	return nil
}

// sanitizeHandlePart 只做轻量归一化，真实系统里应使用稳定 ID 而不是展示名。
func sanitizeHandlePart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, " ", "-")
	value = strings.Trim(value, "/")
	return value
}
