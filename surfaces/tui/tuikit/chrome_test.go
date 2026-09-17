package tuikit

import (
	"testing"
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
