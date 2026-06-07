package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/OnslaughtSnail/caelis/skill"
)

// AgentProfile is a parsed agent profile from a markdown file.
type AgentProfile struct {
	ID           string
	Name         string
	Description  string
	Capabilities []string
	Instructions string
	Path         string
}

// ParseAgentProfile reads and parses an agent profile from a markdown file.
func ParseAgentProfile(path string) (AgentProfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AgentProfile{}, fmt.Errorf("read profile %s: %w", path, err)
	}
	return ParseAgentProfileBytes(path, data)
}

// ParseAgentProfileBytes parses an agent profile from raw markdown bytes.
func ParseAgentProfileBytes(path string, data []byte) (AgentProfile, error) {
	text := string(data)
	if strings.TrimSpace(text) == "" {
		return AgentProfile{}, fmt.Errorf("empty profile: %s", path)
	}

	frontmatter, body := skill.ParseFrontmatter(text)

	p := AgentProfile{
		Path:         path,
		Instructions: strings.TrimSpace(body),
	}

	if id, ok := frontmatter["id"]; ok {
		p.ID = id
	}
	if name, ok := frontmatter["name"]; ok {
		p.Name = name
	}
	if desc, ok := frontmatter["description"]; ok {
		p.Description = desc
	}
	if caps, ok := frontmatter["capabilities"]; ok {
		for _, c := range strings.Split(caps, ",") {
			c = strings.TrimSpace(strings.ToLower(c))
			if c != "" {
				p.Capabilities = append(p.Capabilities, c)
			}
		}
	}

	// Normalize.
	p = NormalizeProfile(p)

	// Validate.
	if err := ValidateProfile(p); err != nil {
		return AgentProfile{}, err
	}

	return p, nil
}

// NormalizeProfile fills defaults and normalizes fields.
func NormalizeProfile(p AgentProfile) AgentProfile {
	p.Name = strings.TrimSpace(p.Name)
	p.Description = strings.TrimSpace(p.Description)
	p.Instructions = strings.TrimSpace(p.Instructions)

	// Deduplicate capabilities.
	if len(p.Capabilities) > 0 {
		seen := make(map[string]bool)
		var caps []string
		for _, c := range p.Capabilities {
			c = strings.ToLower(strings.TrimSpace(c))
			if c != "" && !seen[c] {
				seen[c] = true
				caps = append(caps, c)
			}
		}
		p.Capabilities = caps
	}

	// Derive ID from path if empty.
	if p.ID == "" && p.Path != "" {
		p.ID = profileIDFromPath(p.Path)
	}

	// Derive Name from ID if empty.
	if p.Name == "" {
		p.Name = p.ID
	}

	return p
}

// ValidateProfile checks that the profile is well-formed.
func ValidateProfile(p AgentProfile) error {
	if p.ID == "" {
		return fmt.Errorf("profile: ID is required (path: %s)", p.Path)
	}
	if p.Instructions == "" && p.Description == "" {
		return fmt.Errorf("profile %s: instructions or description required", p.ID)
	}
	return nil
}

// DiscoverAgentProfiles scans a directory for .md agent profiles.
func DiscoverAgentProfiles(dir string) []AgentProfile {
	dir = expandPath(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var profiles []AgentProfile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		p, err := ParseAgentProfile(path)
		if err != nil {
			continue
		}
		profiles = append(profiles, p)
	}
	return profiles
}

// profileIDFromPath derives an ID from a file path.
func profileIDFromPath(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return normalizeID(base)
}

// normalizeID normalizes a string into a valid ID.
func normalizeID(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteRune('-')
			prevDash = true
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "unnamed"
	}
	return result
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
