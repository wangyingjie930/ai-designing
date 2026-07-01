package skillmode

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

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

// TestRegistryBackendHidesDeprecatedSkillByHealthEvents 验证 registry 会用调用结果直接隔离腐化 Skill。
func TestRegistryBackendHidesDeprecatedSkillByHealthEvents(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	store := NewMemorySkillHealthStore()
	backend, err := NewRegistryBackend(registryTestManifest("2026-07-01.1"), RegistryBackendOptions{
		Channel:     "prod",
		HealthStore: store,
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewRegistryBackend() error = %v", err)
	}
	recordRegistryOutcomes(t, ctx, backend, now, []SkillOutcome{
		SkillOutcomeSuccess,
		SkillOutcomeToolError,
		SkillOutcomeAPIContractError,
		SkillOutcomeLowQuality,
		SkillOutcomeToolError,
	})

	frontMatters, err := backend.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(frontMatters) != 0 {
		t.Fatalf("front matters = %+v, want deprecated skill hidden", frontMatters)
	}

	_, err = backend.Get(ctx, "compliance_review_isolated")
	if err == nil {
		t.Fatal("Get() error = nil, want deprecated error")
	}
	if !strings.Contains(err.Error(), "deprecated") {
		t.Fatalf("error = %v", err)
	}

	resolved, err := backend.Resolve(ctx, "compliance_review_isolated")
	if err != nil {
		t.Fatalf("Resolve() should remain available for audit, error = %v", err)
	}
	if resolved.Artifact.Version != "2026-07-01.1" {
		t.Fatalf("resolved version = %q", resolved.Artifact.Version)
	}
}

// TestRegistryBackendIgnoresNotApplicableInSuccessRate 验证不适用不进入失败分母，避免误伤场景型 Skill。
func TestRegistryBackendIgnoresNotApplicableInSuccessRate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	store := NewMemorySkillHealthStore()
	backend, err := NewRegistryBackend(registryTestManifest("2026-07-01.1"), RegistryBackendOptions{
		Channel:     "prod",
		HealthStore: store,
		HealthEvaluator: SkillHealthEvaluator{
			Window:          24 * time.Hour,
			MinimumSamples:  3,
			WatchBelow:      0.90,
			DegradedBelow:   0.60,
			DeprecatedBelow: 0.30,
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewRegistryBackend() error = %v", err)
	}
	recordRegistryOutcomes(t, ctx, backend, now, []SkillOutcome{
		SkillOutcomeSuccess,
		SkillOutcomeSuccess,
		SkillOutcomeNotApplicable,
		SkillOutcomeNotApplicable,
		SkillOutcomeToolError,
	})

	status, metrics, err := backend.Health(ctx, "compliance_review_isolated")
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if status != SkillHealthStatusWatch {
		t.Fatalf("status = %q, want %q", status, SkillHealthStatusWatch)
	}
	if metrics.EvaluableSamples != 3 {
		t.Fatalf("evaluable samples = %d, want 3", metrics.EvaluableSamples)
	}
	if metrics.SuccessRate != 2.0/3.0 {
		t.Fatalf("success rate = %.2f, want %.2f", metrics.SuccessRate, 2.0/3.0)
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

// recordRegistryOutcomes 通过 registry 记录当前 alias 版本的调用结果。
func recordRegistryOutcomes(t *testing.T, ctx context.Context, backend *RegistryBackend, now time.Time, outcomes []SkillOutcome) {
	t.Helper()
	for i, outcome := range outcomes {
		err := backend.RecordOutcome(ctx, SkillHealthEvent{
			SkillName: "compliance_review_isolated",
			Outcome:   outcome,
			At:        now.Add(time.Duration(-i) * time.Minute),
		})
		if err != nil {
			t.Fatalf("RecordOutcome(%d) error = %v", i, err)
		}
	}
}
