package prompt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/plugin"
	coretool "github.com/OnslaughtSnail/caelis/core/tool"
	appresources "github.com/OnslaughtSnail/caelis/internal/app/resources"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
)

func TestBuildInstructionsRendersResourceCatalog(t *testing.T) {
	root := t.TempDir()
	promptPath := filepath.Join(root, "review.md")
	if err := os.WriteFile(promptPath, []byte("review prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	instructions, err := BuildInstructions(context.Background(), Config{
		AppName:      "caelis-test",
		WorkspaceDir: root,
		BasePrompt:   "session rule",
		Catalog: appresources.Catalog{
			AgentFiles: []appresources.AgentFile{
				{ID: "agents.global", Text: "global rule"},
				{ID: "agents.workspace", Text: "workspace agents rule"},
			},
			Prompts: []plugin.PromptFragment{
				{ID: "ignore", Scope: "ui", Text: "ui only"},
				{ID: "agents.global", Scope: "system", Priority: 100, Text: "global rule"},
				{ID: "agents.workspace", Scope: "system", Priority: 200, Text: "workspace agents rule"},
				{ID: "workspace", Scope: "system", Priority: 20, Text: "workspace rule"},
				{ID: "plugin", Scope: "system", Priority: 10, Paths: []string{promptPath}},
			},
			Skills: []plugin.SkillDescriptor{
				{Name: "review", Description: "Review code", Paths: []string{"/skills/review/SKILL.md"}},
				{Name: "echo", Description: "Echo input"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(instructions, "\n\n")
	for _, want := range []string{
		"<system_instructions>",
		"## Core Stable Rules",
		"You are caelis-test",
		"sandbox_permissions=require_escalated",
		"<user_custom_instructions>",
		"Session overrides workspace instructions",
		"## Session Overrides",
		"session rule",
		"## Workspace Instructions",
		"workspace agents rule",
		"## Global Instructions",
		"global rule",
		"### plugin\nreview prompt",
		"### workspace\nworkspace rule",
		"## Skills",
		"### Available skills",
		"- echo: Echo input",
		"- review: Review code (/skills/review/SKILL.md)",
		"<environment_context>",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("instructions = %q, missing %q", joined, want)
		}
	}
	for _, forbidden := range []string{"ui only", "### agents.global", "### agents.workspace"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("instructions = %q, want %q excluded", joined, forbidden)
		}
	}
	if strings.Index(joined, "### plugin") > strings.Index(joined, "### workspace") {
		t.Fatalf("instructions = %q, want priority order", joined)
	}
}

func TestBuildInstructionsCanDisableSkillMetadata(t *testing.T) {
	instructions, err := BuildInstructions(context.Background(), Config{
		SkillPolicy: appsettings.SkillPolicy{LoadingMode: appsettings.SkillLoadingModeDisabled},
		Catalog: appresources.Catalog{
			Skills: []plugin.SkillDescriptor{{Name: "review", Description: "Review code"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(instructions, "\n\n")
	if strings.Contains(joined, "## Skills") || strings.Contains(joined, "Review code") {
		t.Fatalf("instructions = %q, want skill metadata disabled", joined)
	}
}

func TestBuildInstructionsRejectsMissingPromptPathWithoutInlineFallback(t *testing.T) {
	_, err := BuildInstructions(context.Background(), Config{
		Catalog: appresources.Catalog{
			Prompts: []plugin.PromptFragment{{ID: "missing", Paths: []string{filepath.Join(t.TempDir(), "missing.md")}}},
		},
	})
	if err == nil {
		t.Fatal("BuildInstructions missing prompt error = nil, want error")
	}
}

func TestPromptUtilitiesEstimateAndDecorateSharedSystemPrompt(t *testing.T) {
	tools := []coretool.Tool{coretool.NamedTool{Def: coretool.Definition{
		Name:        "read",
		Description: "Read a file",
		InputSchema: map[string]any{"type": "object"},
	}}}
	if got := EstimateModelPromptPrefixTokens(nil, nil); got != 0 {
		t.Fatalf("empty prompt prefix tokens = %d, want 0", got)
	}
	prefix := EstimateModelPromptPrefixTokens(map[string]any{"system_prompt": "abcd"}, tools)
	if prefix <= EstimateTextTokens("abcd")+96 {
		t.Fatalf("prompt prefix tokens = %d, want tool estimate included", prefix)
	}

	base := "stable\n\n<environment_context>\n  <cwd>/repo</cwd>\n  <shell>powershell</shell>\n</environment_context>"
	decorated := WithWindowsSandboxTLSNote(base, true)
	if !strings.Contains(decorated, WindowsSandboxTLSNoteLine) {
		t.Fatalf("decorated prompt = %q, missing TLS note", decorated)
	}
	if again := WithWindowsSandboxTLSNote(decorated, true); again != decorated {
		t.Fatalf("TLS note was not idempotent:\n%s", again)
	}
	if disabled := WithWindowsSandboxTLSNote(base, false); disabled != base {
		t.Fatalf("disabled TLS note = %q, want unchanged base", disabled)
	}
}
