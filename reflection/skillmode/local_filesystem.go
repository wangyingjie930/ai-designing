package skillmode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk/filesystem"
)

// localSkillFilesystem 是给 Eino Skill Middleware 使用的只读本地目录适配器。
type localSkillFilesystem struct {
	root string
}

// newLocalSkillFilesystem 创建只允许访问 root 目录内文件的 filesystem.Backend。
func newLocalSkillFilesystem(root string) (*localSkillFilesystem, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("skill filesystem root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(absRoot); err != nil {
		return nil, err
	} else if !info.IsDir() {
		return nil, fmt.Errorf("skill filesystem root is not a directory: %s", absRoot)
	}
	return &localSkillFilesystem{root: absRoot}, nil
}

// LsInfo 列出本地目录的直接子项，当前 skill backend 不依赖它，但实现接口便于复用。
func (b *localSkillFilesystem) LsInfo(_ context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	dir, err := b.resolvePath(req.Path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	infos := make([]filesystem.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		infos = append(infos, fileInfo(entry.Name(), info))
	}
	return infos, nil
}

// Read 读取真实 SKILL.md 文件内容。
func (b *localSkillFilesystem) Read(_ context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	path, err := b.resolvePath(req.FilePath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &filesystem.FileContent{Content: selectLineWindow(string(data), req.Offset, req.Limit)}, nil
}

// GrepRaw 当前 Skill backend 不需要内容搜索。
func (b *localSkillFilesystem) GrepRaw(context.Context, *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	return nil, errors.New("grep is not supported by local skill filesystem")
}

// GlobInfo 返回匹配 SKILL.md 的文件列表，供 skill.NewBackendFromFilesystem 扫描。
func (b *localSkillFilesystem) GlobInfo(_ context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	base, err := b.resolvePath(req.Path)
	if err != nil {
		return nil, err
	}
	pattern := filepath.Join(base, filepath.FromSlash(req.Pattern))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	infos := make([]filesystem.FileInfo, 0, len(matches))
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(base, match)
		if err != nil {
			return nil, err
		}
		infos = append(infos, fileInfo(filepath.ToSlash(rel), info))
	}
	return infos, nil
}

// Write 明确禁止运行时改写仓库中的 Skill 文件。
func (b *localSkillFilesystem) Write(context.Context, *filesystem.WriteRequest) error {
	return errors.New("write is not supported by local skill filesystem")
}

// Edit 明确禁止运行时改写仓库中的 Skill 文件。
func (b *localSkillFilesystem) Edit(context.Context, *filesystem.EditRequest) error {
	return errors.New("edit is not supported by local skill filesystem")
}

// resolvePath 把相对路径和绝对路径都限制在 root 内，避免误读仓库外文件。
func (b *localSkillFilesystem) resolvePath(path string) (string, error) {
	if b == nil || strings.TrimSpace(b.root) == "" {
		return "", errors.New("skill filesystem is not initialized")
	}
	candidate := strings.TrimSpace(path)
	if candidate == "" {
		candidate = b.root
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(b.root, candidate)
	}
	candidate = filepath.Clean(candidate)
	rel, err := filepath.Rel(b.root, candidate)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes skill root: %s", path)
	}
	return candidate, nil
}

// fileInfo 转成 Eino filesystem.FileInfo。
func fileInfo(path string, info os.FileInfo) filesystem.FileInfo {
	modifiedAt := ""
	if !info.ModTime().IsZero() {
		modifiedAt = info.ModTime().UTC().Format(time.RFC3339Nano)
	}
	return filesystem.FileInfo{
		Path:       filepath.ToSlash(path),
		IsDir:      info.IsDir(),
		Size:       info.Size(),
		ModifiedAt: modifiedAt,
	}
}

// selectLineWindow 支持 Eino ReadRequest 的行范围语义；Skill 加载默认会读取全文。
func selectLineWindow(content string, offset int, limit int) string {
	if offset <= 1 && limit <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	start := offset - 1
	if start < 0 {
		start = 0
	}
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "\n")
}

var _ filesystem.Backend = (*localSkillFilesystem)(nil)
