package tuikit

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTokens_ResolvedFromDefaultTheme(t *testing.T) {
	theme := DefaultTheme()
	tok := theme.Tokens()

	// Tokens should produce non-empty styled output.
	if tok.TextPrimary.Render("hello") == "" {
		t.Fatal("TextPrimary token produced empty render")
	}
	if tok.Accent.Render("highlight") == "" {
		t.Fatal("Accent token produced empty render")
	}
	if tok.Separator.Render("─") == "" {
		t.Fatal("Separator token produced empty render")
	}
}

func TestTokens_CachedAndInvalidated(t *testing.T) {
	theme := DefaultTheme()

	tok1 := theme.Tokens()
	tok2 := theme.Tokens()
	// Cached: should return same values.
	if tok1.TextPrimary.Render("a") != tok2.TextPrimary.Render("a") {
		t.Fatal("cached tokens should produce identical renders")
	}

	theme.InvalidateTokens()
	tok3 := theme.Tokens()
	// After invalidation, tokens are re-resolved (should still be valid).
	if tok3.TextPrimary.Render("a") == "" {
		t.Fatal("re-resolved tokens should produce non-empty render")
	}
}

func TestRenderChromeBar_BasicLayout(t *testing.T) {
	theme := DefaultTheme()
	bar := RenderChromeBar(theme, ChromeBarModel{
		LeftLabel:  "workspace",
		LeftValue:  "caelis",
		RightLabel: "model",
		RightValue: "opus",
		Width:      60,
	})
	plain := ansi.Strip(bar)
	if !strings.Contains(plain, "workspace") || !strings.Contains(plain, "caelis") {
		t.Fatalf("expected left content, got %q", plain)
	}
	if !strings.Contains(plain, "model") || !strings.Contains(plain, "opus") {
		t.Fatalf("expected right content, got %q", plain)
	}
}

func TestRenderChromeBar_EmptyFields(t *testing.T) {
	theme := DefaultTheme()
	bar := RenderChromeBar(theme, ChromeBarModel{
		LeftLabel: "workspace",
		Width:     40,
	})
	plain := ansi.Strip(bar)
	if !strings.Contains(plain, "workspace") {
		t.Fatalf("expected left label, got %q", plain)
	}
}

func TestRenderSectionDivider_WithLabel(t *testing.T) {
	theme := DefaultTheme()
	div := RenderSectionDivider(theme, SectionDividerModel{
		Label: "compose",
		Width: 50,
	})
	plain := ansi.Strip(div)
	if !strings.Contains(plain, "compose") {
		t.Fatalf("expected label in divider, got %q", plain)
	}
	if !strings.Contains(plain, "─") {
		t.Fatalf("expected rule character in divider, got %q", plain)
	}
}

func TestRenderSectionDivider_PlainRule(t *testing.T) {
	theme := DefaultTheme()
	div := RenderSectionDivider(theme, SectionDividerModel{
		Width: 30,
	})
	plain := ansi.Strip(div)
	if len([]rune(plain)) != 30 {
		t.Fatalf("expected 30 rune rule, got %d (%q)", len([]rune(plain)), plain)
	}
}

func TestRenderSectionDivider_WithRightLabel(t *testing.T) {
	theme := DefaultTheme()
	div := RenderSectionDivider(theme, SectionDividerModel{
		Label:      "compose",
		RightLabel: "2 attachments",
		Width:      60,
	})
	plain := ansi.Strip(div)
	if !strings.Contains(plain, "compose") {
		t.Fatalf("expected label, got %q", plain)
	}
	if !strings.Contains(plain, "2 attachments") {
		t.Fatalf("expected right label, got %q", plain)
	}
}

func TestRenderStatusItem_WithTone(t *testing.T) {
	theme := DefaultTheme()
	tests := []struct {
		tone  string
		label string
		value string
	}{
		{"success", "status", "done"},
		{"warning", "status", "pending"},
		{"error", "status", "failed"},
		{"accent", "model", "opus"},
		{"", "context", "12k"},
	}
	for _, tt := range tests {
		item := RenderStatusItem(theme, StatusItemModel{
			Label: tt.label,
			Value: tt.value,
			Tone:  tt.tone,
		})
		plain := ansi.Strip(item)
		if !strings.Contains(plain, tt.label) {
			t.Errorf("tone=%q: expected label %q, got %q", tt.tone, tt.label, plain)
		}
		if !strings.Contains(plain, tt.value) {
			t.Errorf("tone=%q: expected value %q, got %q", tt.tone, tt.value, plain)
		}
	}
}

func TestRenderBadgePill_Empty(t *testing.T) {
	theme := DefaultTheme()
	got := RenderBadgePill(theme, BadgePillModel{Label: "", Tone: "success"})
	if got != "" {
		t.Fatalf("expected empty for empty label, got %q", got)
	}
}

func TestRenderBadgePill_NonEmpty(t *testing.T) {
	theme := DefaultTheme()
	got := RenderBadgePill(theme, BadgePillModel{Label: "running", Tone: "accent"})
	plain := ansi.Strip(got)
	if plain != "running" {
		t.Fatalf("expected 'running', got %q", plain)
	}
}
