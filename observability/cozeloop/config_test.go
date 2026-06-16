package cozeloop

import (
	"testing"
	"time"

	loop "github.com/coze-dev/cozeloop-go"
)

// TestConfigFromEnvDisabledWithoutCredentials 验证未配置罗盘认证时默认保持 no-op。
func TestConfigFromEnvDisabledWithoutCredentials(t *testing.T) {
	t.Setenv(loop.EnvWorkspaceID, "")
	t.Setenv(loop.EnvApiToken, "")
	t.Setenv(enabledEnvKey, "")

	config := ConfigFromEnv()
	if config.Enabled {
		t.Fatal("expected CozeLoop config to be disabled")
	}
	if got, want := DisplayEndpoint(config), "disabled"; got != want {
		t.Fatalf("DisplayEndpoint() = %q, want %q", got, want)
	}
}

// TestConfigFromEnvEnabledWithToken 验证 .env 中 workspace/token 会自动启用罗盘。
func TestConfigFromEnvEnabledWithToken(t *testing.T) {
	t.Setenv(loop.EnvWorkspaceID, "workspace-1")
	t.Setenv(loop.EnvApiToken, "pat-test")
	t.Setenv(loop.EnvApiBaseURL, "https://api.coze.cn")
	t.Setenv("COZELOOP_TIMEOUT", "1500ms")
	t.Setenv("COZELOOP_UPLOAD_TIMEOUT", "3000")
	t.Setenv(enabledEnvKey, "")

	config := ConfigFromEnv()
	if !config.Enabled {
		t.Fatal("expected CozeLoop config to be enabled")
	}
	if got, want := config.Endpoint(), "https://api.coze.cn"; got != want {
		t.Fatalf("Endpoint() = %q, want %q", got, want)
	}
	if got, want := config.Timeout, 1500*time.Millisecond; got != want {
		t.Fatalf("Timeout = %s, want %s", got, want)
	}
	if got, want := config.UploadTimeout, 3*time.Second; got != want {
		t.Fatalf("UploadTimeout = %s, want %s", got, want)
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// TestValidateRequiresAuthWhenForced 验证强制启用时会暴露缺失的罗盘认证配置。
func TestValidateRequiresAuthWhenForced(t *testing.T) {
	config := Config{Enabled: true, WorkspaceID: "workspace-1"}
	if err := config.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}
