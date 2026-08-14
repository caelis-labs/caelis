package tuikit

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderPanelHeader(t *testing.T) {
	theme := DefaultTheme()
	header := RenderPanelHeader(theme, 60, PanelHeaderModel{
		Expanded: true,
		Kind:     "spawn",
		Title:    "helper",
		State:    "running",
		Meta:     "3s",
	})
	plain := ansi.Strip(header)
	if !strings.Contains(plain, "SPAWN") || !strings.Contains(plain, "helper") || !strings.Contains(plain, "running") {
		t.Fatalf("expected semantic header content, got %q", plain)
	}
}

func TestRenderPanelShellDrawer(t *testing.T) {
	theme := DefaultTheme()
	lines := RenderPanelShell(theme, PanelShellModel{
		Variant: PanelShellVariantDrawer,
		Width:   40,
		Header:  "HEADER",
		Body:    []string{"line one", "line two"},
		Footer:  "FOOTER",
	})
	joined := strings.Join(lines, "\n")
	plain := ansi.Strip(joined)
	if !strings.Contains(plain, "HEADER") || !strings.Contains(plain, "line one") || !strings.Contains(plain, "FOOTER") {
		t.Fatalf("expected drawer shell content, got %q", plain)
	}
	if !strings.Contains(plain, "╭") || !strings.Contains(plain, "╰") {
		t.Fatalf("expected boxed shell, got %q", plain)
	}
}

func TestRenderToolLineMatchesLineStyleTool(t *testing.T) {
	theme := DefaultTheme()
	got := RenderToolLine(theme, ToolLineModel{
		Prefix: "▸",
		Name:   "FINDING",
		Suffix: "/tmp/demo",
		Style:  LineStyleTool,
	})
	want := ColorizeLogLine("▸ FINDING /tmp/demo", LineStyleTool, theme)
	if got != want {
		t.Fatalf("expected shared tool-line styling\n got: %q\nwant: %q", got, want)
	}
}
