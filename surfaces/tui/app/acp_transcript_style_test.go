package tuiapp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/ports/model"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
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

func TestTerminalOutputKeepsNonBlankLineWhitespaceOnlyDropsBlankLines(t *testing.T) {
	t.Parallel()

	segments := tailWrappedTerminalSegments("  first line  \r\n   \r\nsecond line  \r\n", 80, 10)
	want := []string{"  first line  ", "second line  "}
	if strings.Join(segments, "\n") != strings.Join(want, "\n") {
		t.Fatalf("segments = %#v, want %#v", segments, want)
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

	line := styleToolEventLine(model.theme, "✓ RUN_COMMAND \x1b[31m└ failed\x1b[0m", tuikit.LineStyleTool)
	if got := ansi.Strip(line); got != "✓ RUN_COMMAND └ failed" {
		t.Fatalf("tool line strips to %q, want sanitized suffix", got)
	}
	if strings.Contains(line, "[31m") {
		t.Fatalf("source ANSI color leaked into tool line: %q", line)
	}
}

func TestExplorationSummaryWrappedDetailStylesContinuationNumbers(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 58, TermWidth: 58, Theme: model.theme}
	rows := wrapExplorationSummaryDetail("  └ ", "Read", "common.go 1~200, common.go 201~400, ebs_snapshot.go 901~1100", 58)
	if len(rows) < 2 {
		t.Fatalf("rows = %#v, want wrapped continuation", rows)
	}
	styled := styleExplorationSummaryRow(rows[1], ctx)
	if got := strings.TrimRight(ansi.Strip(styled), " "); got != rows[1] {
		t.Fatalf("styled strips to %q, want %q", got, rows[1])
	}
	numberFG := sgrForegroundCode(t, model.theme.TextStyle().GetForeground())
	numberText := normalizeInlineStyleText(textWithSGRForeground(styled, numberFG))
	if !strings.Contains(numberText, "901") || !strings.Contains(numberText, "1100") {
		t.Fatalf("continuation numbers not styled with number foreground\nnumbers=%q\nstyled=%q", numberText, styled)
	}
}

func TestTerminalErrorLineRedactsModelRetryDetails(t *testing.T) {
	line := terminalErrorLine(&model.RetryExhaustedError{
		MaxRetries: 5,
		Cause:      errors.New("model: http status 500 body=Internal Server Error"),
	})
	if line != "✗ model request failed after 5 retries" {
		t.Fatalf("terminalErrorLine() = %q, want redacted retry failure", line)
	}
	if strings.Contains(line, "Internal Server Error") || strings.Contains(line, "http status 500") {
		t.Fatalf("terminal error leaked provider detail: %q", line)
	}
}

func TestTerminalLifecycleForTaskResultRedactsModelRetryDetails(t *testing.T) {
	env := terminalLifecycleForTaskResult(TaskResultMsg{
		Err: &model.RetryExhaustedError{
			MaxRetries: 5,
			Cause:      errors.New("model: http status 500 body=Internal Server Error"),
		},
	}, time.Unix(120, 0))
	if env.Lifecycle == nil || env.Lifecycle.Reason != "model request failed after 5 retries" {
		t.Fatalf("terminal lifecycle = %#v, want redacted retry reason", env.Lifecycle)
	}
	line := terminalErrorLine(errorFromTerminalLifecycle(env))
	if line != "✗ model request failed after 5 retries" {
		t.Fatalf("terminalErrorLine(errorFromTerminalLifecycle()) = %q, want redacted retry failure", line)
	}
	if strings.Contains(line, "Internal Server Error") || strings.Contains(line, "http status 500") ||
		strings.Contains(env.Lifecycle.Reason, "Internal Server Error") || strings.Contains(env.Lifecycle.Reason, "http status 500") {
		t.Fatalf("terminal lifecycle leaked provider detail: reason=%q line=%q", env.Lifecycle.Reason, line)
	}
}
