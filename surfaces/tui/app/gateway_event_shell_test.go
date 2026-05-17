package tuiapp

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestRanHeaderStylesShellCommandTokens(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	plain := "• Ran GOMODCACHE=/tmp/cache git status --short --branch"
	styled := styleACPTranscriptHeader(BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme}, plain)
	if got := ansi.Strip(styled); got != plain {
		t.Fatalf("styled header strips to %q, want %q", got, plain)
	}
	if styled == plain || !strings.Contains(styled, "\x1b[") {
		t.Fatalf("styled header = %q, want ANSI token styling", styled)
	}
}

func TestRanHeaderShellCommandUsesDistinctTokenStyles(t *testing.T) {
	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 160, TermWidth: 160, Theme: model.theme}
	plain := `• Ran ls -la /home/xueyongzhi/WorkDir/code/demo/.venv/bin/ 2>/dev/null | head -20; echo "---"; cat /home/xueyongzhi/WorkDir/code/demo/.venv/pyvenv.cfg 2>/dev/null`
	styled := styleACPTranscriptHeader(ctx, plain)
	if got := ansi.Strip(styled); got != plain {
		t.Fatalf("styled header strips to %q, want %q", got, plain)
	}

	ranStyle := toolActionStyle(ctx, "Ran").Render("token")
	commandStyle := shellTokenStyle(ctx, shellTokenCommand).Render("token")
	operatorStyle := shellTokenStyle(ctx, shellTokenOperator).Render("token")
	redirectStyle := shellTokenStyle(ctx, shellTokenRedirect).Render("token")
	pathStyle := shellTokenStyle(ctx, shellTokenPath).Render("token")
	for name, rendered := range map[string]string{
		"ran":      ranStyle,
		"command":  commandStyle,
		"operator": operatorStyle,
		"redirect": redirectStyle,
		"path":     pathStyle,
	} {
		if rendered == "token" || !strings.Contains(rendered, "\x1b[") {
			t.Fatalf("%s style = %q, want ANSI styling", name, rendered)
		}
	}
	if ranStyle == commandStyle {
		t.Fatalf("Ran action and shell commands should not share the same style: %q", ranStyle)
	}
	if commandStyle == operatorStyle {
		t.Fatalf("shell commands and operators should not share the same style: %q", commandStyle)
	}
	if operatorStyle == redirectStyle {
		t.Fatalf("shell operators and redirects should not share the same style: %q", operatorStyle)
	}
}

func TestShellCommandTokensClassifyCompoundBashCommand(t *testing.T) {
	command := `ls -la /home/xueyongzhi/WorkDir/code/demo/.venv/bin/ 2>/dev/null | head -20; echo "---"; cat /home/xueyongzhi/WorkDir/code/demo/.venv/pyvenv.cfg 2>/dev/null`
	got := compactShellTokens(shellCommandTokens(command))
	want := []shellCommandToken{
		{Text: "ls", Class: shellTokenCommand},
		{Text: "-la", Class: shellTokenFlag},
		{Text: "/home/xueyongzhi/WorkDir/code/demo/.venv/bin/", Class: shellTokenPath},
		{Text: "2>", Class: shellTokenRedirect},
		{Text: "/dev/null", Class: shellTokenPath},
		{Text: "|", Class: shellTokenOperator},
		{Text: "head", Class: shellTokenCommand},
		{Text: "-20", Class: shellTokenFlag},
		{Text: ";", Class: shellTokenOperator},
		{Text: "echo", Class: shellTokenCommand},
		{Text: `"---"`, Class: shellTokenQuoted},
		{Text: ";", Class: shellTokenOperator},
		{Text: "cat", Class: shellTokenCommand},
		{Text: "/home/xueyongzhi/WorkDir/code/demo/.venv/pyvenv.cfg", Class: shellTokenPath},
		{Text: "2>", Class: shellTokenRedirect},
		{Text: "/dev/null", Class: shellTokenPath},
	}
	if len(got) != len(want) {
		t.Fatalf("token count = %d, want %d\ngot:  %#v\nwant: %#v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d = %#v, want %#v\nall: %#v", i, got[i], want[i], got)
		}
	}
}

func compactShellTokens(tokens []shellCommandToken) []shellCommandToken {
	out := make([]shellCommandToken, 0, len(tokens))
	for _, token := range tokens {
		if token.Class == shellTokenSpace {
			continue
		}
		out = append(out, token)
	}
	return out
}
