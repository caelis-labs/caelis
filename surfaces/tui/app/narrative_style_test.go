package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
)

func TestRenderPlainUserRowsMatchComposerPrompt(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	rows := renderPlainUserRows("user", "hello\nworld", "▌ ", 40, theme)
	if len(rows) < 3 {
		t.Fatalf("rows = %#v, want chrome padding plus content", rows)
	}

	var content []RenderedRow
	for _, row := range rows {
		if strings.TrimSpace(row.Plain) != "" {
			content = append(content, row)
		}
	}
	if len(content) != 2 {
		t.Fatalf("content rows = %#v, want hello and world", content)
	}
	if !strings.Contains(content[0].Plain, "> hello") || strings.Contains(content[0].Plain, "▌") {
		t.Fatalf("first user row = %q, want composer prompt without rail", content[0].Plain)
	}
	if strings.Contains(content[1].Plain, ">") || !strings.Contains(content[1].Plain, "world") {
		t.Fatalf("continuation row = %q, want world without repeating prompt", content[1].Plain)
	}
	wantIndent := displayColumns(userNarrativeOuterPad() + userNarrativePrefix)
	if content[0].selectionIndent != wantIndent {
		t.Fatalf("selection indent = %d, want %d", content[0].selectionIndent, wantIndent)
	}

	composerSurface := sgrBackgroundCode(t, theme.ComposerBg)
	if !strings.Contains(content[0].Styled, composerSurface) {
		t.Fatalf("user content missing composer surface %q: %q", composerSurface, content[0].Styled)
	}
	if userSurface := sgrBackgroundCode(t, theme.UserBg); userSurface != composerSurface && strings.Contains(content[0].Styled, userSurface) {
		t.Fatalf("user content reused warm surface %q: %q", userSurface, content[0].Styled)
	}
}
