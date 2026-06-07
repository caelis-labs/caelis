package skill

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Discover scans directories for SKILL.md files and returns metadata.
func Discover(dirs []string) ([]Bundle, error) {
	var bundles []Bundle
	seen := make(map[string]bool)

	for _, dir := range dirs {
		dir = expandPath(dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // skip missing directories
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
			if _, err := os.Stat(skillPath); err != nil {
				continue
			}
			bundle, err := loadBundle(skillPath)
			if err != nil {
				continue
			}
			// Deduplicate by normalized name.
			key := strings.ToLower(bundle.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			bundles = append(bundles, bundle)
		}
	}
	return bundles, nil
}

// Load reads a single skill bundle from a SKILL.md path.
func Load(path string) (Bundle, error) {
	return loadBundle(expandPath(path))
}

func loadBundle(path string) (Bundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Bundle{}, err
	}

	frontmatter, body := ParseFrontmatter(string(data))

	name := frontmatter["name"]
	desc := frontmatter["description"]

	// Fallback: derive name from directory name.
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
	}
	// Fallback: derive description from first paragraph.
	if desc == "" {
		desc = firstParagraph(body)
	}

	return Bundle{
		Name:        name,
		Description: desc,
		Content:     string(data),
		Path:        path,
		Metadata:    frontmatterToMap(frontmatter),
	}, nil
}

// ParseFrontmatter extracts YAML-style frontmatter from markdown content.
// Returns the key-value pairs and the body text after the frontmatter.
func ParseFrontmatter(content string) (map[string]string, string) {
	content = normalizeText(content)

	// Look for opening ---
	if !strings.HasPrefix(content, "---") {
		return nil, content
	}

	// Find closing ---
	endIdx := strings.Index(content[3:], "\n---")
	if endIdx < 0 {
		return nil, content
	}

	fmBlock := content[3 : 3+endIdx]
	body := strings.TrimSpace(content[3+endIdx+4:]) // skip \n---

	meta := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(fmBlock))
	for scanner.Scan() {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.ToLower(key))
		value = strings.TrimSpace(value)
		// Strip quotes.
		value = strings.Trim(value, "\"'")
		if key != "" && value != "" {
			meta[key] = value
		}
	}

	return meta, body
}

// firstParagraph returns the first non-empty paragraph from markdown text.
func firstParagraph(text string) string {
	lines := strings.Split(text, "\n")
	var para []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(para) > 0 {
				return strings.Join(para, " ")
			}
			continue
		}
		// Skip headings.
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		para = append(para, trimmed)
	}
	if len(para) > 0 {
		return strings.Join(para, " ")
	}
	return ""
}

// normalizeText cleans up text for consistent parsing.
func normalizeText(s string) string {
	// CRLF → LF.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	// Strip BOM.
	s = strings.TrimPrefix(s, "\ufeff")
	return strings.TrimSpace(s)
}

// frontmatterToMap converts string map to any map for Bundle.Metadata.
func frontmatterToMap(fm map[string]string) map[string]any {
	if fm == nil {
		return nil
	}
	m := make(map[string]any, len(fm))
	for k, v := range fm {
		m[k] = v
	}
	return m
}

// expandPath expands ~ and relative paths.
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return path
		}
		return filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

// DefaultDiscoveryDirs returns the default skill discovery directories.
func DefaultDiscoveryDirs(workspaceDir string) []string {
	return []string{
		filepath.Join("~", ".caelis", "skills", ".system"),
		filepath.Join(workspaceDir, ".agents", "skills"),
		filepath.Join(workspaceDir, "skills"),
		filepath.Join("~", ".caelis", "skills"),
		filepath.Join("~", ".agents", "skills"),
	}
}
