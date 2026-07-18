package tuiapp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestReasoningDisplaySeparatesAdjacentGPTSummarySteps(t *testing.T) {
	t.Parallel()

	rendered := RenderText(TextRenderRequest{
		Kind:      TextReasoning,
		Mode:      RenderStream,
		Raw:       "**Clarifying pause behavior****Analyzing scan errors****Recommending cautious updates**",
		Prefix:    "› ",
		Width:     100,
		BlockID:   "reasoning",
		Theme:     tuikit.DefaultTheme(),
		LineStyle: tuikit.LineStyleReasoning,
	})
	got := make([]string, 0, len(rendered.Rows))
	for _, row := range rendered.Rows {
		got = append(got, row.Plain)
	}
	want := []string{
		"› Clarifying pause behavior",
		"  Analyzing scan errors",
		"  Recommending cautious updates",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reasoning rows = %#v, want %#v", got, want)
	}
	if preview := reasoningPreviewText("**Clarifying****Analyzing****Recommending**", 100); preview != "Clarifying · Analyzing · Recommending" {
		t.Fatalf("preview = %q", preview)
	}
}

func TestReasoningDisplayPreservesIncompleteAndCodeMarkers(t *testing.T) {
	t.Parallel()

	partial := RenderText(TextRenderRequest{
		Kind:    TextReasoning,
		Mode:    RenderStream,
		Raw:     "**Analyzing",
		Prefix:  "› ",
		Width:   80,
		BlockID: "partial",
		Theme:   tuikit.DefaultTheme(),
	})
	if plain := joinRenderedPlain(partial.Rows); !strings.Contains(plain, "**Analyzing") {
		t.Fatalf("partial reasoning = %q, want unmatched streaming marker preserved", plain)
	}

	code := RenderText(TextRenderRequest{
		Kind:    TextReasoning,
		Mode:    RenderStream,
		Raw:     "Inspect `literal **** marker` safely",
		Prefix:  "› ",
		Width:   80,
		BlockID: "code",
		Theme:   tuikit.DefaultTheme(),
	})
	if len(code.Rows) != 1 || code.Rows[0].Plain != "› Inspect literal **** marker safely" {
		t.Fatalf("code reasoning rows = %#v, want inline code kept on one row", code.Rows)
	}
}
