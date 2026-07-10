package claudeautomemory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

var (
	bearerSecretPattern   = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]{8,}`)
	assignedSecretPattern = regexp.MustCompile(`(?im)(api[_-]?key|token|secret|password|cookie)\s*[:=]\s*["']?[^\s"']{6,}`)
)

// validateCandidate 校验模型语义选择和必填内容，保留模型决策但守住硬边界。
func validateCandidate(candidate MemoryCandidate) error {
	if !candidate.Type.Valid() {
		return fmt.Errorf("invalid memory type %q", candidate.Type)
	}
	if !candidate.Scope.Valid() {
		return fmt.Errorf("invalid memory scope %q", candidate.Scope)
	}
	if candidate.Type == MemoryTypeUser && candidate.Scope != ScopePrivate {
		return errors.New("user memory must remain private")
	}
	if strings.TrimSpace(candidate.Topic) == "" || strings.TrimSpace(candidate.Description) == "" || strings.TrimSpace(candidate.Content) == "" {
		return errors.New("topic, description and content are required")
	}
	if candidate.Scope == ScopeTeam && containsSecret(candidate.Description+"\n"+candidate.Content) {
		return errors.New("team memory contains a possible secret")
	}
	return nil
}

// slugifyTopic 把模型提供的语义主题收敛为安全且支持中文的文件名。
func slugifyTopic(topic string) (string, error) {
	raw := strings.TrimSpace(topic)
	if raw == "" || filepath.IsAbs(raw) || strings.Contains(raw, "..") {
		return "", errors.New("invalid memory topic")
	}
	var builder strings.Builder
	lastHyphen := false
	for _, current := range strings.ToLower(raw) {
		if unicode.IsLetter(current) || unicode.IsNumber(current) || current == '_' {
			builder.WriteRune(current)
			lastHyphen = false
			continue
		}
		if !lastHyphen && builder.Len() > 0 {
			builder.WriteByte('-')
			lastHyphen = true
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" || strings.EqualFold(slug, strings.TrimSuffix(memoryIndexName, ".md")) {
		return "", errors.New("memory topic normalizes to a reserved name")
	}
	return slug, nil
}

// containsSecret 用保守规则阻止凭据进入团队共享目录。
func containsSecret(content string) bool {
	lower := strings.ToLower(content)
	if strings.Contains(lower, "-----begin") && strings.Contains(lower, "private key-----") {
		return true
	}
	return bearerSecretPattern.MatchString(content) || assignedSecretPattern.MatchString(content)
}

// ensureContainedPath 验证目标路径没有逃出配置的记忆根目录。
func ensureContainedPath(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("memory path escapes root")
	}
	return nil
}

// ensureNoSymlinkPath 拒绝从根目录到目标文件之间已有的符号链接。
func ensureNoSymlinkPath(root, target string) error {
	if err := ensureContainedPath(root, target); err != nil {
		return err
	}
	relative, _ := filepath.Rel(root, target)
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("memory path contains symlink: %s", current)
		}
	}
	return nil
}
