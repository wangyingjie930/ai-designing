package skillmode

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk/middlewares/skill"
)

// TestRegistryBackendResolvesAliasToImmutableArtifact 验证线上入口只通过 alias 指向不可变版本。
func TestRegistryBackendResolvesAliasToImmutableArtifact(t *testing.T) {
	ctx := context.Background()
	manifest := registryTestManifest("2026-07-01.1")
	backend, err := NewRegistryBackend(manifest, RegistryBackendOptions{Channel: "prod"})
	if err != nil {
		t.Fatalf("NewRegistryBackend() error = %v", err)
	}

	resolved, err := backend.Resolve(ctx, "compliance_review_isolated")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if resolved.Artifact.Version != "2026-07-01.1" {
		t.Fatalf("version = %q, want 2026-07-01.1", resolved.Artifact.Version)
	}
	if resolved.Skill.BaseDirectory != "registry://compliance_review_isolated/2026-07-01.1" {
		t.Fatalf("base dir = %q", resolved.Skill.BaseDirectory)
	}
	if !strings.Contains(resolved.Skill.Content, "不能承诺成绩提升") {
		t.Fatalf("content = %q", resolved.Skill.Content)
	}
}

// TestRegistryBackendRollbackSwitchesAliasOnly 验证回滚只需要切 alias，不需要改 artifact 内容。
func TestRegistryBackendRollbackSwitchesAliasOnly(t *testing.T) {
	ctx := context.Background()
	forwardManifest := registryTestManifest("2026-07-01.2")
	forwardBackend, err := NewRegistryBackend(forwardManifest, RegistryBackendOptions{Channel: "prod"})
	if err != nil {
		t.Fatalf("NewRegistryBackend(forward) error = %v", err)
	}
	forward, err := forwardBackend.Resolve(ctx, "compliance_review_isolated")
	if err != nil {
		t.Fatalf("Resolve(forward) error = %v", err)
	}
	if forward.Artifact.Version != "2026-07-01.2" {
		t.Fatalf("forward version = %q", forward.Artifact.Version)
	}

	rollbackManifest := registryTestManifest("2026-07-01.1")
	rollbackBackend, err := NewRegistryBackend(rollbackManifest, RegistryBackendOptions{Channel: "prod"})
	if err != nil {
		t.Fatalf("NewRegistryBackend(rollback) error = %v", err)
	}
	rollback, err := rollbackBackend.Resolve(ctx, "compliance_review_isolated")
	if err != nil {
		t.Fatalf("Resolve(rollback) error = %v", err)
	}
	if rollback.Artifact.Version != "2026-07-01.1" {
		t.Fatalf("rollback version = %q", rollback.Artifact.Version)
	}
	if rollback.Artifact.ContentSHA256 != contentHash(registrySkillBody("2026-07-01.1")) {
		t.Fatalf("rollback hash = %q", rollback.Artifact.ContentSHA256)
	}
}

// TestRegistryBackendPrefersTenantAlias 验证租户灰度优先于环境默认 alias。
func TestRegistryBackendPrefersTenantAlias(t *testing.T) {
	ctx := context.Background()
	manifest := registryTestManifest("2026-07-01.1")
	manifest.Aliases = append(manifest.Aliases, SkillAlias{
		SkillName: "compliance_review_isolated",
		Channel:   "prod",
		Tenant:    "tenant_a",
		Version:   "2026-07-01.2",
	})
	backend, err := NewRegistryBackend(manifest, RegistryBackendOptions{Channel: "prod", Tenant: "tenant_a"})
	if err != nil {
		t.Fatalf("NewRegistryBackend() error = %v", err)
	}

	resolved, err := backend.Resolve(ctx, "compliance_review_isolated")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if resolved.Artifact.Version != "2026-07-01.2" {
		t.Fatalf("tenant version = %q, want 2026-07-01.2", resolved.Artifact.Version)
	}
}

// TestRegistryBackendRejectsHashMismatch 验证发布门禁会拒绝内容哈希不一致的 artifact。
func TestRegistryBackendRejectsHashMismatch(t *testing.T) {
	manifest := registryTestManifest("2026-07-01.1")
	manifest.Artifacts[0].ContentSHA256 = "bad-hash"

	_, err := NewRegistryBackend(manifest, RegistryBackendOptions{Channel: "prod"})
	if err == nil {
		t.Fatal("NewRegistryBackend() error = nil, want hash mismatch")
	}
	if !strings.Contains(err.Error(), "content hash mismatch") {
		t.Fatalf("error = %v", err)
	}
}

// registryTestManifest 构造带两个不可变版本和一个可切换 prod alias 的测试清单。
func registryTestManifest(prodVersion string) SkillReleaseManifest {
	versions := []string{"2026-07-01.1", "2026-07-01.2"}
	artifacts := make([]SkillArtifact, 0, len(versions))
	for _, version := range versions {
		content := registrySkillBody(version)
		artifacts = append(artifacts, SkillArtifact{
			FrontMatter: skill.FrontMatter{
				Name:        "compliance_review_isolated",
				Description: "合规风险隔离审查",
				Context:     skill.ContextModeFork,
				Agent:       "compliance_guard_agent",
			},
			Version:       version,
			Owner:         "risk-team",
			SourceSHA:     "git-sha-" + version,
			Content:       content,
			ContentSHA256: contentHash(content),
		})
	}
	return SkillReleaseManifest{
		Artifacts: artifacts,
		Aliases: []SkillAlias{{
			SkillName: "compliance_review_isolated",
			Channel:   "prod",
			Version:   prodVersion,
		}},
	}
}

// registrySkillBody 根据版本生成可区分的测试 Skill 正文。
func registrySkillBody(version string) string {
	return fmt.Sprintf("版本 %s：只基于传入文本审查，不能承诺成绩提升。", version)
}

// contentHash 返回 Skill 正文的稳定 SHA256，用于证明 artifact 内容不可变。
func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", sum)
}
