package claudeautomemory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeMemorySelector 返回预设安全引用，并记录收到的双索引。
type fakeMemorySelector struct {
	refs     []MemoryRef
	err      error
	query    string
	manifest MemoryManifest
}

// Select 记录查询和候选清单，避免召回测试依赖真实模型。
func (f *fakeMemorySelector) Select(_ context.Context, query string, manifest MemoryManifest) ([]MemoryRef, error) {
	f.query = query
	f.manifest = manifest
	if f.err != nil {
		return nil, f.err
	}
	return append([]MemoryRef(nil), f.refs...), nil
}

// TestRecallerReadsOnlyFiveUniqueManifestEntries 验证召回只读取前五个唯一合法引用。
func TestRecallerReadsOnlyFiveUniqueManifestEntries(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	refs := seedMemories(t, store, 6)
	selector := &fakeMemorySelector{refs: append(append([]MemoryRef(nil), refs...), refs[0])}
	recaller, err := NewRecaller(store, selector)
	if err != nil {
		t.Fatal(err)
	}
	result := recaller.Recall(context.Background(), "新增工具要注意什么")
	if len(result.Records) != maxRecalledMemories {
		t.Fatalf("records = %d, result = %+v", len(result.Records), result)
	}
	if !strings.Contains(result.Context, "source=") || !strings.Contains(result.Context, "scope=") {
		t.Fatalf("context = %s", result.Context)
	}
	if len(selector.manifest.Private)+len(selector.manifest.Team) != 6 {
		t.Fatalf("manifest = %+v", selector.manifest)
	}
}

// TestRecallerRejectsUnknownReferenceButKeepsValidOnes 验证模型构造的未索引引用只产生警告。
func TestRecallerRejectsUnknownReferenceButKeepsValidOnes(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	valid := seedMemories(t, store, 1)[0]
	selector := &fakeMemorySelector{refs: []MemoryRef{{Scope: ScopeTeam, Topic: "missing"}, valid}}
	recaller, err := NewRecaller(store, selector)
	if err != nil {
		t.Fatal(err)
	}
	result := recaller.Recall(context.Background(), "问题")
	if len(result.Records) != 1 || len(result.Warnings) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

// TestRecallerSelectorFailureReturnsEmptyContext 验证召回模型失败不会阻断主 Agent。
func TestRecallerSelectorFailureReturnsEmptyContext(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recaller, err := NewRecaller(store, &fakeMemorySelector{err: errors.New("selector unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	result := recaller.Recall(context.Background(), "问题")
	if result.Context != "" || len(result.Warnings) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

// seedMemories 写入交替作用域的测试主题并返回其安全引用。
func seedMemories(t *testing.T, store *Store, count int) []MemoryRef {
	t.Helper()
	refs := make([]MemoryRef, 0, count)
	for index := 0; index < count; index++ {
		scope := ScopePrivate
		memoryType := MemoryTypeFeedback
		if index%2 == 1 {
			scope = ScopeTeam
			memoryType = MemoryTypeProject
		}
		record, err := store.Upsert(context.Background(), MemoryCandidate{
			Type: memoryType, Scope: scope, Topic: fmt.Sprintf("topic-%d", index),
			Description: fmt.Sprintf("主题 %d", index), Content: fmt.Sprintf("记忆正文 %d", index),
		})
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, record.Ref)
	}
	return refs
}
