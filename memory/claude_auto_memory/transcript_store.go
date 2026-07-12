package claudeautomemory

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"
)

const transcriptFileName = "transcript.jsonl"

var safeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// TranscriptStore 管理一个 Session 的完整只追加事实日志，Compact 不会改写它。
type TranscriptStore struct {
	root string
	path string
	mu   sync.Mutex
}

// NewTranscriptStore 在记忆根目录下创建受控 Session 目录和 Transcript 文件位置。
func NewTranscriptStore(root, sessionID string) (*TranscriptStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("transcript root is required")
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve transcript root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create transcript root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve transcript root symlinks: %w", err)
	}
	sessionDirectory := filepath.Join(resolved, "sessions", sessionID)
	if err := ensureNoSymlinkPath(resolved, sessionDirectory); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(sessionDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("create transcript session directory: %w", err)
	}
	path := filepath.Join(sessionDirectory, transcriptFileName)
	if err := ensureNoSymlinkPath(resolved, path); err != nil {
		return nil, err
	}
	return &TranscriptStore{root: resolved, path: path}, nil
}

// Path 返回 Transcript 的绝对路径，供 CLI 低敏诊断和测试使用。
func (s *TranscriptStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Append 校验并追加一条真实消息；失败时不会留下半行 JSON。
func (s *TranscriptStore) Append(ctx context.Context, message ConversationMessage) error {
	if s == nil {
		return errors.New("transcript store is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTranscriptMessage(message); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.loadUnlocked(ctx)
	if err != nil {
		return err
	}
	for _, item := range existing {
		if item.ID == message.ID {
			return fmt.Errorf("duplicate transcript message ID %q", message.ID)
		}
	}
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode transcript message: %w", err)
	}
	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open transcript: %w", err)
	}
	if _, err = file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return fmt.Errorf("append transcript: %w", err)
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync transcript: %w", err)
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("close transcript: %w", err)
	}
	return nil
}

// Load 按追加顺序恢复完整真实会话，不把派生 Context 消息混入事实日志。
func (s *TranscriptStore) Load(ctx context.Context) ([]ConversationMessage, error) {
	if s == nil {
		return nil, errors.New("transcript store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked(ctx)
}

// loadUnlocked 在调用方持锁时解析 JSONL，避免 Append 的重复检查与并发读取交错。
func (s *TranscriptStore) loadUnlocked(ctx context.Context) ([]ConversationMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()
	var messages []ConversationMessage
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var message ConversationMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return nil, fmt.Errorf("decode transcript line %d: %w", len(messages)+1, err)
		}
		if err := validateTranscriptMessage(message); err != nil {
			return nil, fmt.Errorf("validate transcript line %d: %w", len(messages)+1, err)
		}
		messages = append(messages, message)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}
	return messages, nil
}

// validateSessionID 把用户可见的会话标识限制为单个安全路径片段。
func validateSessionID(sessionID string) error {
	if !safeSessionIDPattern.MatchString(sessionID) || sessionID == "." || sessionID == ".." {
		return errors.New("invalid session ID")
	}
	return nil
}

// validateTranscriptMessage 保证 Transcript 只包含稳定 UUID 标识的真实用户/助手消息。
func validateTranscriptMessage(message ConversationMessage) error {
	if _, err := uuid.Parse(message.ID); err != nil {
		return errors.New("valid conversation message ID is required")
	}
	if !message.Role.Valid() {
		return fmt.Errorf("invalid conversation role %q", message.Role)
	}
	if message.Kind != MessageKindNormal {
		return errors.New("only normal messages can be persisted to transcript")
	}
	if strings.TrimSpace(message.Content) == "" {
		return errors.New("conversation message content is required")
	}
	if message.ToolCallCount < 0 {
		return errors.New("tool call count cannot be negative")
	}
	return nil
}
