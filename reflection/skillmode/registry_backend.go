package skillmode

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk/middlewares/skill"
)

const defaultRegistryChannel = "prod"

// SkillReleaseManifest 描述一次可发布的 Skill 版本清单和线上 alias 指针。
type SkillReleaseManifest struct {
	Artifacts []SkillArtifact
	Aliases   []SkillAlias
}

// SkillArtifact 是不可变 Skill 内容版本；上线后只能新增版本，不能原地改内容。
type SkillArtifact struct {
	FrontMatter   skill.FrontMatter
	Version       string
	Owner         string
	SourceSHA     string
	Content       string
	ContentSHA256 string
}

// SkillAlias 是线上入口到具体不可变版本的指针，回滚只需要切换 Version。
type SkillAlias struct {
	SkillName string
	Channel   string
	Tenant    string
	Version   string
}

// RegistryBackendOptions 控制运行时从哪个环境和租户视角解析 alias。
type RegistryBackendOptions struct {
	Channel         string
	Tenant          string
	HealthStore     SkillHealthStore
	HealthEvaluator SkillHealthEvaluator
	Now             func() time.Time
}

// ResolvedSkill 保留运行时留证需要的 alias、artifact 和 Eino Skill 结果。
type ResolvedSkill struct {
	Skill    skill.Skill
	Alias    SkillAlias
	Artifact SkillArtifact
}

// RegistryBackend 用 release manifest 实现 Eino skill.Backend，隔离发布治理和 middleware 执行。
type RegistryBackend struct {
	channel         string
	tenant          string
	artifacts       map[string]map[string]SkillArtifact
	aliases         map[registryAliasKey]SkillAlias
	healthStore     SkillHealthStore
	healthEvaluator SkillHealthEvaluator
	now             func() time.Time
}

// registryAliasKey 用环境和租户限定 alias 范围，避免不同灰度流量互相覆盖。
type registryAliasKey struct {
	skillName string
	channel   string
	tenant    string
}

// NewRegistryBackend 从发布清单构造 registry-backed Skill backend。
func NewRegistryBackend(manifest SkillReleaseManifest, options RegistryBackendOptions) (*RegistryBackend, error) {
	channel := normalizeRegistryChannel(options.Channel)
	now := options.Now
	if now == nil {
		now = time.Now
	}
	backend := &RegistryBackend{
		channel:         channel,
		tenant:          strings.TrimSpace(options.Tenant),
		artifacts:       map[string]map[string]SkillArtifact{},
		aliases:         map[registryAliasKey]SkillAlias{},
		healthStore:     options.HealthStore,
		healthEvaluator: normalizeSkillHealthEvaluator(options.HealthEvaluator),
		now:             now,
	}
	if err := backend.indexArtifacts(manifest.Artifacts); err != nil {
		return nil, err
	}
	if err := backend.indexAliases(manifest.Aliases); err != nil {
		return nil, err
	}
	return backend, nil
}

// List 返回当前运行视角可见的 active Skill frontmatter。
func (b *RegistryBackend) List(ctx context.Context) ([]skill.FrontMatter, error) {
	names := b.visibleSkillNames()
	matters := make([]skill.FrontMatter, 0, len(names))
	for _, name := range names {
		resolved, err := b.Resolve(ctx, name)
		if err != nil {
			return nil, err
		}
		deprecated, err := b.isResolvedSkillDeprecated(ctx, resolved)
		if err != nil {
			return nil, err
		}
		if deprecated {
			continue
		}
		matters = append(matters, resolved.Skill.FrontMatter)
	}
	return matters, nil
}

// Get 按当前环境和租户 alias 取出实际要执行的不可变 Skill 版本。
func (b *RegistryBackend) Get(ctx context.Context, name string) (skill.Skill, error) {
	resolved, err := b.Resolve(ctx, name)
	if err != nil {
		return skill.Skill{}, err
	}
	status, metrics, err := b.healthForResolved(ctx, resolved)
	if err != nil {
		return skill.Skill{}, err
	}
	if status == SkillHealthStatusDeprecated {
		return skill.Skill{}, fmt.Errorf("skill %s@%s is deprecated: success_rate=%.2f samples=%d", resolved.Alias.SkillName, resolved.Artifact.Version, metrics.SuccessRate, metrics.EvaluableSamples)
	}
	return resolved.Skill, nil
}

// Resolve 返回 Skill 执行结果和版本元数据，供 trace 或审计使用。
func (b *RegistryBackend) Resolve(_ context.Context, name string) (ResolvedSkill, error) {
	if b == nil {
		return ResolvedSkill{}, fmt.Errorf("registry backend is nil")
	}
	skillName := strings.TrimSpace(name)
	if skillName == "" {
		return ResolvedSkill{}, fmt.Errorf("skill name is required")
	}
	alias, ok := b.resolveAlias(skillName)
	if !ok {
		return ResolvedSkill{}, fmt.Errorf("skill alias not found: %s channel=%s tenant=%s", skillName, b.channel, b.tenant)
	}
	artifact, ok := b.lookupArtifact(alias.SkillName, alias.Version)
	if !ok {
		return ResolvedSkill{}, fmt.Errorf("skill artifact not found: %s@%s", alias.SkillName, alias.Version)
	}
	loaded := skill.Skill{
		FrontMatter:   artifact.FrontMatter,
		Content:       artifact.Content,
		BaseDirectory: fmt.Sprintf("registry://%s/%s", artifact.FrontMatter.Name, artifact.Version),
	}
	return ResolvedSkill{
		Skill:    loaded,
		Alias:    alias,
		Artifact: artifact,
	}, nil
}

// RecordOutcome 记录当前 alias/version 的调用结果，供 registry 后续判断 Skill 是否腐化。
func (b *RegistryBackend) RecordOutcome(ctx context.Context, event SkillHealthEvent) error {
	if b == nil {
		return fmt.Errorf("registry backend is nil")
	}
	if b.healthStore == nil {
		return fmt.Errorf("skill health store is not configured")
	}
	resolved, err := b.Resolve(ctx, event.SkillName)
	if err != nil {
		return err
	}
	event.SkillName = resolved.Alias.SkillName
	if strings.TrimSpace(event.Version) == "" {
		event.Version = resolved.Artifact.Version
	}
	if strings.TrimSpace(event.Alias) == "" {
		event.Alias = registryAliasLabel(resolved.Alias)
	}
	return b.healthStore.Record(ctx, event)
}

// Health 返回当前 alias/version 的健康状态和窗口指标。
func (b *RegistryBackend) Health(ctx context.Context, skillName string) (SkillHealthStatus, SkillHealthMetrics, error) {
	resolved, err := b.Resolve(ctx, skillName)
	if err != nil {
		return "", SkillHealthMetrics{}, err
	}
	return b.healthForResolved(ctx, resolved)
}

// indexArtifacts 校验 artifact 内容哈希并建立 name/version 索引。
func (b *RegistryBackend) indexArtifacts(artifacts []SkillArtifact) error {
	if len(artifacts) == 0 {
		return fmt.Errorf("skill artifacts are required")
	}
	for _, artifact := range artifacts {
		name := strings.TrimSpace(artifact.FrontMatter.Name)
		version := strings.TrimSpace(artifact.Version)
		if name == "" {
			return fmt.Errorf("skill artifact name is required")
		}
		if version == "" {
			return fmt.Errorf("skill artifact version is required: %s", name)
		}
		if strings.TrimSpace(artifact.Content) == "" {
			return fmt.Errorf("skill artifact content is required: %s@%s", name, version)
		}
		actualHash := computeSkillContentHash(artifact.Content)
		if artifact.ContentSHA256 != "" && artifact.ContentSHA256 != actualHash {
			return fmt.Errorf("content hash mismatch: %s@%s", name, version)
		}
		artifact.FrontMatter.Name = name
		artifact.Version = version
		artifact.ContentSHA256 = actualHash
		if _, ok := b.artifacts[name]; !ok {
			b.artifacts[name] = map[string]SkillArtifact{}
		}
		if _, exists := b.artifacts[name][version]; exists {
			return fmt.Errorf("duplicate skill artifact: %s@%s", name, version)
		}
		b.artifacts[name][version] = artifact
	}
	return nil
}

// indexAliases 校验 alias 指向已发布 artifact，并建立运行时解析索引。
func (b *RegistryBackend) indexAliases(aliases []SkillAlias) error {
	if len(aliases) == 0 {
		return fmt.Errorf("skill aliases are required")
	}
	for _, alias := range aliases {
		alias.SkillName = strings.TrimSpace(alias.SkillName)
		alias.Channel = normalizeRegistryChannel(alias.Channel)
		alias.Tenant = strings.TrimSpace(alias.Tenant)
		alias.Version = strings.TrimSpace(alias.Version)
		if alias.SkillName == "" {
			return fmt.Errorf("skill alias name is required")
		}
		if alias.Version == "" {
			return fmt.Errorf("skill alias version is required: %s", alias.SkillName)
		}
		if _, ok := b.lookupArtifact(alias.SkillName, alias.Version); !ok {
			return fmt.Errorf("skill alias points to missing artifact: %s@%s", alias.SkillName, alias.Version)
		}
		key := registryAliasKey{skillName: alias.SkillName, channel: alias.Channel, tenant: alias.Tenant}
		if _, exists := b.aliases[key]; exists {
			return fmt.Errorf("duplicate skill alias: %s channel=%s tenant=%s", alias.SkillName, alias.Channel, alias.Tenant)
		}
		b.aliases[key] = alias
	}
	return nil
}

// resolveAlias 按租户灰度优先、环境默认兜底的顺序解析版本指针。
func (b *RegistryBackend) resolveAlias(skillName string) (SkillAlias, bool) {
	if b.tenant != "" {
		key := registryAliasKey{skillName: skillName, channel: b.channel, tenant: b.tenant}
		if alias, ok := b.aliases[key]; ok {
			return alias, true
		}
	}
	key := registryAliasKey{skillName: skillName, channel: b.channel}
	alias, ok := b.aliases[key]
	return alias, ok
}

// visibleSkillNames 返回当前运行视角下有 alias 的 Skill 名称，保证 List 输出稳定。
func (b *RegistryBackend) visibleSkillNames() []string {
	seen := map[string]bool{}
	for key := range b.aliases {
		if key.channel != b.channel {
			continue
		}
		if key.tenant != "" && key.tenant != b.tenant {
			continue
		}
		seen[key.skillName] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// lookupArtifact 从 name/version 索引中读取不可变 Skill 版本。
func (b *RegistryBackend) lookupArtifact(skillName string, version string) (SkillArtifact, bool) {
	versions, ok := b.artifacts[skillName]
	if !ok {
		return SkillArtifact{}, false
	}
	artifact, ok := versions[version]
	return artifact, ok
}

// healthForResolved 用当前 alias 解析结果查询健康状态；未配置 store 时默认健康。
func (b *RegistryBackend) healthForResolved(ctx context.Context, resolved ResolvedSkill) (SkillHealthStatus, SkillHealthMetrics, error) {
	if b == nil || b.healthStore == nil {
		return SkillHealthStatusHealthy, SkillHealthMetrics{}, nil
	}
	return b.healthEvaluator.Evaluate(ctx, b.healthStore, SkillHealthQuery{
		SkillName: resolved.Alias.SkillName,
		Version:   resolved.Artifact.Version,
		Now:       b.now(),
	})
}

// isResolvedSkillDeprecated 把健康状态判断封装起来，避免 List/Get 分支散落。
func (b *RegistryBackend) isResolvedSkillDeprecated(ctx context.Context, resolved ResolvedSkill) (bool, error) {
	status, _, err := b.healthForResolved(ctx, resolved)
	if err != nil {
		return false, err
	}
	return status == SkillHealthStatusDeprecated, nil
}

// registryAliasLabel 记录事件来源时保留 channel/tenant 视角，方便后续审计。
func registryAliasLabel(alias SkillAlias) string {
	if strings.TrimSpace(alias.Tenant) != "" {
		return alias.Channel + "/" + alias.Tenant
	}
	return alias.Channel
}

// normalizeRegistryChannel 统一空 channel 语义，避免 alias 解析时出现双重默认值。
func normalizeRegistryChannel(channel string) string {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return defaultRegistryChannel
	}
	return channel
}

// computeSkillContentHash 计算 artifact 正文哈希，用于发布门禁和运行留证。
func computeSkillContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", sum)
}

var _ skill.Backend = (*RegistryBackend)(nil)
