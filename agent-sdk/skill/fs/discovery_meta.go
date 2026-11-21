package fs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"gopkg.in/yaml.v3"
)

const maxMetaCacheEntries = 512

var metaCache = struct {
	sync.Mutex
	next    uint64
	entries map[string]metaCacheEntry
}{
	entries: map[string]metaCacheEntry{},
}

type metaCacheEntry struct {
	used     uint64
	meta     skill.Meta
	hash     string
	standard bool
}

// DefaultDiscoveryDirs returns workspace and user-global skill roots without
// product-specific system skill directories.
func DefaultDiscoveryDirs(workspaceDir string) []string {
	workspaceDir = strings.TrimSpace(workspaceDir)
	out := []string{}
	if workspaceDir != "" {
		out = append(out,
			filepath.Join(workspaceDir, ".claude", "skills"),
			filepath.Join(workspaceDir, ".opencode", "skills"),
			filepath.Join(workspaceDir, ".opencode", "skill"),
			filepath.Join(workspaceDir, ".agents", "skills"),
			filepath.Join(workspaceDir, "skills"),
		)
	}
	out = append(out,
		"~/.claude/skills",
		"~/.config/opencode/skills",
		"~/.config/opencode/skill",
		"~/.agents/skills",
	)
	return out
}

// DiscoverMetaRequest collects readable Skill metadata in discovery order.
// Unreadable roots and invalid entries are skipped.
func DiscoverMetaRequest(req skill.DiscoverRequest) []skill.Meta {
	dirs := discoveryDirs(req.Dirs, req.WorkspaceDir)
	pluginMetas, suppressedRegular := discoverPluginBundleMeta(req.PluginBundles)
	seenNames := map[string]struct{}{}
	out := scanRegularSkillDirs(dirs, func(meta skill.Meta, hash string) bool {
		if suppressedRegular[skillIdentityKey(meta.Name, hash)] {
			return false
		}
		nameKey := strings.ToLower(meta.Name)
		if _, ok := seenNames[nameKey]; ok {
			return false
		}
		seenNames[nameKey] = struct{}{}
		return true
	})
	return append(out, pluginMetas...)
}

// DiscoverRootMeta reads metadata for one explicitly selected Skill root. It
// does not scan siblings or load the Skill body into a model prompt.
func DiscoverRootMeta(root string) (skill.Meta, error) {
	resolved, err := ResolvePath(root)
	if err != nil {
		return skill.Meta{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return skill.Meta{}, err
	}
	if !info.IsDir() {
		return skill.Meta{}, fmt.Errorf("skill root is not a directory: %s", resolved)
	}
	path := filepath.Join(resolved, "SKILL.md")
	entry, err := readSkillMetadata(path)
	if err != nil {
		return skill.Meta{}, err
	}
	entry.meta.Source = skill.SourceRegular
	return entry.meta, nil
}

// DiscoverLegacyPluginCopies identifies regular Skills suppressed by matching plugin copies.
func DiscoverLegacyPluginCopies(req skill.DiscoverRequest) []skill.Meta {
	dirs := discoveryDirs(req.Dirs, req.WorkspaceDir)
	_, suppressedRegular := discoverPluginBundleMeta(req.PluginBundles)
	if len(suppressedRegular) == 0 {
		return nil
	}
	return scanRegularSkillDirs(dirs, func(meta skill.Meta, hash string) bool {
		return suppressedRegular[skillIdentityKey(meta.Name, hash)]
	})
}

func scanRegularSkillDirs(dirs []string, include func(skill.Meta, string) bool) []skill.Meta {
	var out []skill.Meta
	seenPaths := map[string]struct{}{}
	for _, dir := range dirs {
		resolvedDir, err := ResolvePath(dir)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(resolvedDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillPath := filepath.Join(resolvedDir, entry.Name(), "SKILL.md")
			skillPath = filepath.Clean(skillPath)
			if _, ok := seenPaths[skillPath]; ok {
				continue
			}
			seenPaths[skillPath] = struct{}{}
			metadata, err := readSkillMetadata(skillPath)
			if err != nil || !include(metadata.meta, metadata.hash) {
				continue
			}
			metadata.meta.Source = skill.SourceRegular
			out = append(out, metadata.meta)
		}
	}
	return out
}

func discoveryDirs(dirs []string, workspaceDir string) []string {
	if dirs == nil {
		return DefaultDiscoveryDirs(workspaceDir)
	}
	return dirs
}

func ResolvePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("empty skill path")
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path), nil
}

func readSkillMetadata(path string) (metaCacheEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return metaCacheEntry{}, err
	}
	// The same path, size, and timestamp can describe different file contents.
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	metaCache.Lock()
	cached, ok := metaCache.entries[path]
	if ok && cached.hash == hash {
		metaCache.next++
		cached.used = metaCache.next
		metaCache.entries[path] = cached
		metaCache.Unlock()
		return cached, nil
	}
	metaCache.Unlock()
	parsed, err := parseSkillContent(path, raw)
	if err != nil {
		return metaCacheEntry{}, err
	}
	metaCache.Lock()
	metaCache.next++
	entry := metaCacheEntry{used: metaCache.next, meta: parsed.meta, hash: hash, standard: parsed.standard}
	metaCache.entries[path] = entry
	pruneMetaCacheLocked()
	metaCache.Unlock()
	return entry, nil
}

func pruneMetaCacheLocked() {
	for len(metaCache.entries) > maxMetaCacheEntries {
		var oldestPath string
		var oldestUsed uint64
		for path, entry := range metaCache.entries {
			if oldestPath == "" || entry.used < oldestUsed {
				oldestPath = path
				oldestUsed = entry.used
			}
		}
		if oldestPath == "" {
			return
		}
		delete(metaCache.entries, oldestPath)
	}
}

type frontMatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type parsedSkill struct {
	meta     skill.Meta
	body     string
	standard bool
}

func parseSkillContent(path string, raw []byte) (parsedSkill, error) {
	content := normalizeText(string(raw))
	if content == "" {
		return parsedSkill{}, fmt.Errorf("empty SKILL.md: %s", path)
	}
	front, body, err := parseFrontMatter(content)
	if err != nil {
		return parsedSkill{}, fmt.Errorf("invalid skill frontmatter %s: %w", path, err)
	}
	directory := filepath.Base(filepath.Dir(path))
	name := firstNonEmpty(
		front.Name,
		firstHeading(body),
		directory,
	)
	description := firstNonEmpty(
		front.Description,
		firstParagraph(body),
	)
	if name == "" || description == "" {
		return parsedSkill{}, fmt.Errorf("invalid skill metadata: %s", path)
	}
	return parsedSkill{
		meta: skill.Meta{
			Name:        strings.TrimSpace(name),
			Description: strings.TrimSpace(description),
			Path:        path,
			LocalName:   strings.TrimSpace(name),
		},
		body:     strings.TrimSpace(body),
		standard: validStandardPluginSkill(front, directory),
	}, nil
}

func parseFrontMatter(content string) (frontMatter, string, error) {
	trimmed := strings.TrimLeft(content, "\n\r\t ")
	if !strings.HasPrefix(trimmed, "---\n") {
		return frontMatter{}, content, nil
	}
	rest := strings.TrimPrefix(trimmed, "---\n")
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		if strings.HasSuffix(rest, "\n---") {
			idx = len(rest) - len("\n---")
		} else {
			return frontMatter{}, "", fmt.Errorf("unterminated YAML frontmatter")
		}
	}
	body := ""
	if idx+len("\n---\n") <= len(rest) {
		body = rest[idx+len("\n---\n"):]
	}
	var fields frontMatter
	if err := yaml.Unmarshal([]byte(rest[:idx]), &fields); err != nil {
		return frontMatter{}, "", err
	}
	return fields, body, nil
}

func firstHeading(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func firstParagraph(content string) string {
	paragraph := make([]string, 0, 4)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(paragraph) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "```") {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			continue
		}
		paragraph = append(paragraph, trimmed)
		if len(paragraph) >= 2 {
			break
		}
	}
	return strings.Join(paragraph, " ")
}

func normalizeText(input string) string {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	input = strings.TrimPrefix(input, "\ufeff")
	return strings.TrimSpace(input)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
