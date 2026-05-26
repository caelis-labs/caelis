package gatewayapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func TestBuildSystemPromptIncludesPromptAssets(t *testing.T) {
	globalHome := t.TempDir()
	setHomeForGatewayAppTest(t, globalHome)
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
		"## Shell Tool Permissions",
		"sandbox_permissions",
		"use RUN_COMMAND for shell work",
		"Start platform shell commands with default sandbox permissions",
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

func TestBuildSystemPromptOmitsDynamicTimeContext(t *testing.T) {
	globalHome := t.TempDir()
	setHomeForGatewayAppTest(t, globalHome)
	t.Setenv("SHELL", "/bin/zsh")
	workspace := t.TempDir()

	t.Setenv("TZ", "Asia/Shanghai")
	first, err := buildSystemPrompt(promptConfig{
		AppName:      "CAELIS",
		WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatalf("buildSystemPrompt(first) error = %v", err)
	}
	t.Setenv("TZ", "UTC")
	second, err := buildSystemPrompt(promptConfig{
		AppName:      "CAELIS",
		WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatalf("buildSystemPrompt(second) error = %v", err)
	}
	if first != second {
		t.Fatalf("prompt changed across timezone-only change:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	for _, forbidden := range []string{"<current_date>", "<timezone>"} {
		if strings.Contains(first, forbidden) {
			t.Fatalf("prompt contains dynamic context %q:\n%s", forbidden, first)
		}
	}
}

func TestWindowsSandboxTLSNoteInjectsIntoEnvironmentContext(t *testing.T) {
	base := "stable prefix\n\n<environment_context>\n  <cwd>C:\\work</cwd>\n  <shell>powershell</shell>\n</environment_context>"

	got := systemPromptWithWindowsSandboxTLSNote(base, true)
	stablePrefix := strings.TrimSuffix(base, "</environment_context>")
	if !strings.HasPrefix(got, stablePrefix) {
		t.Fatalf("prompt prefix before environment note changed:\n%s", got)
	}
	if !strings.Contains(got, "<shell>powershell</shell>\n"+windowsSandboxTLSNoteLine+"\n</environment_context>") {
		t.Fatalf("prompt missing Windows sandbox TLS note inside environment context:\n%s", got)
	}
	if strings.Contains(got, "<windows_sandbox_known_limits>") {
		t.Fatalf("prompt contains obsolete top-level known limits tag:\n%s", got)
	}
	if strings.Count(got, "<sandbox_tls>") != 1 {
		t.Fatalf("sandbox_tls tag count = %d, want 1", strings.Count(got, "<sandbox_tls>"))
	}
	if again := systemPromptWithWindowsSandboxTLSNote(got, true); again != got {
		t.Fatalf("Windows sandbox TLS note was appended more than once:\n%s", again)
	}
	if disabled := systemPromptWithWindowsSandboxTLSNote(base, false); disabled != base {
		t.Fatalf("disabled prompt = %q, want unchanged base", disabled)
	}
}

func TestWindowsSandboxTLSNoteEnabledOnlyForActiveWindowsSandbox(t *testing.T) {
	active := sandbox.Status{ResolvedBackend: sandbox.BackendWindows}
	if !windowsSandboxTLSNoteEnabledForGOOS(active, "windows") {
		t.Fatal("windowsSandboxTLSNoteEnabledForGOOS(active windows) = false, want true")
	}

	for _, tt := range []struct {
		name   string
		goos   string
		status sandbox.Status
	}{
		{name: "linux sandbox", goos: "linux", status: active},
		{name: "darwin sandbox", goos: "darwin", status: active},
		{name: "windows host", goos: "windows", status: sandbox.Status{ResolvedBackend: sandbox.BackendHost}},
		{name: "windows fallback", goos: "windows", status: sandbox.Status{ResolvedBackend: sandbox.BackendHost, FallbackToHost: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if windowsSandboxTLSNoteEnabledForGOOS(tt.status, tt.goos) {
				t.Fatal("windowsSandboxTLSNoteEnabledForGOOS() = true, want false")
			}
		})
	}
}

func TestBuildSystemPromptPermissionBoundariesAreRuntimeAgnostic(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	prompt, err := buildSystemPrompt(promptConfig{
		AppName:      "CAELIS",
		WorkspaceDir: workspace,
	})
	if err != nil {
		t.Fatalf("buildSystemPrompt() error = %v", err)
	}
	for _, required := range []string{
		"Start platform shell commands with default sandbox permissions",
		"use RUN_COMMAND for shell work",
		"workspace-local reads, builds, tests, and temp writes should stay default",
		"Use request_permissions for extra read/write paths",
		"Use `sandbox_permissions=require_escalated` only when host execution or host network access is required",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q:\n%s", required, prompt)
		}
	}
	for _, forbidden := range []string{
		"Default permission mode:",
		"Sandbox backend request:",
		"Start RUN_COMMAND commands",
		"Default RUN_COMMAND execution uses the sandbox route",
		"Default RUN_COMMAND execution uses the host route",
		"Default RUN_COMMAND execution uses the host backend",
		"Configured readable roots:",
		"Configured writable roots:",
		"Configured read-only subpaths:",
		"Base instructions are stable",
		"Active permissions are runtime policy state",
		"with_additional_permissions",
		"network grant",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt should not contain runtime-specific %q:\n%s", forbidden, prompt)
		}
	}
}

func TestBuildSystemPromptPreservesSessionOverridePrecedence(t *testing.T) {
	globalHome := t.TempDir()
	setHomeForGatewayAppTest(t, globalHome)

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
