package tuikit

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderBlockShell_CollapsedRail(t *testing.T) {
	theme := DefaultTheme()
	lines := RenderBlockShell(theme, BlockShellModel{
		Variant:  BlockShellRail,
		Width:    60,
		Expanded: false,
		Kind:     "BASH",
		Title:    "ls -la",
		State:    "completed",
	})
	if len(lines) != 1 {
		t.Fatalf("collapsed rail should produce 1 line, got %d", len(lines))
	}
	plain := ansi.Strip(lines[0])
	if !strings.Contains(plain, "▸") {
		t.Fatalf("collapsed should show ▸, got %q", plain)
	}
	if !strings.Contains(plain, "BASH") {
		t.Fatalf("expected kind, got %q", plain)
	}
}

func TestRenderBlockShell_ExpandedRail(t *testing.T) {
	theme := DefaultTheme()
	lines := RenderBlockShell(theme, BlockShellModel{
		Variant:  BlockShellRail,
		Width:    60,
		Expanded: true,
		Kind:     "SPAWN",
		Title:    "helper",
		State:    "running",
		Elapsed:  3 * time.Second,
		Body:     []string{"line 1", "line 2", "line 3"},
	})
	if len(lines) < 5 { // header + 3 body + footer
		t.Fatalf("expected at least 5 lines, got %d", len(lines))
	}
	// Check rail
	for i := 1; i < len(lines)-1; i++ {
		plain := ansi.Strip(lines[i])
		if !strings.HasPrefix(plain, "│ ") {
			t.Fatalf("body line %d should have rail prefix, got %q", i, plain)
		}
	}
	// Check footer
	lastPlain := ansi.Strip(lines[len(lines)-1])
	if !strings.HasPrefix(lastPlain, "╰") {
		t.Fatalf("expected footer with ╰, got %q", lastPlain)
	}
}

func TestRenderBlockShell_ExpandedBox(t *testing.T) {
	theme := DefaultTheme()
	lines := RenderBlockShell(theme, BlockShellModel{
		Variant:  BlockShellBox,
		Width:    50,
		Expanded: true,
		Kind:     "DIFF",
		Title:    "main.go",
		Body:     []string{"+added", "-removed"},
	})
	joined := strings.Join(lines, "\n")
	plain := ansi.Strip(joined)
	if !strings.Contains(plain, "╭") || !strings.Contains(plain, "╰") {
		t.Fatalf("expected rounded box borders, got %q", plain)
	}
	if !strings.Contains(plain, "DIFF") {
		t.Fatalf("expected kind in header, got %q", plain)
	}
}

func TestRenderBlockShell_NoneVariant(t *testing.T) {
	theme := DefaultTheme()
	lines := RenderBlockShell(theme, BlockShellModel{
		Variant:  BlockShellNone,
		Width:    40,
		Expanded: true,
		Kind:     "INFO",
		Title:    "test",
		Body:     []string{"content"},
		Footer:   "footer text",
	})
	// Should have: header, content, footer = 3 lines
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines for none variant, got %d", len(lines))
	}
}

func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "<1ms"},
		{500 * time.Millisecond, "500ms"},
		{1500 * time.Millisecond, "1.5s"},
		{60 * time.Second, "1m"},
		{90 * time.Second, "1m30s"},
	}
	for _, tt := range tests {
		got := formatElapsed(tt.d)
		if got != tt.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
