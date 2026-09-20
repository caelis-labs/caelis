package promptassembly

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/bot"
)

// TestBuildBotSystemPromptComposesSharedAndBotFragments pins the Bot instruction
// baseline: it keeps the work-mode prompt's top-level structure and reuses the
// shared instruction/evidence section verbatim instead of restating it.
func TestBuildBotSystemPromptComposesSharedAndBotFragments(t *testing.T) {
	prompt := BuildBotSystemPrompt("caelis")
	if !strings.HasPrefix(prompt, "<system_instructions>\n") || !strings.HasSuffix(prompt, "\n</system_instructions>") {
		t.Fatalf("Bot prompt is not a system instruction block:\n%s", prompt)
	}
	shared := builtInInstructionEvidencePrompt()
	for _, want := range []string{
		"## Identity",
		"You are a Bot assistant in caelis",
		shared,
		"## Conversation",
		bot.NotebookSection,
		"## Capabilities And Permissions",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("Bot prompt is missing %q:\n%s", want, prompt)
		}
	}
	if strings.Count(prompt, shared) != 1 || strings.Count(prompt, bot.NotebookSection) != 1 {
		t.Fatalf("Bot prompt duplicated a shared fragment:\n%s", prompt)
	}
	// Bot mode must not inherit work-mode-only guidance, and it must describe its
	// capabilities from the request instead of hard-coding an absent tool set.
	for _, absent := range []string{"## Workspace Stewardship", "## Sandbox And Host Approval", "## Skills", "You have no tools"} {
		if strings.Contains(prompt, absent) {
			t.Fatalf("Bot prompt leaked %q:\n%s", absent, prompt)
		}
	}
	// The prefix depends only on the Host application name, so it stays stable
	// across Turns, Runtime reactivation, and Host restarts.
	if BuildBotSystemPrompt("caelis") != prompt {
		t.Fatal("Bot prompt is not deterministic")
	}
	if unnamed := BuildBotSystemPrompt(""); !strings.Contains(unnamed, "You are a Bot assistant in caelis") {
		t.Fatalf("unnamed Host prompt = %s", unnamed)
	}
}
