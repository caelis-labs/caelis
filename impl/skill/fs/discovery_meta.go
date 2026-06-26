package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/impl/skill/system"
	"github.com/OnslaughtSnail/caelis/ports/skill"
)

type Meta = skill.Meta

const maxMetaCacheEntries = 512

var metaCache = struct {
	sync.Mutex
	next    uint64
	entries map[string]metaCacheEntry
}{
	entries: map[string]metaCacheEntry{},
}

type metaCacheEntry struct {
	size    int64
	modTime int64
	used    uint64
	meta    Meta
}

func DefaultDiscoveryDirs(workspaceDir string) []string {
	out := []string{"~/.caelis/skills/.system"}
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir != "" {
		out = append(out,
			filepath.Join(workspaceDir, ".agents", "skills"),
			filepath.Join(workspaceDir, "skills"),
		)
	}
	out = append(out,
		"~/.caelis/skills",
		"~/.agents/skills",
	)
	return out
}

func DiscoverMeta(dirs []string, workspaceDir string) ([]Meta, error) {
	if len(dirs) == 0 {
		systemRoot, err := system.Ensure()
		dirs = DefaultDiscoveryDirs(workspaceDir)
		if err != nil {
			dirs = withoutDiscoveryDir(dirs, systemRoot)
		}
	} else if systemDiscoveryRequested(dirs) {
		systemRoot, err := system.Ensure()
		if err != nil {
			dirs = withoutDiscoveryDir(dirs, systemRoot)
		}
	}
	out := make([]Meta, 0)
	seenPaths := map[string]struct{}{}
	seenNames := map[string]struct{}{}
	for _, dir := range dirs {
		resolvedDir, err := ResolvePath(dir)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(resolvedDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(resolvedDir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry == nil || !entry.IsDir() {
				continue
			}
			skillPath := filepath.Join(resolvedDir, entry.Name(), "SKILL.md")
			info, err := os.Stat(skillPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			skillPath = filepath.Clean(skillPath)
			if _, ok := seenPaths[skillPath]; ok {
				continue
			}
			meta, err := parseMetaCached(skillPath, info)
			if err != nil {
				return nil, err
			}
			nameKey := strings.ToLower(strings.TrimSpace(meta.Name))
			if nameKey == "" {
				continue
			}
			seenPaths[skillPath] = struct{}{}
			if _, ok := seenNames[nameKey]; ok {
				continue
			}
			seenNames[nameKey] = struct{}{}
			out = append(out, meta)
		}
	}
	return out, nil
}

func systemDiscoveryRequested(dirs []string) bool {
	systemRoot, rootErr := system.Root()
	for _, dir := range dirs {
		if rootErr == nil {
			resolved, err := ResolvePath(dir)
			if err == nil && sameDiscoveryPath(resolved, systemRoot) {
				return true
			}
		}
		if filepath.ToSlash(filepath.Clean(strings.TrimSpace(dir))) == "~/.caelis/skills/.system" {
			return true
		}
	}
	return false
}

func withoutDiscoveryDir(dirs []string, skip string) []string {
	skip = filepath.Clean(strings.TrimSpace(skip))
	if skip == "" || skip == "." {
		return append([]string(nil), dirs...)
	}
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		resolved, err := ResolvePath(dir)
		if err == nil && sameDiscoveryPath(resolved, skip) {
			continue
		}
		out = append(out, dir)
	}
	return out
}

func sameDiscoveryPath(a string, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if a == b {
		return true
	}
	return strings.EqualFold(a, b)
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

func parseMetaCached(path string, info os.FileInfo) (Meta, error) {
	if info == nil {
		return parseMeta(path)
	}
	entry := metaCacheEntry{
		size:    info.Size(),
		modTime: info.ModTime().UnixNano(),
	}
	metaCache.Lock()
	cached, ok := metaCache.entries[path]
	if ok && cached.size == entry.size && cached.modTime == entry.modTime {
		metaCache.next++
		cached.used = metaCache.next
		metaCache.entries[path] = cached
		metaCache.Unlock()
		return cached.meta, nil
	}
	metaCache.Unlock()
	meta, err := parseMeta(path)
	if err != nil {
		return Meta{}, err
	}
	entry.meta = meta
	metaCache.Lock()
	metaCache.next++
	entry.used = metaCache.next
	metaCache.entries[path] = entry
	pruneMetaCacheLocked()
	metaCache.Unlock()
	return meta, nil
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

func parseMeta(path string) (Meta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Meta{}, err
	}
	content := normalizeText(string(raw))
	if content == "" {
		return Meta{}, fmt.Errorf("empty SKILL.md: %s", path)
	}
	frontMatter, body := parseFrontMatter(content)
	name := firstNonEmpty(
		frontMatter["name"],
		firstHeading(body),
		filepath.Base(filepath.Dir(path)),
	)
	description := firstNonEmpty(
		frontMatter["description"],
		firstParagraph(body),
	)
	if name == "" || description == "" {
		return Meta{}, fmt.Errorf("invalid skill metadata: %s", path)
	}
	return Meta{
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		Path:        path,
	}, nil
}

func parseFrontMatter(content string) (map[string]string, string) {
	trimmed := strings.TrimLeft(content, "\n\r\t ")
	if !strings.HasPrefix(trimmed, "---\n") {
		return map[string]string{}, content
	}
	rest := strings.TrimPrefix(trimmed, "---\n")
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return map[string]string{}, content
	}
	front := rest[:idx]
	body := rest[idx+len("\n---\n"):]
	values := map[string]string{}
	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(strings.ToLower(parts[0]))
		value := strings.TrimSpace(parts[1])
		values[key] = strings.Trim(value, `"'`)
	}
	return values, body
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
