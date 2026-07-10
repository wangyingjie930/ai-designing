package claudeautomemory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	memoryIndexName   = "MEMORY.md"
	teamDirectoryName = "team"
)

// Store 管理 private/team Markdown 主题和各自的摘要索引。
type Store struct {
	root string
}

// NewStore 创建并解析真实根目录，同时初始化两个作用域的索引。
func NewStore(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("memory root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve memory root: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(absolute, teamDirectoryName), 0o700); err != nil {
		return nil, fmt.Errorf("create memory directories: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve memory root symlinks: %w", err)
	}
	store := &Store{root: resolved}
	for _, scope := range []Scope{ScopePrivate, ScopeTeam} {
		if err := store.ensureIndex(scope); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// Root 返回已经解析符号链接的绝对记忆根目录。
func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// Upsert 原子写入一个主题，并重建同作用域 MEMORY.md。
func (s *Store) Upsert(ctx context.Context, candidate MemoryCandidate) (MemoryRecord, error) {
	if s == nil {
		return MemoryRecord{}, errors.New("memory store is required")
	}
	if err := ctx.Err(); err != nil {
		return MemoryRecord{}, err
	}
	if err := validateCandidate(candidate); err != nil {
		return MemoryRecord{}, err
	}
	slug, err := slugifyTopic(candidate.Topic)
	if err != nil {
		return MemoryRecord{}, err
	}
	record := MemoryRecord{
		Ref: MemoryRef{Scope: candidate.Scope, Topic: slug}, Type: candidate.Type,
		Description: strings.TrimSpace(candidate.Description), Content: strings.TrimSpace(candidate.Content),
	}
	record.Path = s.topicPath(record.Ref)
	if err := ensureNoSymlinkPath(s.root, record.Path); err != nil {
		return MemoryRecord{}, err
	}
	data, err := encodeTopic(record)
	if err != nil {
		return MemoryRecord{}, err
	}
	if err := atomicWrite(record.Path, data, 0o600); err != nil {
		return MemoryRecord{}, err
	}
	if err := s.rebuildIndex(candidate.Scope); err != nil {
		return MemoryRecord{}, err
	}
	return record, nil
}

// LoadManifest 从 private/team 的 MEMORY.md 加载模型可选择的完整候选清单。
func (s *Store) LoadManifest(ctx context.Context) (MemoryManifest, error) {
	if s == nil {
		return MemoryManifest{}, errors.New("memory store is required")
	}
	if err := ctx.Err(); err != nil {
		return MemoryManifest{}, err
	}
	privateEntries, err := s.loadIndex(ScopePrivate)
	if err != nil {
		return MemoryManifest{}, err
	}
	teamEntries, err := s.loadIndex(ScopeTeam)
	if err != nil {
		return MemoryManifest{}, err
	}
	return MemoryManifest{Private: privateEntries, Team: teamEntries}, nil
}

// Read 只读取当前 manifest 中存在的引用，阻断模型构造任意文件路径。
func (s *Store) Read(ctx context.Context, ref MemoryRef) (MemoryRecord, error) {
	if err := ctx.Err(); err != nil {
		return MemoryRecord{}, err
	}
	manifest, err := s.LoadManifest(ctx)
	if err != nil {
		return MemoryRecord{}, err
	}
	found := false
	for _, entry := range manifestEntries(manifest) {
		if entry.Ref == ref {
			found = true
			break
		}
	}
	if !found {
		return MemoryRecord{}, fmt.Errorf("memory reference is not indexed: %s/%s", ref.Scope, ref.Topic)
	}
	path := s.topicPath(ref)
	if err := ensureNoSymlinkPath(s.root, path); err != nil {
		return MemoryRecord{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return MemoryRecord{}, err
	}
	record, err := decodeTopic(data, ref.Scope, path)
	if err != nil {
		return MemoryRecord{}, err
	}
	if record.Ref != ref {
		return MemoryRecord{}, errors.New("memory topic metadata does not match index")
	}
	return record, nil
}

// ensureIndex 确保新建目录立即拥有可读的空 MEMORY.md。
func (s *Store) ensureIndex(scope Scope) error {
	path := s.indexPath(scope)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return atomicWrite(path, renderIndex(nil), 0o600)
}

// rebuildIndex 以主题文件为事实源重建一个作用域的稳定索引。
func (s *Store) rebuildIndex(scope Scope) error {
	directory := s.scopeDirectory(scope)
	items, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	entries := make([]IndexEntry, 0, len(items))
	for _, item := range items {
		if item.Name() == memoryIndexName || filepath.Ext(item.Name()) != ".md" {
			continue
		}
		if item.Type()&os.ModeSymlink != 0 || item.IsDir() {
			return fmt.Errorf("unsafe memory topic entry: %s", item.Name())
		}
		path := filepath.Join(directory, item.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		record, decodeErr := decodeTopic(data, scope, path)
		if decodeErr != nil {
			return decodeErr
		}
		if filepath.Base(item.Name()) != record.Ref.Topic+".md" {
			return fmt.Errorf("topic filename does not match metadata: %s", item.Name())
		}
		entries = append(entries, IndexEntry{
			Ref: record.Ref, Type: record.Type, Description: record.Description, Path: item.Name(),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Ref.Topic < entries[j].Ref.Topic })
	return atomicWrite(s.indexPath(scope), renderIndex(entries), 0o600)
}

// loadIndex 读取并解析指定作用域的 MEMORY.md。
func (s *Store) loadIndex(scope Scope) ([]IndexEntry, error) {
	data, err := os.ReadFile(s.indexPath(scope))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseIndex(data, scope)
}

// scopeDirectory 把模型选出的作用域映射到固定、安全的物理目录。
func (s *Store) scopeDirectory(scope Scope) string {
	if scope == ScopeTeam {
		return filepath.Join(s.root, teamDirectoryName)
	}
	return s.root
}

// indexPath 返回指定作用域的 MEMORY.md 路径。
func (s *Store) indexPath(scope Scope) string {
	return filepath.Join(s.scopeDirectory(scope), memoryIndexName)
}

// topicPath 返回安全引用对应的主题文件路径。
func (s *Store) topicPath(ref MemoryRef) string {
	return filepath.Join(s.scopeDirectory(ref.Scope), ref.Topic+".md")
}

// manifestEntries 合并两个作用域的候选，供安全引用校验复用。
func manifestEntries(manifest MemoryManifest) []IndexEntry {
	entries := make([]IndexEntry, 0, len(manifest.Private)+len(manifest.Team))
	entries = append(entries, manifest.Private...)
	entries = append(entries, manifest.Team...)
	return entries
}

// atomicWrite 通过同目录临时文件和 rename 避免暴露半写内容。
func atomicWrite(path string, data []byte, permission os.FileMode) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".memory-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err = temporary.Chmod(permission); err != nil {
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
