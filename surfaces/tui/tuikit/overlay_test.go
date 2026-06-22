package tuikit

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestRenderOverlayFrame_BasicContent(t *testing.T) {
	theme := DefaultTheme()
	frame := RenderOverlayFrame(theme, OverlayFrameModel{
		Title: "Select Model",
		Body:  []string{"option 1", "option 2"},
		Width: 40,
	})
	plain := ansi.Strip(frame)
	if !strings.Contains(plain, "Select Model") {
		t.Fatalf("expected title, got %q", plain)
	}
	if !strings.Contains(plain, "option 1") || !strings.Contains(plain, "option 2") {
		t.Fatalf("expected body content, got %q", plain)
	}
	if !strings.Contains(plain, "╭") {
		t.Fatalf("expected rounded border, got %q", plain)
	}
}

func TestRenderOverlayFrame_NoTitle(t *testing.T) {
	theme := DefaultTheme()
	frame := RenderOverlayFrame(theme, OverlayFrameModel{
		Body:  []string{"single line"},
		Width: 30,
	})
	plain := ansi.Strip(frame)
	if !strings.Contains(plain, "single line") {
		t.Fatalf("expected body, got %q", plain)
	}
}

func TestRenderOverlayCompletion_EmptyItems(t *testing.T) {
	theme := DefaultTheme()
	got := RenderOverlayCompletion(theme, OverlayCompletionModel{
		Title: "Test",
		Items: nil,
		Width: 40,
	})
	if got != "" {
		t.Fatalf("expected empty for zero items, got %q", got)
	}
}

func TestRenderOverlayCompletion_SelectedHighlight(t *testing.T) {
	theme := DefaultTheme()
	got := RenderOverlayCompletion(theme, OverlayCompletionModel{
		Title: "Models",
		Items: []OverlayCompletionItem{
			{Label: "gpt-4"},
			{Label: "claude-4-opus", Desc: "recommended"},
			{Label: "gemini-2"},
		},
		Index: 1,
		Width: 50,
	})
	plain := ansi.Strip(got)
	if !strings.Contains(plain, "Models") {
		t.Fatalf("expected title, got %q", plain)
	}
	if !strings.Contains(plain, "claude-4-opus") {
		t.Fatalf("expected selected item, got %q", plain)
	}
	if !strings.Contains(plain, "▸") {
		t.Fatalf("expected selection indicator, got %q", plain)
	}
}

func TestRenderOverlayCompletion_SelectedHighlightAvoidsFocusAccent(t *testing.T) {
	theme := DefaultTheme()
	theme.Focus = lipgloss.Color("#123456")
	theme.PromptFg = theme.Focus
	theme.InvalidateTokens()
	got := RenderOverlayCompletion(theme, OverlayCompletionModel{
		Title: "Models",
		Items: []OverlayCompletionItem{
			{Label: "gpt-4"},
			{Label: "claude-4-opus", Desc: "recommended"},
		},
		Index: 1,
		Width: 50,
	})
	if strings.Contains(got, "38;2;18;52;86") {
		t.Fatalf("selected overlay completion still uses focus accent: %q", got)
	}
}

func TestRenderOverlayCompletion_ScrollIndicators(t *testing.T) {
	theme := DefaultTheme()
	items := make([]OverlayCompletionItem, 20)
	for i := range items {
		items[i] = OverlayCompletionItem{Label: strings.Repeat("item", 1)}
	}
	got := RenderOverlayCompletion(theme, OverlayCompletionModel{
		Title:   "Long List",
		Items:   items,
		Index:   10,
		Width:   40,
		MaxShow: 5,
	})
	plain := ansi.Strip(got)
	if !strings.Contains(plain, "↑ more") {
		t.Fatalf("expected scroll up indicator, got %q", plain)
	}
	if !strings.Contains(plain, "↓ more") {
		t.Fatalf("expected scroll down indicator, got %q", plain)
	}
}

func TestOverlayCenter_PlacesInMiddle(t *testing.T) {
	// 10x10 screen
	base := strings.Repeat(strings.Repeat(" ", 10)+"\n", 9) + strings.Repeat(" ", 10)
	overlay := "hello"

	result := OverlayCenter(base, overlay, 10, 10)
	lines := strings.Split(result, "\n")
	if len(lines) < 10 {
		t.Fatalf("expected 10 lines, got %d", len(lines))
	}
	// The overlay should be roughly in the middle (row ~4-5)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "hello") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("overlay content not found in result")
	}
}
