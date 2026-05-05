package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Meta struct {
	Name        string
	Description string
	Path        string
}

func DefaultDiscoveryDirs(workspaceDir string) []string {
	out := []string{"~/.agents/skills"}
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return out
	}
	out = append(out,
		filepath.Join(workspaceDir, ".agents", "skills"),
		filepath.Join(workspaceDir, "skills"),
	)
	return out
}

func DiscoverMeta(dirs []string, workspaceDir string) ([]Meta, error) {
	if len(dirs) == 0 {
		dirs = DefaultDiscoveryDirs(workspaceDir)
	}
	out := make([]Meta, 0)
	seen := map[string]struct{}{}
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
			if _, err := os.Stat(skillPath); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			skillPath = filepath.Clean(skillPath)
			if _, ok := seen[skillPath]; ok {
				continue
			}
			meta, err := parseMeta(skillPath)
			if err != nil {
				return nil, err
			}
			seen[skillPath] = struct{}{}
			out = append(out, meta)
		}
	}
	return out, nil
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
