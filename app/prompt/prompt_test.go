package prompt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OnslaughtSnail/caelis/skill"
)

func TestParseFrontmatter(t *testing.T) {
	content := `---
name: test-skill
description: A test skill
---
# Test

This is the body.`

	fm, body := skill.ParseFrontmatter(content)
	if fm["name"] != "test-skill" {
		t.Errorf("name: got %q", fm["name"])
	}
	if fm["description"] != "A test skill" {
		t.Errorf("description: got %q", fm["description"])
	}
	if body == "" {
		t.Error("expected non-empty body")
	}
}

func TestParseFrontmatterNoFM(t *testing.T) {
	content := `# Just markdown

No frontmatter here.`

	fm, body := skill.ParseFrontmatter(content)
	if fm != nil {
		t.Error("expected nil frontmatter")
	}
	if body == "" {
		t.Error("expected body")
	}
}

func TestSkillDiscovery(t *testing.T) {
	dir := t.TempDir()
	// Create two skills.
	s1 := filepath.Join(dir, "skill-a")
	s2 := filepath.Join(dir, "skill-b")
	os.MkdirAll(s1, 0o755)
	os.MkdirAll(s2, 0o755)
	os.WriteFile(filepath.Join(s1, "SKILL.md"), []byte("---\nname: skill-a\ndescription: First skill\n---\nBody"), 0o644)
	os.WriteFile(filepath.Join(s2, "SKILL.md"), []byte("---\nname: skill-b\ndescription: Second skill\n---\nBody"), 0o644)

	bundles, err := skill.Discover([]string{dir})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(bundles) != 2 {
		t.Fatalf("got %d, want 2", len(bundles))
	}
}

func TestSkillDiscoveryDedup(t *testing.T) {
	dir := t.TempDir()
	s1 := filepath.Join(dir, "my-skill")
	os.MkdirAll(s1, 0o755)
	os.WriteFile(filepath.Join(s1, "SKILL.md"), []byte("---\nname: my-skill\ndescription: dup\n---\nBody"), 0o644)

	// Same directory twice — should deduplicate.
	bundles, _ := skill.Discover([]string{dir, dir})
	if len(bundles) != 1 {
		t.Errorf("got %d, want 1 (dedup)", len(bundles))
	}
}

func TestAgentProfileParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reviewer.md")
	os.WriteFile(path, []byte(`---
id: reviewer
name: Code Reviewer
description: Reviews code
capabilities: review, tests
---
You are a code reviewer.`), 0o644)

	p, err := ParseAgentProfile(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.ID != "reviewer" {
		t.Errorf("ID: got %q", p.ID)
	}
	if p.Name != "Code Reviewer" {
		t.Errorf("Name: got %q", p.Name)
	}
	if len(p.Capabilities) != 2 {
		t.Errorf("Capabilities: got %d, want 2", len(p.Capabilities))
	}
	if p.Instructions != "You are a code reviewer." {
		t.Errorf("Instructions: got %q", p.Instructions)
	}
}

func TestAgentProfileNormalize(t *testing.T) {
	p := NormalizeProfile(AgentProfile{
		Path:         "/tmp/my-agent.md",
		Capabilities: []string{"REVIEW", "review", "Tests"},
	})
	if p.ID != "my-agent" {
		t.Errorf("ID: got %q", p.ID)
	}
	if len(p.Capabilities) != 2 {
		t.Errorf("Capabilities: got %d, want 2 (dedup)", len(p.Capabilities))
	}
}

func TestAgentProfileValidate(t *testing.T) {
	// Missing ID.
	err := ValidateProfile(AgentProfile{Instructions: "test"})
	if err == nil {
		t.Error("expected error for missing ID")
	}

	// Missing instructions and description.
	err = ValidateProfile(AgentProfile{ID: "test"})
	if err == nil {
		t.Error("expected error for missing instructions")
	}

	// Valid.
	err = ValidateProfile(AgentProfile{ID: "test", Instructions: "do stuff"})
	if err != nil {
		t.Errorf("expected valid, got %v", err)
	}
}

func TestPromptAssembly(t *testing.T) {
	cfg := Config{
		AppName:      "test-agent",
		WorkspaceDir: "/tmp/workspace",
		Shell:        "zsh",
		Skills: []skill.Bundle{
			{Name: "coding", Description: "Helps with code", Path: "/skills/coding/SKILL.md"},
		},
	}

	prompt := Assemble(cfg)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !contains(prompt, "test-agent") {
		t.Error("expected app name in prompt")
	}
	if !contains(prompt, "coding") {
		t.Error("expected skill name in prompt")
	}
	if !contains(prompt, "zsh") {
		t.Error("expected shell in prompt")
	}
	if !contains(prompt, "<system_instructions>") {
		t.Error("expected system_instructions tag")
	}
}

func TestPromptWithAgentProfiles(t *testing.T) {
	cfg := Config{
		AppName:      "test",
		WorkspaceDir: "/tmp",
		AgentProfiles: []AgentProfile{
			{ID: "reviewer", Instructions: "You review code."},
		},
	}

	prompt := Assemble(cfg)
	if !contains(prompt, "You review code.") {
		t.Error("expected agent profile instructions in prompt")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
