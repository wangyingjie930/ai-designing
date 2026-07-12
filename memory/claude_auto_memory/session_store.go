package claudeautomemory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	sessionMemoryDirectoryName = "session-memory"
	sessionSummaryFileName     = "summary.md"
	sessionStateFileName       = "state.json"
)

// SessionStore 管理单个 Session 的结构化摘要和 UUID 边界状态。
type SessionStore struct {
	root        string
	directory   string
	summaryPath string
	statePath   string
	mu          sync.Mutex
}

// NewSessionStore 初始化受控 Session 目录、固定摘要模板和空状态。
func NewSessionStore(root, sessionID string) (*SessionStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("session memory root is required")
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve session memory root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create session memory root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve session memory root symlinks: %w", err)
	}
	directory := filepath.Join(resolved, "sessions", sessionID, sessionMemoryDirectoryName)
	if err := ensureNoSymlinkPath(resolved, directory); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create session memory directory: %w", err)
	}
	store := &SessionStore{
		root: resolved, directory: directory,
		summaryPath: filepath.Join(directory, sessionSummaryFileName),
		statePath:   filepath.Join(directory, sessionStateFileName),
	}
	if err := store.initialize(); err != nil {
		return nil, err
	}
	return store, nil
}

// SummaryPath 返回当前会话摘要路径，供安全诊断而非模型任意选择。
func (s *SessionStore) SummaryPath() string {
	if s == nil {
		return ""
	}
	return s.summaryPath
}

// Load 原子地读取当前进程可见的 Summary 和 State 快照。
func (s *SessionStore) Load(ctx context.Context) (SessionSnapshot, error) {
	if s == nil {
		return SessionSnapshot{}, errors.New("session store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked(ctx)
}

// Commit 校验完整模板后写摘要，再写边界状态；状态失败时旧边界仍可安全重试。
func (s *SessionStore) Commit(ctx context.Context, summary string, state SessionState) error {
	if s == nil {
		return errors.New("session store is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	summary = normalizeSessionSummary(summary)
	if err := validateSessionSummary(summary); err != nil {
		return err
	}
	if state.TokensAtLastUpdate < 0 {
		return errors.New("session token watermark cannot be negative")
	}
	stateData, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session state: %w", err)
	}
	stateData = append(stateData, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := atomicWrite(s.summaryPath, []byte(summary), 0o600); err != nil {
		return fmt.Errorf("write session summary: %w", err)
	}
	if err := atomicWrite(s.statePath, stateData, 0o600); err != nil {
		return fmt.Errorf("write session state: %w", err)
	}
	return nil
}

// IsEmptySummary 判断摘要是否仍是未写入真实会话信息的初始模板。
func (s *SessionStore) IsEmptySummary(summary string) bool {
	return normalizeSessionSummary(summary) == defaultSessionMemoryTemplate
}

// initialize 只在文件不存在时创建模板和状态，避免启动覆盖 Resume 数据。
func (s *SessionStore) initialize() error {
	for _, path := range []string{s.summaryPath, s.statePath} {
		if err := ensureNoSymlinkPath(s.root, path); err != nil {
			return err
		}
	}
	if _, err := os.Stat(s.summaryPath); os.IsNotExist(err) {
		if err := atomicWrite(s.summaryPath, []byte(defaultSessionMemoryTemplate), 0o600); err != nil {
			return fmt.Errorf("initialize session summary: %w", err)
		}
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(s.statePath); os.IsNotExist(err) {
		data, _ := json.MarshalIndent(SessionState{}, "", "  ")
		if err := atomicWrite(s.statePath, append(data, '\n'), 0o600); err != nil {
			return fmt.Errorf("initialize session state: %w", err)
		}
	} else if err != nil {
		return err
	}
	return nil
}

// loadUnlocked 在调用方持锁时读取并验证两个文件，防止并发 Commit 产生混合快照。
func (s *SessionStore) loadUnlocked(ctx context.Context) (SessionSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return SessionSnapshot{}, err
	}
	summaryData, err := os.ReadFile(s.summaryPath)
	if err != nil {
		return SessionSnapshot{}, fmt.Errorf("read session summary: %w", err)
	}
	summary := normalizeSessionSummary(string(summaryData))
	if err := validateSessionSummary(summary); err != nil {
		return SessionSnapshot{}, err
	}
	stateData, err := os.ReadFile(s.statePath)
	if err != nil {
		return SessionSnapshot{}, fmt.Errorf("read session state: %w", err)
	}
	var state SessionState
	if err := json.Unmarshal(stateData, &state); err != nil {
		return SessionSnapshot{}, fmt.Errorf("decode session state: %w", err)
	}
	if state.TokensAtLastUpdate < 0 {
		return SessionSnapshot{}, errors.New("session token watermark cannot be negative")
	}
	return SessionSnapshot{Summary: summary, State: state}, nil
}

// normalizeSessionSummary 统一换行结尾，避免模型尾部空白造成伪变更。
func normalizeSessionSummary(summary string) string {
	return strings.TrimSpace(summary) + "\n"
}

// validateSessionSummary 验证固定一级标题各出现一次且顺序不变。
func validateSessionSummary(summary string) error {
	lastIndex := -1
	for _, header := range sessionMemoryHeaders {
		if strings.Count(summary, header+"\n") != 1 {
			return fmt.Errorf("session summary must contain exactly one %q section", header)
		}
		index := strings.Index(summary, header+"\n")
		if index <= lastIndex {
			return errors.New("session summary sections are out of order")
		}
		lastIndex = index
	}
	return nil
}
