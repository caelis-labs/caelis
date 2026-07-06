package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestWelcomeVersionLabel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"1.2.3", "v1.2.3"},
		{"v1.2.3", "v1.2.3"},
		{"  2.0  ", "v2.0"},
		{"", "v0.0.0"},
	}
	for _, tc := range cases {
		if got := welcomeVersionLabel(tc.in); got != tc.want {
			t.Fatalf("welcomeVersionLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWelcomeBlockRenderWideLayout(t *testing.T) {
	theme := tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	block := NewWelcomeBlock("1.0.0")
	rows := block.Render(BlockRenderContext{
		Width:  80,
		Height: 20,
		Theme:  theme,
	})
	if len(rows) < 8 {
		t.Fatalf("Render() rows = %d, want logo block with padding", len(rows))
	}
	plain := strings.Join(rowPlainTexts(rows), "\n")
	if !strings.Contains(plain, "████") {
		t.Fatalf("wide welcome = %q, want block ASCII logo", plain)
	}
	if !strings.Contains(plain, "v1.0.0") {
		t.Fatalf("wide welcome = %q, want version label", plain)
	}
	if strings.Contains(plain, "workspace") {
		t.Fatalf("wide welcome = %q, should not show workspace metadata", plain)
	}
}

func TestWelcomeBlockRenderNarrowLayout(t *testing.T) {
	theme := tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	block := NewWelcomeBlock("v2.0.0")
	rows := block.Render(BlockRenderContext{
		Width:  40,
		Height: 8,
		Theme:  theme,
	})
	plain := ansi.Strip(strings.Join(rowPlainTexts(rows), "\n"))
	if strings.Contains(plain, "████") {
		t.Fatalf("narrow welcome = %q, should not render block logo", plain)
	}
	if !strings.Contains(plain, "CAELIS") || !strings.Contains(plain, "v2.0.0") {
		t.Fatalf("narrow welcome = %q, want compact title line", plain)
	}
	if !strings.Contains(plain, "type / for commands") {
		t.Fatalf("narrow welcome = %q, want command hint", plain)
	}
}

func TestRenderCompletionTextLineWithoutDetailMatchesOverlayWidth(t *testing.T) {
	model := NewModel(Config{})
	model.width = 79
	model.theme = tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)

	line := model.renderCompletionTextLine("short-name", "", false)
	if got := displayColumns(line); got != model.completionOverlayRenderedRowWidth() {
		t.Fatalf("row width = %d, want %d: %q", got, model.completionOverlayRenderedRowWidth(), line)
	}
}

func rowPlainTexts(rows []RenderedRow) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.Plain
	}
	return out
}
