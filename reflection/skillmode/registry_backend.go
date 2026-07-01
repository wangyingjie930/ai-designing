package skillmode

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

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
	Channel string
	Tenant  string
}

// ResolvedSkill 保留运行时留证需要的 alias、artifact 和 Eino Skill 结果。
type ResolvedSkill struct {
	Skill    skill.Skill
	Alias    SkillAlias
	Artifact SkillArtifact
}

// RegistryBackend 用 release manifest 实现 Eino skill.Backend，隔离发布治理和 middleware 执行。
type RegistryBackend struct {
	channel   string
	tenant    string
	artifacts map[string]map[string]SkillArtifact
	aliases   map[registryAliasKey]SkillAlias
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
	backend := &RegistryBackend{
		channel:   channel,
		tenant:    strings.TrimSpace(options.Tenant),
		artifacts: map[string]map[string]SkillArtifact{},
		aliases:   map[registryAliasKey]SkillAlias{},
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
