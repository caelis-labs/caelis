package prompt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/plugin"
	appresources "github.com/OnslaughtSnail/caelis/internal/app/resources"
)

func TestBuildInstructionsRendersResourceCatalog(t *testing.T) {
	root := t.TempDir()
	promptPath := filepath.Join(root, "review.md")
	if err := os.WriteFile(promptPath, []byte("review prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	instructions, err := BuildInstructions(context.Background(), Config{
		AppName: "caelis-test",
		Catalog: appresources.Catalog{
			Prompts: []plugin.PromptFragment{
				{ID: "ignore", Scope: "ui", Text: "ui only"},
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
		"You are caelis-test",
		"sandbox_permissions=require_escalated",
		"### plugin\nreview prompt",
		"### workspace\nworkspace rule",
		"### Available Skills",
		"- echo: Echo input",
		"- review: Review code (/skills/review/SKILL.md)",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("instructions = %q, missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "ui only") {
		t.Fatalf("instructions = %q, want ui-scoped prompt excluded", joined)
	}
	if strings.Index(joined, "### plugin") > strings.Index(joined, "### workspace") {
		t.Fatalf("instructions = %q, want priority order", joined)
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
