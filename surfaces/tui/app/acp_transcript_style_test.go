package tuiapp

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestACPToolOutputSanitizesANSIAndKeepsStructuralPrefix(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 64, TermWidth: 64, Theme: model.theme}

	rows := renderACPToolOutputRowsWithToken(
		"block-1",
		"  └ ",
		"\x1b[31m└ failed\x1b[0m\n\x1b[32m+line\x1b[0m",
		64,
		ctx,
		ctx.Theme.ToolOutputStyle(),
		"",
	)

	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %#v", len(rows), rows)
	}
	wantPlain := []string{"  └ └ failed", "    +line"}
	for i, row := range rows {
		if row.Plain != wantPlain[i] {
			t.Fatalf("row %d plain = %q, want %q", i, row.Plain, wantPlain[i])
		}
		if got := strings.TrimRight(ansi.Strip(row.Styled), " "); got != row.Plain {
			t.Fatalf("row %d styled strips to %q, want %q", i, got, row.Plain)
		}
		if strings.Contains(row.Styled, "[31m") || strings.Contains(row.Styled, "[32m") {
			t.Fatalf("row %d leaked source ANSI color into styled output: %q", i, row.Styled)
		}
	}
	metaPrefix := ctx.Theme.TranscriptMetaStyle().Render("  └ ")
	if !strings.HasPrefix(rows[0].Styled, metaPrefix) {
		t.Fatalf("structural prefix should use transcript meta style\nprefix=%q\nstyled=%q", metaPrefix, rows[0].Styled)
	}
}

func TestStyleTerminalOutputLineSanitizesANSI(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 64, TermWidth: 64, Theme: model.theme}

	styled := styleTerminalOutputLine(ctx, "  └ ", "\x1b[31mfailed\x1b[0m", ctx.Theme.ToolErrorStyle())
	if got := strings.TrimRight(ansi.Strip(styled), " "); got != "  └ failed" {
		t.Fatalf("styled strips to %q, want structural prefix plus sanitized content", got)
	}
	if strings.Contains(styled, "[31m") {
		t.Fatalf("source ANSI color leaked into terminal output line: %q", styled)
	}
	metaPrefix := ctx.Theme.TranscriptMetaStyle().Render("  └ ")
	if !strings.HasPrefix(styled, metaPrefix) {
		t.Fatalf("structural prefix should use transcript meta style\nprefix=%q\nstyled=%q", metaPrefix, styled)
	}
}

func TestACPHeaderAndToolLineSanitizeSourceANSI(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme}

	header := styleACPTranscriptHeader(ctx, "• Ran \x1b[31mgit\x1b[0m status --short")
	if got := ansi.Strip(header); got != "• Ran git status --short" {
		t.Fatalf("header strips to %q, want sanitized shell command", got)
	}
	if strings.Contains(header, "[31m") {
		t.Fatalf("source ANSI color leaked into header: %q", header)
	}

	line := styleToolEventLine(model.theme, "✓ BASH \x1b[31m└ failed\x1b[0m", tuikit.LineStyleTool)
	if got := ansi.Strip(line); got != "✓ BASH └ failed" {
		t.Fatalf("tool line strips to %q, want sanitized suffix", got)
	}
	if strings.Contains(line, "[31m") {
		t.Fatalf("source ANSI color leaked into tool line: %q", line)
	}
}
