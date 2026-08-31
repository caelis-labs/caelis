package tuiapp

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestNewModelRendersInitialChoicePrompt(t *testing.T) {
	prompt := PromptRequestMsg{
		Title: "Trust this workspace's MCP setup?",
		Details: []PromptDetail{
			{Label: "Workspace", Value: "/tmp/project", Emphasis: true},
			{Label: "MCP access", Value: "Can start local programs.", Tone: PromptToneWarning},
		},
		Choices: []PromptChoice{
			{Label: "Trust and enable MCP", Value: "trusted", Detail: "Use workspace MCP in new Runtimes.", Tone: PromptToneAccent},
			{Label: "Continue without MCP", Value: "untrusted", Detail: "Keep workspace MCP disabled."},
		},
		DefaultChoice:  "trusted",
		StackedChoices: true,
		Response:       make(chan PromptResponse, 1),
	}
	model := NewModel(Config{InitialPrompt: &prompt, NoColor: true})
	model.width = 48
	model.height = 24
	view := ansi.Strip(model.renderPromptModal())
	for _, want := range []string{"Trust this workspace's MCP setup?", "/tmp/project", "Trust and enable MCP", "Use workspace MCP in new Runtimes.", "Continue without MCP"} {
		if !strings.Contains(view, want) {
			t.Fatalf("initial prompt = %q, want %q", view, want)
		}
	}
	if model.activePrompt == nil || model.activePrompt.choiceIndex != 0 {
		t.Fatalf("initial prompt default index = %#v, want first choice", model.activePrompt)
	}
	stackedPrimary := false
	lines := strings.Split(view, "\n")
	for index := 0; index+1 < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "▎ Trust and enable MCP" &&
			strings.TrimSpace(lines[index+1]) == "Use workspace MCP in new Runtimes." {
			stackedPrimary = true
			break
		}
	}
	if !stackedPrimary {
		t.Fatalf("stacked initial prompt = %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > model.width {
			t.Fatalf("narrow initial prompt line width = %d, want <= %d: %q", width, model.width, line)
		}
	}
}

func TestInitialChoicePromptUsesSemanticThemeTones(t *testing.T) {
	prompt := PromptRequestMsg{
		Title: "Trust this workspace's MCP setup?",
		Details: []PromptDetail{
			{Label: "MCP access", Value: "Can start local programs.", Tone: PromptToneWarning},
		},
		Choices: []PromptChoice{
			{Label: "Trust and enable MCP", Value: "trusted", Detail: "Use workspace MCP in new Runtimes.", Tone: PromptToneAccent},
			{Label: "Continue without MCP", Value: "untrusted", Detail: "Keep workspace MCP disabled."},
		},
		DefaultChoice:  "trusted",
		StackedChoices: true,
		Response:       make(chan PromptResponse, 1),
	}
	model := NewModel(Config{InitialPrompt: &prompt})
	model.width = 80
	model.height = 32
	model.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	model.activePrompt.choiceIndex = 1
	view := model.renderPromptModal()
	if got := ansiTextForForeground(t, view, model.theme.Tokens().Warning.GetForeground()); !strings.Contains(got, "MCP ACCESS:") {
		t.Fatalf("prompt does not use warning tone for MCP access: %q", view)
	}
	if got := ansiTextForForeground(t, view, model.theme.Tokens().Accent.GetForeground()); !strings.Contains(got, "Trust and enable MCP") {
		t.Fatalf("prompt does not use accent tone for primary choice: %q", view)
	}
	selectedLine := firstStyledLineContaining(view, "Continue without MCP")
	if !strings.Contains(selectedLine, sgrBackgroundCode(t, model.theme.SelectionBg)) {
		t.Fatalf("prompt does not use selection contrast for focused choice: %q", view)
	}
}

func TestInitialChoicePromptUsesWideTabularLayout(t *testing.T) {
	prompt := PromptRequestMsg{
		Title: "Trust this workspace's MCP setup?",
		Details: []PromptDetail{
			{Label: "Workspace", Value: "/tmp/project", Emphasis: true},
			{Label: "MCP access", Value: "Can start local programs.", Tone: PromptToneWarning},
			{Label: "Applies", Value: "New Runtimes only."},
		},
		Choices: []PromptChoice{
			{Label: "Trust and enable MCP", Value: "trusted", Detail: "Use workspace MCP in new Runtimes.", Tone: PromptToneAccent},
			{Label: "Continue without MCP", Value: "untrusted", Detail: "Keep workspace MCP disabled."},
		},
		DefaultChoice:  "trusted",
		StackedChoices: true,
		Response:       make(chan PromptResponse, 1),
	}
	model := NewModel(Config{InitialPrompt: &prompt, NoColor: true})
	model.width = 160
	model.height = 32
	view := ansi.Strip(model.renderPromptModal())
	lines := strings.Split(view, "\n")

	valueColumn := -1
	for _, item := range []struct {
		label string
		value string
	}{
		{label: "WORKSPACE:", value: "/tmp/project"},
		{label: "MCP ACCESS:", value: "Can start local programs."},
		{label: "APPLIES:", value: "New Runtimes only."},
	} {
		line := lineContaining(lines, item.label)
		column := strings.Index(line, item.value)
		if column < 0 {
			t.Fatalf("wide prompt row %q does not contain %q: %q", item.label, item.value, view)
		}
		if valueColumn < 0 {
			valueColumn = column
		} else if column != valueColumn {
			t.Fatalf("wide prompt value column = %d for %q, want %d: %q", column, item.label, valueColumn, view)
		}
	}

	for _, item := range []struct {
		label  string
		detail string
	}{
		{label: "Trust and enable MCP", detail: "Use workspace MCP in new Runtimes."},
		{label: "Continue without MCP", detail: "Keep workspace MCP disabled."},
	} {
		line := lineContaining(lines, item.label)
		if !strings.Contains(line, item.detail) {
			t.Fatalf("wide prompt choice %q wrapped its hint: %q", item.label, view)
		}
	}
	footer := lineContaining(lines, "↑/↓ select")
	for _, want := range []string{"enter confirm", "esc cancel"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("wide prompt footer wrapped or compacted %q: %q", want, view)
		}
	}
	if got := model.promptModalOuterWidth(); got != 120 {
		t.Fatalf("wide prompt outer width = %d, want 120", got)
	}
}

func TestInitialChoicePromptFitsNarrowTerminal(t *testing.T) {
	prompt := PromptRequestMsg{
		Title: "Trust this workspace's MCP setup?",
		Details: []PromptDetail{
			{Label: "Workspace", Value: "/tmp/project", Emphasis: true},
			{Label: "MCP access", Value: "This setup can start local programs or connect to external services.", Tone: PromptToneWarning},
		},
		Choices: []PromptChoice{
			{Label: "Trust and enable MCP", Value: "trusted", Detail: "Use workspace MCP in new Runtimes.", Tone: PromptToneAccent},
			{Label: "Continue without MCP", Value: "untrusted", Detail: "Keep workspace MCP disabled."},
		},
		DefaultChoice:  "trusted",
		StackedChoices: true,
		Response:       make(chan PromptResponse, 1),
	}
	model := NewModel(Config{InitialPrompt: &prompt, NoColor: true})
	model.width = 32
	model.height = 36
	view := ansi.Strip(model.renderPromptModal())
	for _, want := range []string{"Trust and enable MCP", "Continue without MCP"} {
		if !strings.Contains(view, want) {
			t.Fatalf("narrow initial prompt = %q, want %q", view, want)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > model.width {
			t.Fatalf("narrow initial prompt line width = %d, want <= %d: %q", width, model.width, line)
		}
	}
}
