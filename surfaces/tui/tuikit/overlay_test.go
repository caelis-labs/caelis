package tuikit

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestRenderOverlayFrame_BasicContent(t *testing.T) {
	theme := DefaultTheme()
	frame := RenderOverlayFrame(theme, OverlayFrameModel{
		Title: "Select Model",
		Body:  []string{"option 1", "option 2"},
		Width: 40,
	})
	plain := ansi.Strip(frame)
	if !strings.Contains(plain, "Select Model") {
		t.Fatalf("expected title, got %q", plain)
	}
	if !strings.Contains(plain, "option 1") || !strings.Contains(plain, "option 2") {
		t.Fatalf("expected body content, got %q", plain)
	}
	if !strings.Contains(plain, "╭") {
		t.Fatalf("expected rounded border, got %q", plain)
	}
}

func TestRenderResponsiveOverlayFrame_Borderless(t *testing.T) {
	theme := DefaultTheme()
	frame := RenderResponsiveOverlayFrame(theme, ResponsiveOverlayFrameModel{
		Body:      []string{"option 1", "option 2"},
		Width:     40,
		UseBorder: false,
	})
	plain := ansi.Strip(frame)
	if strings.ContainsAny(plain, "╭╮╰╯┌┐└┘│─") {
		t.Fatalf("borderless overlay should not render chrome, got %q", plain)
	}
	if !strings.Contains(plain, "option 1") || !strings.Contains(plain, "option 2") {
		t.Fatalf("expected body content, got %q", plain)
	}
}

func TestRenderOverlayFrame_NoTitle(t *testing.T) {
	theme := DefaultTheme()
	frame := RenderOverlayFrame(theme, OverlayFrameModel{
		Body:  []string{"single line"},
		Width: 30,
	})
	plain := ansi.Strip(frame)
	if !strings.Contains(plain, "single line") {
		t.Fatalf("expected body, got %q", plain)
	}
}

func TestRenderOverlayCompletion_EmptyItems(t *testing.T) {
	theme := DefaultTheme()
	got := RenderOverlayCompletion(theme, OverlayCompletionModel{
		Title: "Test",
		Items: nil,
		Width: 40,
	})
	if got != "" {
		t.Fatalf("expected empty for zero items, got %q", got)
	}
}

func TestRenderOverlayCompletion_SelectedHighlight(t *testing.T) {
	theme := DefaultTheme()
	got := RenderOverlayCompletion(theme, OverlayCompletionModel{
		Title: "Models",
		Items: []OverlayCompletionItem{
			{Label: "gpt-4"},
			{Label: "claude-4-opus", Desc: "recommended"},
			{Label: "gemini-2"},
		},
		Index: 1,
		Width: 50,
	})
	plain := ansi.Strip(got)
	if !strings.Contains(plain, "Models") {
		t.Fatalf("expected title, got %q", plain)
	}
	if !strings.Contains(plain, "claude-4-opus") {
		t.Fatalf("expected selected item, got %q", plain)
	}
	if !strings.Contains(plain, "▸") {
		t.Fatalf("expected selection indicator, got %q", plain)
	}
}

func TestRenderOverlayCompletion_SelectedHighlightAvoidsFocusAccent(t *testing.T) {
	theme := DefaultTheme()
	theme.Focus = lipgloss.Color("#123456")
	theme.PromptFg = theme.Focus
	theme.InvalidateTokens()
	got := RenderOverlayCompletion(theme, OverlayCompletionModel{
		Title: "Models",
		Items: []OverlayCompletionItem{
			{Label: "gpt-4"},
			{Label: "claude-4-opus", Desc: "recommended"},
		},
		Index: 1,
		Width: 50,
	})
	if strings.Contains(got, "38;2;18;52;86") {
		t.Fatalf("selected overlay completion still uses focus accent: %q", got)
	}
}

func TestRenderOverlayCompletion_ScrollIndicators(t *testing.T) {
	theme := DefaultTheme()
	items := make([]OverlayCompletionItem, 20)
	for i := range items {
		items[i] = OverlayCompletionItem{Label: strings.Repeat("item", 1)}
	}
	got := RenderOverlayCompletion(theme, OverlayCompletionModel{
		Title:   "Long List",
		Items:   items,
		Index:   10,
		Width:   40,
		MaxShow: 5,
	})
	plain := ansi.Strip(got)
	if !strings.Contains(plain, "↑ more") {
		t.Fatalf("expected scroll up indicator, got %q", plain)
	}
	if !strings.Contains(plain, "↓ more") {
		t.Fatalf("expected scroll down indicator, got %q", plain)
	}
}

func TestOverlayCenter_PlacesInMiddle(t *testing.T) {
	// 10x10 screen
	base := strings.Repeat(strings.Repeat(" ", 10)+"\n", 9) + strings.Repeat(" ", 10)
	overlay := "hello"

	result := OverlayCenter(base, overlay, 10, 10)
	lines := strings.Split(result, "\n")
	if len(lines) < 10 {
		t.Fatalf("expected 10 lines, got %d", len(lines))
	}
	// The overlay should be roughly in the middle (row ~4-5)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "hello") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("overlay content not found in result")
	}
}

func TestOverlayCenterPreservesBaseOutsideOpaqueRectangle(t *testing.T) {
	const (
		width  = 24
		height = 5
	)
	baseLine := "abcdefghijklmnopqrstuvwx"
	base := strings.Repeat(baseLine+"\n", height-1) + baseLine
	overlay := strings.Join([]string{
		"╭──────╮",
		"│ card",
		"╰──────╯",
	}, "\n")

	result := ansi.Strip(OverlayCenter(base, overlay, width, height))
	lines := strings.Split(result, "\n")
	overlayWidth := lipgloss.Width("╭──────╮")
	startX := (width - overlayWidth) / 2
	for row, overlayLine := range strings.Split(overlay, "\n") {
		screenRow := 1 + row
		opaqueLine := overlayLine + strings.Repeat(" ", overlayWidth-lipgloss.Width(overlayLine))
		want := baseLine[:startX] + opaqueLine + baseLine[startX+overlayWidth:]
		if lines[screenRow] != want {
			t.Fatalf("composited row %d = %q, want %q", screenRow, lines[screenRow], want)
		}
	}
	if lines[0] != baseLine || lines[4] != baseLine {
		t.Fatalf("rows outside overlay changed: %#v", lines)
	}
}

func TestOverlayCenterPreservesANSIStylesAndWideCellsAtBothSides(t *testing.T) {
	const width = 30
	baseForeground := lipgloss.Color("#d06c75")
	overlayBackground := lipgloss.Color("#20283a")
	baseLine := lipgloss.NewStyle().Foreground(baseForeground).Render("左侧文字 keep colors 右侧文字 remain")
	overlayLine := lipgloss.NewStyle().Background(overlayBackground).Width(12).Render("浮层")

	result := OverlayCenter(baseLine, overlayLine, width, 1)
	screen := uv.NewScreenBuffer(width, 1)
	screen.Method = ansi.GraphemeWidth
	uv.NewStyledString(result).Draw(screen, screen.Bounds())
	startX := (width - lipgloss.Width(overlayLine)) / 2
	for _, x := range []int{0, width - 1} {
		cell := screen.CellAt(x, 0)
		if cell == nil || !sameColor(cell.Style.Fg, baseForeground) {
			t.Fatalf("base foreground at x=%d = %v, want %v: %q", x, cell, baseForeground, result)
		}
	}
	for x := startX; x < startX+lipgloss.Width(overlayLine); x++ {
		cell := screen.CellAt(x, 0)
		if cell != nil && cell.Width == 0 {
			continue
		}
		if cell == nil || !sameColor(cell.Style.Bg, overlayBackground) {
			t.Fatalf("overlay background at x=%d = %v, want %v: %q", x, cell, overlayBackground, result)
		}
	}
	plain := ansi.Strip(result)
	left := ansi.Cut(plain, 0, startX)
	right := ansi.Cut(plain, startX+lipgloss.Width(overlayLine), width)
	if !strings.Contains(left, "左侧") || !strings.Contains(right, "右侧") {
		t.Fatalf("wide base text did not survive outside overlay: %q", plain)
	}
}

func TestRenderResponsiveOverlayFramePaintsCompleteSurface(t *testing.T) {
	theme := DefaultTheme()
	theme.ModalBg = lipgloss.Color("#20283a")
	theme.InvalidateTokens()
	frame := RenderResponsiveOverlayFrame(theme, ResponsiveOverlayFrameModel{
		Body: []string{
			theme.TitleStyle().Render("Subagents"),
			"",
			theme.MutedTextStyle().Render("Done  1"),
		},
		Width:     32,
		UseBorder: true,
	})

	frameWidth := lipgloss.Width(frame)
	frameHeight := strings.Count(frame, "\n") + 1
	screen := uv.NewScreenBuffer(frameWidth, frameHeight)
	uv.NewStyledString(frame).Draw(screen, screen.Bounds())
	for y := 0; y < frameHeight; y++ {
		for x := 0; x < frameWidth; x++ {
			cell := screen.CellAt(x, y)
			if cell == nil || !sameColor(cell.Style.Bg, theme.ModalBg) {
				t.Fatalf("overlay surface cell (%d,%d) background = %v, want %v", x, y, cell, theme.ModalBg)
			}
		}
	}
}

var overlayCenterBenchmarkSink string

func BenchmarkOverlayCenterStreaming(b *testing.B) {
	const (
		width  = 180
		height = 24
	)
	theme := DefaultTheme()
	theme.ModalBg = lipgloss.Color("#20283a")
	theme.InvalidateTokens()
	baseStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#d06c75")).Width(width)
	baseRows := make([]string, height+1)
	for index := range baseRows {
		marker := "streaming transcript ANSI 中文 emoji 🧭 row "
		if index%2 == 0 {
			marker = "updated transcript colors 保持清晰 row "
		}
		baseRows[index] = baseStyle.Render(ansi.Truncate(marker+strings.Repeat("0123456789", 20), width, ""))
	}
	bases := []string{
		strings.Join(baseRows[:height], "\n"),
		strings.Join(baseRows[1:], "\n"),
	}
	overlayBody := make([]string, 0, 14)
	overlayBody = append(overlayBody, theme.TitleStyle().Render("Subagents"), "")
	for range 10 {
		overlayBody = append(overlayBody, theme.TextStyle().Render("  • overlay-ux-review  [orbit]  inspect floating composition"))
	}
	overlayBody = append(overlayBody, "", theme.HelpHintTextStyle().Render("↑↓ select  Enter open  Esc close"))
	overlay := RenderResponsiveOverlayFrame(theme, ResponsiveOverlayFrameModel{
		Body:      overlayBody,
		Width:     120,
		UseBorder: true,
	})

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		overlayCenterBenchmarkSink = OverlayCenter(bases[index%len(bases)], overlay, width, height)
	}
}

func sameColor(left color.Color, right color.Color) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftR, leftG, leftB, leftA := left.RGBA()
	rightR, rightG, rightB, rightA := right.RGBA()
	return leftR == rightR && leftG == rightG && leftB == rightB && leftA == rightA
}
