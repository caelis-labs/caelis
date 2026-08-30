package tuiapp

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestNewModelRendersInitialChoicePrompt(t *testing.T) {
	prompt := PromptRequestMsg{
		Title:   "Trust this workspace?",
		Details: []PromptDetail{{Label: "Workspace", Value: "/tmp/project", Emphasis: true}},
		Choices: []PromptChoice{
			{Label: "Trust and continue", Value: "trusted"},
			{Label: "Continue without trust", Value: "untrusted"},
		},
		DefaultChoice: "untrusted",
		Response:      make(chan PromptResponse, 1),
	}
	model := NewModel(Config{InitialPrompt: &prompt, NoColor: true})
	view := ansi.Strip(model.renderPromptModal())
	for _, want := range []string{"Trust this workspace?", "/tmp/project", "Trust and continue", "Continue without trust"} {
		if !strings.Contains(view, want) {
			t.Fatalf("initial prompt = %q, want %q", view, want)
		}
	}
	if model.activePrompt == nil || model.activePrompt.choiceIndex != 1 {
		t.Fatalf("initial prompt default index = %#v, want second choice", model.activePrompt)
	}
}
