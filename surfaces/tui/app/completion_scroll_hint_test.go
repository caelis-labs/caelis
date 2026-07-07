package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestCompletionListDoesNotRenderEarlierOrMoreLines(t *testing.T) {
	model := NewModel(Config{
		SkillComplete: func(query string, limit int) ([]CompletionCandidate, error) {
			return numberedCompletionCandidates("skill", 12), nil
		},
	})
	model.setInputText("$")
	model.syncTextareaFromInput()
	model.refreshSkill()
	for i := 0; i < 10; i++ {
		_, _ = model.handleSkillKey(keyPress("down"))
	}

	rendered := ansi.Strip(model.renderSkillList())
	if strings.Contains(rendered, "earlier") || strings.Contains(rendered, "more") {
		t.Fatalf("renderSkillList() = %q, should not contain scroll text rows", rendered)
	}
}

func TestCompletionOverlayFooterShowsBelowList(t *testing.T) {
	model := NewModel(Config{
		SkillComplete: func(query string, limit int) ([]CompletionCandidate, error) {
			return numberedCompletionCandidates("skill", 12), nil
		},
	})
	model.width = 100
	model.setInputText("$")
	model.syncTextareaFromInput()
	model.refreshSkill()
	for i := 0; i < 10; i++ {
		_, _ = model.handleSkillKey(keyPress("down"))
	}

	rendered := ansi.Strip(model.renderSkillList())
	if !strings.Contains(rendered, "select") || !strings.Contains(rendered, "enter") {
		t.Fatalf("renderSkillList() = %q, want unified overlay footer", rendered)
	}
	if hint := strings.TrimSpace(model.hintRowText()); hint != "" {
		t.Fatalf("hintRowText() = %q, want empty while completion overlay is active", hint)
	}
}

func TestCompletionOverlayFooterAlwaysShowsWhenOverlayOpen(t *testing.T) {
	model := NewModel(Config{
		SkillComplete: func(query string, limit int) ([]CompletionCandidate, error) {
			return numberedCompletionCandidates("skill", 5), nil
		},
	})
	model.width = 100
	model.setInputText("$")
	model.syncTextareaFromInput()
	model.refreshSkill()

	rendered := ansi.Strip(model.renderSkillList())
	if !strings.Contains(rendered, "tab fill") {
		t.Fatalf("renderSkillList() = %q, want unified overlay footer even when list fits", rendered)
	}
}

func TestSkillScrollAffordanceAtTopAndBottom(t *testing.T) {
	model := NewModel(Config{
		SkillComplete: func(query string, limit int) ([]CompletionCandidate, error) {
			return numberedCompletionCandidates("skill", 12), nil
		},
	})
	model.setInputText("$")
	model.syncTextareaFromInput()
	model.refreshSkill()

	aff, ok := model.activeCompletionScroll()
	if !ok || !aff.Show {
		t.Fatalf("activeCompletionScroll() = %+v, ok=%v, want scrollable overlay", aff, ok)
	}
	if aff.CanUp {
		t.Fatalf("CanUp = true at top, want false")
	}
	if !aff.CanDown {
		t.Fatalf("CanDown = false at top, want true")
	}

	for i := 0; i < 11; i++ {
		_, _ = model.handleSkillKey(keyPress("down"))
	}
	aff, ok = model.activeCompletionScroll()
	if !ok || !aff.Show {
		t.Fatalf("activeCompletionScroll() after scroll = %+v, ok=%v, want scrollable overlay", aff, ok)
	}
	if !aff.CanUp {
		t.Fatalf("CanUp = false near bottom, want true")
	}
	if aff.CanDown {
		t.Fatalf("CanDown = true at bottom without more pages, want false")
	}
}

func TestPromptChoiceFooterBelowModal(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands()})
	model.width = 100
	model.height = 40
	choices := make([]PromptChoice, 0, 12)
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("plugin-%02d", i)
		choices = append(choices, PromptChoice{Label: id, Value: id})
	}
	model.activePrompt = newPromptState(PromptRequestMsg{
		Title:      "Manage plugins",
		Choices:    choices,
		Filterable: true,
		Response:   make(chan PromptResponse, 1),
	})
	model.activePrompt.choiceIndex = 10
	model.syncPromptChoiceWindow()

	out := ansi.Strip(model.renderPromptModal())
	if strings.Contains(out, "earlier") {
		t.Fatalf("renderPromptModal() = %q, should not contain scroll text rows", out)
	}
	for _, want := range []string{"type filter", "↑/↓ select", "enter confirm", "esc cancel"} {
		if !strings.Contains(out, want) {
			t.Fatalf("renderPromptModal() = %q, want prompt choice footer item %q", out, want)
		}
	}
	for _, forbidden := range []string{"tab fill", "enter apply", "space toggle"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("renderPromptModal() = %q, should not contain prompt choice footer item %q", out, forbidden)
		}
	}
	if hint := strings.TrimSpace(model.hintRowText()); hint != "" {
		t.Fatalf("hintRowText() = %q, want empty while prompt choices are active", hint)
	}
}

func TestPromptChoiceMultiSelectFooterIncludesToggle(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands()})
	model.width = 100
	model.height = 40
	model.activePrompt = newPromptState(PromptRequestMsg{
		Title:       "Manage plugins",
		Choices:     []PromptChoice{{Label: "enabled-plug", Value: "enabled-plug"}},
		Filterable:  true,
		MultiSelect: true,
		Response:    make(chan PromptResponse, 1),
	})

	out := ansi.Strip(model.renderPromptModal())
	for _, want := range []string{"type filter", "↑/↓ select", "space toggle", "enter confirm", "esc cancel"} {
		if !strings.Contains(out, want) {
			t.Fatalf("renderPromptModal() = %q, want prompt choice footer item %q", out, want)
		}
	}
	if strings.Contains(out, "tab fill") {
		t.Fatalf("renderPromptModal() = %q, should not contain completion footer", out)
	}
}

func TestPromptChoiceSingleSelectFooterMatchesApprovalControls(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands()})
	model.width = 100
	model.height = 40
	model.activePrompt = newPromptState(PromptRequestMsg{
		Title:    "Approval Required",
		Choices:  []PromptChoice{{Label: "allow", Value: "allow"}, {Label: "deny", Value: "deny"}},
		Response: make(chan PromptResponse, 1),
	})

	out := ansi.Strip(model.renderPromptModal())
	for _, want := range []string{"↑/↓ select", "enter confirm", "esc cancel"} {
		if !strings.Contains(out, want) {
			t.Fatalf("renderPromptModal() = %q, want prompt choice footer item %q", out, want)
		}
	}
	for _, forbidden := range []string{"type filter", "space toggle", "tab fill"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("renderPromptModal() = %q, should not contain prompt choice footer item %q", out, forbidden)
		}
	}
}

func TestPromptChoiceTabDoesNotMoveSelection(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands()})
	model.activePrompt = newPromptState(PromptRequestMsg{
		Title:      "Approval Required",
		Choices:    []PromptChoice{{Label: "allow", Value: "allow"}, {Label: "deny", Value: "deny"}},
		Filterable: true,
		Response:   make(chan PromptResponse, 1),
	})
	model.activePrompt.choiceIndex = 0

	model.handlePromptChoiceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if got := model.activePrompt.choiceIndex; got != 0 {
		t.Fatalf("choiceIndex after tab = %d, want unchanged", got)
	}
	if got := string(model.activePrompt.filter); got != "" {
		t.Fatalf("filter after tab = %q, want unchanged", got)
	}
}

func TestPromptChoiceFilterAcceptsJAndK(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands()})
	model.activePrompt = newPromptState(PromptRequestMsg{
		Title:      "Manage plugins",
		Choices:    []PromptChoice{{Label: "jupiter", Value: "jupiter"}, {Label: "kilo", Value: "kilo"}},
		Filterable: true,
		Response:   make(chan PromptResponse, 1),
	})

	model.handlePromptChoiceKey(keyPress("j"))
	if got := string(model.activePrompt.filter); got != "j" {
		t.Fatalf("filter after j = %q, want j", got)
	}
	model.handlePromptChoiceKey(keyPress("k"))
	if got := string(model.activePrompt.filter); got != "jk" {
		t.Fatalf("filter after k = %q, want jk", got)
	}
}
