package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
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

func TestShellCommandStylesUseCatppuccinSyntaxPalette(t *testing.T) {
	tests := []struct {
		name    string
		isDark  bool
		command string
		want    string
	}{
		{name: "dark command", isDark: true, command: "git", want: "38;2;137;180;250"},
		{name: "dark keyword", isDark: true, command: "if", want: "38;2;203;166;247"},
		{name: "dark flag", isDark: true, command: "-la", want: "38;2;250;179;135"},
		{name: "dark path", isDark: true, command: "/tmp/demo", want: "38;2;137;180;250"},
		{name: "dark quoted", isDark: true, command: `"hello"`, want: "38;2;166;227;161"},
		{name: "light command", isDark: false, command: "git", want: "38;2;30;102;245"},
		{name: "light keyword", isDark: false, command: "if", want: "38;2;136;57;239"},
		{name: "light flag", isDark: false, command: "-la", want: "38;2;254;100;11"},
		{name: "light path", isDark: false, command: "/tmp/demo", want: "38;2;30;102;245"},
		{name: "light quoted", isDark: false, command: `"hello"`, want: "38;2;64;160;43"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			theme := tuikit.ResolveThemeWithState(tt.isDark, false, colorprofile.TrueColor)
			ctx := BlockRenderContext{Width: 80, TermWidth: 80, Theme: theme}
			var class shellTokenClass
			switch {
			case strings.Contains(tt.name, "keyword"):
				class = shellTokenKeyword
			case strings.Contains(tt.name, "flag"):
				class = shellTokenFlag
			case strings.Contains(tt.name, "path"):
				class = shellTokenPath
			case strings.Contains(tt.name, "quoted"):
				class = shellTokenQuoted
			default:
				class = shellTokenCommand
			}
			rendered := shellTokenStyle(ctx, class).Render(tt.command)
			if !strings.Contains(rendered, tt.want) {
				t.Fatalf("rendered %q missing Catppuccin SGR %q", fmt.Sprintf("%q", rendered), tt.want)
			}
		})
	}
}

func TestShellCommandTokensClassifyCompoundShellCommand(t *testing.T) {
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
