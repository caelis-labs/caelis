package gatewayapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSystemPromptIncludesPromptAssets(t *testing.T) {
	globalHome := t.TempDir()
	t.Setenv("HOME", globalHome)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("TZ", "Asia/Shanghai")

	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(globalHome, ".agents"), 0o755); err != nil {
		t.Fatalf("mkdir global agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(globalHome, ".agents", "AGENTS.md"), []byte("# Global\n\nGlobal rule."), 0o600); err != nil {
		t.Fatalf("write global AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("# Workspace\n\nWorkspace rule."), 0o600); err != nil {
		t.Fatalf("write workspace AGENTS.md: %v", err)
	}
	skillsRoot := filepath.Join(globalHome, ".agents", "skills", "echo")
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsRoot, "SKILL.md"), []byte("---\nname: echo\ndescription: Echo skill.\n---\n# Echo\n\nEcho skill body.\n"), 0o600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	prompt, err := buildSystemPrompt(promptConfig{
		AppName:      "CAELIS",
		WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatalf("buildSystemPrompt() error = %v", err)
	}
	for _, required := range []string{
		"<system_instructions>",
		"## Core Stable Rules",
		"<user_custom_instructions>",
		"Workspace rule.",
		"Global rule.",
		"<environment_context>",
		"<cwd>" + workspace + "</cwd>",
		"### Available skills",
		"echo",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q:\n%s", required, prompt)
		}
	}
}

func TestBuildSystemPromptPreservesSessionOverridePrecedence(t *testing.T) {
	globalHome := t.TempDir()
	t.Setenv("HOME", globalHome)

	if err := os.MkdirAll(filepath.Join(globalHome, ".agents"), 0o755); err != nil {
		t.Fatalf("mkdir global agents: %v", err)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(globalHome, ".agents", "AGENTS.md"), []byte("global"), 0o600); err != nil {
		t.Fatalf("write global AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("workspace"), 0o600); err != nil {
		t.Fatalf("write workspace AGENTS.md: %v", err)
	}

	prompt, err := buildSystemPrompt(promptConfig{
		AppName:      "CAELIS",
		WorkspaceDir: workspace,
		BasePrompt:   "session",
	})
	if err != nil {
		t.Fatalf("buildSystemPrompt() error = %v", err)
	}
	for _, required := range []string{
		"Session overrides workspace instructions, and workspace instructions override global instructions on conflict.",
		"## Session Overrides",
		"session",
		"## Workspace Instructions",
		"workspace",
		"## Global Instructions",
		"global",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q:\n%s", required, prompt)
		}
	}
}
