package tuiapp

import (
	"os"
	"os/exec"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

func TestAmbiguousWidthLayout(t *testing.T) {
	// ansi reads RUNEWIDTH_EASTASIAN at initialization, so each policy needs
	// a fresh process rather than a runtime environment change.
	mode := os.Getenv("CAELIS_TEST_AMBIGUOUS_WIDTH")
	if mode == "" {
		for _, width := range []string{"0", "1"} {
			t.Run("east-asian="+width, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAmbiguousWidthLayout$", "-test.timeout=30s", "-test.v")
				cmd.Env = append(os.Environ(), "CAELIS_TEST_AMBIGUOUS_WIDTH="+width, "RUNEWIDTH_EASTASIAN="+width)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("width policy %s: %v\n%s", width, err, output)
				}
			})
		}
		return
	}

	digitWidth := 1
	wantRows := []string{"①OBS", "②p"}
	if mode == "1" {
		digitWidth = 2
		wantRows = []string{"①OB", "S②p"}
	}
	if got := displayColumns("①"); got != digitWidth {
		t.Fatalf("circled digit width = %d, want %d", got, digitWidth)
	}

	t.Run("streaming-rows", func(t *testing.T) {
		rows := RenderText(TextRenderRequest{
			Kind:    TextAssistant,
			Mode:    RenderStream,
			Raw:     "①OBS②p",
			Width:   4,
			BlockID: "ambiguous-width",
			Theme:   tuikit.DefaultTheme(),
		}).Rows
		var plain []string
		for _, row := range rows {
			plain = append(plain, row.Plain)
			if ansi.Strip(row.Styled) != row.Plain {
				t.Fatalf("styled row disagrees with copy text: %q, plain %q", row.Styled, row.Plain)
			}
			if got := ansi.StringWidth(row.Styled); got > 4 {
				t.Fatalf("streaming row %q occupies %d columns, want at most 4", row.Plain, got)
			}
		}
		if !slices.Equal(plain, wantRows) {
			t.Fatalf("streaming rows = %q, want %q", plain, wantRows)
		}
	})

	t.Run("selection", func(t *testing.T) {
		got := selectionTextFromLinesWithIndents(
			[]string{"①OBS②properties"}, []int{0},
			textSelectionPoint{line: 0, col: digitWidth},
			textSelectionPoint{line: 0, col: digitWidth + 3},
		)
		if got != "OBS" {
			t.Fatalf("selected text = %q, want OBS", got)
		}
		if got := alignDisplayColumnToCharBoundary("①OBS②properties", digitWidth+3+digitWidth-1); got != digitWidth+3 {
			t.Fatalf("circled digit selection boundary = %d, want %d", got, digitWidth+3)
		}
	})

	t.Run("grapheme-boundaries", func(t *testing.T) {
		clusters := []string{"①", "O", "②", "中", "e\u0301", "🏳️‍🌈"}
		var text string
		for _, cluster := range clusters {
			text += cluster
		}
		column := 0
		for _, cluster := range clusters {
			end := column + ansi.StringWidth(cluster)
			if got := graphemeSlice(text, column, end); got != cluster {
				t.Fatalf("slice [%d,%d) = %q, want %q", column, end, got, cluster)
			}
			column = end
		}
	})
}
