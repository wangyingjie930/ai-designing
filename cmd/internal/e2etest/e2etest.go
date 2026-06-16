package e2etest

import (
	"os"
	"path/filepath"
	"strings"
)

const EnvName = "CMD_E2E"

// Enabled 判断是否显式打开 cmd 端到端测试，避免普通 go test 误触发真实模型调用。
func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvName))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// ResolvePath 将测试里的相对路径锚定到模块根目录，保持 GoLand 和命令行测试路径一致。
func ResolvePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	root := moduleRoot()
	if root == "" {
		return path
	}
	return filepath.Join(root, path)
}

// ResolvePaths 批量处理端到端测试输入文件，避免报告类测试在 cmd 包目录下找错 fixture。
func ResolvePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, ResolvePath(path))
	}
	return out
}

// moduleRoot 从当前测试工作目录向上寻找 go.mod，作为 demo 资源和 .env 的稳定基准目录。
func moduleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			return ""
		}
		dir = next
	}
}
