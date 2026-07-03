package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestTextRendererSemanticPolicies(t *testing.T) {
	theme := tuikit.DefaultTheme()

	assistant := RenderText(TextRenderRequest{
		Kind:           TextAssistant,
		Mode:           RenderInlineOnly,
		MarkdownPolicy: MarkdownInline,
		Raw:            "Use `shell` now",
		Width:          80,
		BlockID:        "assistant",
		Theme:          theme,
		LineStyle:      tuikit.LineStyleAssistant,
	})
	if plain := joinRenderedPlain(assistant.Rows); strings.Contains(plain, "`") || !strings.Contains(plain, "shell") {
		t.Fatalf("assistant inline plain = %q, want markdown presentation stripped", plain)
	}

	reasoning := RenderText(TextRenderRequest{
		Kind:           TextReasoning,
		Mode:           RenderStream,
		MarkdownPolicy: MarkdownNone,
		Raw:            "**think** with `code`",
		Prefix:         "- ",
		Width:          80,
		BlockID:        "reasoning",
		Theme:          theme,
		LineStyle:      tuikit.LineStyleReasoning,
	})
	if plain := joinRenderedPlain(reasoning.Rows); !strings.Contains(plain, "**think**") || !strings.Contains(plain, "`code`") {
		t.Fatalf("reasoning plain = %q, want markdown source preserved", plain)
	}

	user := RenderText(TextRenderRequest{
		Kind:           TextUser,
		Mode:           RenderPlain,
		MarkdownPolicy: MarkdownNone,
		Raw:            "**literal** user text",
		Prefix:         "> ",
		Width:          80,
		BlockID:        "user",
		Theme:          theme,
		LineStyle:      tuikit.LineStyleUser,
	})
	if plain := joinRenderedPlain(user.Rows); !strings.Contains(plain, "**literal**") {
		t.Fatalf("user plain = %q, want markdown source preserved", plain)
	}
}
