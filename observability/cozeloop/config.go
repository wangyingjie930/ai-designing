package cozeloop

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	loopcallback "github.com/cloudwego/eino-ext/callbacks/cozeloop"
	"github.com/cloudwego/eino/callbacks"
	loop "github.com/coze-dev/cozeloop-go"
)

const enabledEnvKey = "COZELOOP_ENABLED"

// Config 描述扣子罗盘接入配置，所有值都来自 .env 加载后的环境变量。
type Config struct {
	Enabled             bool
	WorkspaceID         string
	APIToken            string
	APIBaseURL          string
	JWTOAuthClientID    string
	JWTOAuthPrivateKey  string
	JWTOAuthPublicKeyID string
	Timeout             time.Duration
	UploadTimeout       time.Duration
}

// ShutdownFunc 关闭扣子罗盘客户端，并在进程退出前 flush 剩余 trace。
type ShutdownFunc func(ctx context.Context) error

// ConfigFromEnv 从环境变量读取扣子罗盘配置；未配置 workspace/auth 时默认禁用。
func ConfigFromEnv() Config {
	config := Config{
		WorkspaceID:         firstEnv(loop.EnvWorkspaceID),
		APIToken:            firstEnv(loop.EnvApiToken),
		APIBaseURL:          firstEnv(loop.EnvApiBaseURL),
		JWTOAuthClientID:    firstEnv(loop.EnvJwtOAuthClientID),
		JWTOAuthPrivateKey:  firstEnv(loop.EnvJwtOAuthPrivateKey),
		JWTOAuthPublicKeyID: firstEnv(loop.EnvJwtOAuthPublicKeyID),
		Timeout:             parseDuration(firstEnv("COZELOOP_TIMEOUT")),
		UploadTimeout:       parseDuration(firstEnv("COZELOOP_UPLOAD_TIMEOUT")),
	}
	config.Enabled = parseBool(firstEnv(enabledEnvKey), config.ready())
	return config
}

// InstallFromEnv 使用当前进程环境安装 Eino 官方 CozeLoop callback。
func InstallFromEnv(ctx context.Context) (Config, ShutdownFunc, error) {
	config := ConfigFromEnv()
	shutdown, err := Install(ctx, config)
	if err != nil {
		return config, nil, err
	}
	return config, shutdown, nil
}

// Install 初始化扣子罗盘客户端，并把官方 LoopHandler 注册为 Eino 全局 callback。
func Install(ctx context.Context, config Config) (ShutdownFunc, error) {
	if !config.Enabled {
		return func(context.Context) error { return nil }, nil
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	client, err := loop.NewClient(clientOptions(config)...)
	if err != nil {
		return nil, err
	}
	callbacks.AppendGlobalHandlers(loopcallback.NewLoopHandler(client))
	return func(ctx context.Context) error {
		client.Close(ctx)
		return nil
	}, nil
}

// Validate 校验启用扣子罗盘时必须存在 workspace 和认证信息。
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.WorkspaceID) == "" {
		return fmt.Errorf("%s is required when CozeLoop is enabled", loop.EnvWorkspaceID)
	}
	if !c.hasAuth() {
		return fmt.Errorf("%s or %s/%s/%s is required when CozeLoop is enabled",
			loop.EnvApiToken,
			loop.EnvJwtOAuthClientID,
			loop.EnvJwtOAuthPrivateKey,
			loop.EnvJwtOAuthPublicKeyID,
		)
	}
	if c.APIBaseURL != "" {
		if _, err := url.ParseRequestURI(c.APIBaseURL); err != nil {
			return fmt.Errorf("invalid %s: %w", loop.EnvApiBaseURL, err)
		}
	}
	return nil
}

// Endpoint 返回本次扣子罗盘上报使用的 API base URL。
func (c Config) Endpoint() string {
	if c.APIBaseURL != "" {
		return c.APIBaseURL
	}
	return loop.CnBaseURL
}

// DisplayEndpoint 在命令行输出中隐藏未启用状态的具体端点噪音。
func DisplayEndpoint(config Config) string {
	if !config.Enabled {
		return "disabled"
	}
	return config.Endpoint()
}

// DisplayWorkspaceID 在命令行输出中标明 workspace 是否已配置。
func DisplayWorkspaceID(config Config) string {
	if !config.Enabled {
		return "disabled"
	}
	if config.WorkspaceID == "" {
		return "missing"
	}
	return config.WorkspaceID
}

// ready 判断默认开关是否应该自动启用。
func (c Config) ready() bool {
	return strings.TrimSpace(c.WorkspaceID) != "" && c.hasAuth()
}

// hasAuth 判断当前配置是否有 API token 或完整 JWT OAuth 认证。
func (c Config) hasAuth() bool {
	if strings.TrimSpace(c.APIToken) != "" {
		return true
	}
	return strings.TrimSpace(c.JWTOAuthClientID) != "" &&
		strings.TrimSpace(c.JWTOAuthPrivateKey) != "" &&
		strings.TrimSpace(c.JWTOAuthPublicKeyID) != ""
}

// clientOptions 把本地配置转换为官方 cozeloop-go SDK 选项。
func clientOptions(config Config) []loop.Option {
	opts := []loop.Option{
		loop.WithWorkspaceID(config.WorkspaceID),
	}
	if config.APIToken != "" {
		opts = append(opts, loop.WithAPIToken(config.APIToken))
	}
	if config.APIBaseURL != "" {
		opts = append(opts, loop.WithAPIBaseURL(config.APIBaseURL))
	}
	if config.JWTOAuthClientID != "" {
		opts = append(opts, loop.WithJWTOAuthClientID(config.JWTOAuthClientID))
	}
	if config.JWTOAuthPrivateKey != "" {
		opts = append(opts, loop.WithJWTOAuthPrivateKey(config.JWTOAuthPrivateKey))
	}
	if config.JWTOAuthPublicKeyID != "" {
		opts = append(opts, loop.WithJWTOAuthPublicKeyID(config.JWTOAuthPublicKeyID))
	}
	if config.Timeout > 0 {
		opts = append(opts, loop.WithTimeout(config.Timeout))
	}
	if config.UploadTimeout > 0 {
		opts = append(opts, loop.WithUploadTimeout(config.UploadTimeout))
	}
	return opts
}

// firstEnv 返回第一项非空环境变量。
func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

// parseBool 解析布尔环境变量，空值时使用 fallback。
func parseBool(value string, fallback bool) bool {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

// parseDuration 解析 Go duration 或毫秒数环境变量，空值表示沿用 SDK 默认值。
func parseDuration(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if d, err := time.ParseDuration(value); err == nil {
		return d
	}
	ms, err := strconv.Atoi(value)
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}
