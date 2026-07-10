package claudeautomemory

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// topicMetadata 是主题 Markdown frontmatter 中稳定、可机器解析的元数据。
type topicMetadata struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Type        MemoryType `json:"type"`
}

// encodeTopic 把记忆编码为 JSON frontmatter 加可读 Markdown 正文。
func encodeTopic(record MemoryRecord) ([]byte, error) {
	metadata, err := json.Marshal(topicMetadata{
		Name: record.Ref.Topic, Description: record.Description, Type: record.Type,
	})
	if err != nil {
		return nil, fmt.Errorf("encode topic metadata: %w", err)
	}
	return []byte("---\n" + string(metadata) + "\n---\n\n" + strings.TrimSpace(record.Content) + "\n"), nil
}

// decodeTopic 从主题 Markdown 还原完整记忆并校验 frontmatter。
func decodeTopic(data []byte, scope Scope, path string) (MemoryRecord, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return MemoryRecord{}, errors.New("memory topic is missing frontmatter")
	}
	rest := strings.TrimPrefix(text, "---\n")
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return MemoryRecord{}, errors.New("memory topic frontmatter is not closed")
	}
	var metadata topicMetadata
	if err := json.Unmarshal([]byte(rest[:end]), &metadata); err != nil {
		return MemoryRecord{}, fmt.Errorf("decode topic metadata: %w", err)
	}
	if strings.TrimSpace(metadata.Name) == "" || strings.TrimSpace(metadata.Description) == "" || !metadata.Type.Valid() {
		return MemoryRecord{}, errors.New("memory topic metadata is invalid")
	}
	content := strings.TrimSpace(rest[end+len("\n---\n"):])
	if content == "" {
		return MemoryRecord{}, errors.New("memory topic content is empty")
	}
	return MemoryRecord{
		Ref: MemoryRef{Scope: scope, Topic: metadata.Name}, Type: metadata.Type,
		Description: metadata.Description, Content: content, Path: path,
	}, nil
}

// renderIndex 把同一作用域的主题摘要稳定渲染为 MEMORY.md。
func renderIndex(entries []IndexEntry) []byte {
	entries = append([]IndexEntry(nil), entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Ref.Topic < entries[j].Ref.Topic })
	var builder strings.Builder
	builder.WriteString("# Memory Index\n\n")
	for _, entry := range entries {
		description := strings.Join(strings.Fields(entry.Description), " ")
		description = strings.ReplaceAll(description, "|", "/")
		fmt.Fprintf(&builder, "- [%s](%s) | %s | %s\n", entry.Ref.Topic, entry.Path, entry.Type, description)
	}
	return []byte(builder.String())
}

// parseIndex 解析本模块生成的 MEMORY.md，拒绝手工注入的非法类型和路径。
func parseIndex(data []byte, scope Scope) ([]IndexEntry, error) {
	var entries []IndexEntry
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "- [") {
			continue
		}
		labelEnd := strings.Index(line, "](")
		pathEnd := strings.Index(line, ") | ")
		if labelEnd < 3 || pathEnd <= labelEnd+2 {
			return nil, fmt.Errorf("invalid memory index line: %s", line)
		}
		topic := line[3:labelEnd]
		path := line[labelEnd+2 : pathEnd]
		rest := strings.SplitN(line[pathEnd+4:], " | ", 2)
		if len(rest) != 2 {
			return nil, fmt.Errorf("invalid memory index fields: %s", line)
		}
		memoryType := MemoryType(rest[0])
		if !memoryType.Valid() || filepath.Base(path) != path || path != topic+".md" {
			return nil, fmt.Errorf("unsafe memory index entry: %s", line)
		}
		entries = append(entries, IndexEntry{
			Ref: MemoryRef{Scope: scope, Topic: topic}, Type: memoryType,
			Description: rest[1], Path: path,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}
