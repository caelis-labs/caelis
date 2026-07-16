package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginServiceSkillsFollowEnabledStateAndSuppressLegacyCopies(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	regularSkills := filepath.Join(tmp, "regular-skills")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}

	pluginDir := filepath.Join(tmp, "skillplugin")
	skillDir := filepath.Join(pluginDir, "skills", "runtime-skill")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("mkdir plugin skill dir: %v", err)
	}
	buildMinimalPluginDir(t, pluginDir, `{"name":"skill-plugin","version":"1.0.0"}`)
	skillContent := []byte("---\nname: runtime-skill\ndescription: Runtime plugin skill.\n---\n# Runtime Skill\n")
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), skillContent, 0o600); err != nil {
		t.Fatalf("write plugin skill: %v", err)
	}
	legacyCopyDir := filepath.Join(regularSkills, "runtime-skill")
	if err := os.MkdirAll(legacyCopyDir, 0o700); err != nil {
		t.Fatalf("mkdir legacy copy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyCopyDir, "SKILL.md"), skillContent, 0o600); err != nil {
		t.Fatalf("write legacy copy: %v", err)
	}

	stack, err := NewLocalStack(Config{
		AppName:      "CAELIS",
		StoreDir:     storeDir,
		WorkspaceCWD: workspaceDir,
		SkillDirs:    []string{regularSkills},
		Sandbox:      SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	ctx := context.Background()

	if _, err := stack.Plugins().AddPath(ctx, pluginDir); err != nil {
		t.Fatalf("AddPath() error = %v", err)
	}
	systemPrompt, _ := stack.runtime.BaseMetadata["system_prompt"].(string)
	if !strings.Contains(systemPrompt, "skillplugin:runtime-skill") {
		t.Fatalf("namespaced plugin skill missing after add:\n%s", systemPrompt)
	}
	if strings.Contains(systemPrompt, filepath.Join(legacyCopyDir, "SKILL.md")) {
		t.Fatalf("legacy regular copy leaked into prompt:\n%s", systemPrompt)
	}
	discovered, err := stack.Skills().Discover(ctx, workspaceDir)
	if err != nil {
		t.Fatalf("Skills().Discover() error = %v", err)
	}
	if !skillNamesForPluginTest(discovered)["skillplugin:runtime-skill"] {
		t.Fatalf("plugin skill missing from discovery: %#v", discovered)
	}
	if skillNamesForPluginTest(discovered)["runtime-skill"] {
		t.Fatalf("legacy regular copy leaked into discovery: %#v", discovered)
	}

	report, err := stack.Doctor(ctx, DoctorRequest{})
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	if !strings.Contains(strings.Join(report.Warnings, "\n"), filepath.Join(legacyCopyDir, "SKILL.md")) {
		t.Fatalf("Doctor warnings = %#v, want legacy copy warning", report.Warnings)
	}

	if _, err := stack.Plugins().Disable(ctx, "skillplugin"); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	systemPrompt, _ = stack.runtime.BaseMetadata["system_prompt"].(string)
	if strings.Contains(systemPrompt, "runtime-skill") {
		t.Fatalf("plugin skill or legacy copy still present after disable:\n%s", systemPrompt)
	}
	discovered, err = stack.Skills().Discover(ctx, workspaceDir)
	if err != nil {
		t.Fatalf("Skills().Discover() after disable error = %v", err)
	}
	if skillNamesForPluginTest(discovered)["runtime-skill"] || skillNamesForPluginTest(discovered)["skillplugin:runtime-skill"] {
		t.Fatalf("skills leaked after disable: %#v", discovered)
	}
}

func skillNamesForPluginTest(metas []SkillMeta) map[string]bool {
	out := map[string]bool{}
	for _, meta := range metas {
		if name := strings.TrimSpace(meta.Name); name != "" {
			out[name] = true
		}
	}
	return out
}
