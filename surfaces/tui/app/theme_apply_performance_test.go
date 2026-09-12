package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

var themeApplyPreviewSink string

func themeApplyPreviewNames() []string {
	return []string{"auto", "catppuccin", "catppuccin-mocha", "nord", "dracula", "catppuccin-latte"}
}

func newThemeApplyPerformanceModel(tb testing.TB) *Model {
	tb.Helper()
	tb.Setenv("CAELIS_THEME", "auto")
	m := NewModel(Config{NoAnimation: true, ColorProfile: colorprofile.TrueColor})
	m.Update(tea.WindowSizeMsg{Width: 180, Height: 60})

	for turn := range 8 {
		m.commitUserDisplayLine(fmt.Sprintf("请排查场景 %d，并给出可执行步骤", turn+1))
		var body strings.Builder
		fmt.Fprintf(&body, "## 结论 %d 👩‍💻\n\n- 使用 `go test ./surfaces/tui/app` 验证\n- [链接](https://example.com/%d) 与 **加粗中文**\n\n", turn+1, turn+1)
		fmt.Fprintf(&body, "```go\nfunc previewTheme%d() {\n\tfmt.Println(%q)\n}\n```\n\n", turn+1, fmt.Sprintf("turn-%d", turn+1))
		m.Update(TranscriptEventsMsg{Events: []TranscriptEvent{{
			Kind: TranscriptEventNarrative, Scope: ACPProjectionMain,
			TurnID: fmt.Sprintf("turn-%d", turn+1), MessageID: fmt.Sprintf("msg-%d", turn+1),
			NarrativeKind: TranscriptNarrativeAssistant, Text: body.String(), Final: true,
		}}})
		m.Update(TranscriptEventsMsg{Events: []TranscriptEvent{{
			Kind: TranscriptEventLifecycle, Scope: ACPProjectionMain,
			TurnID: fmt.Sprintf("turn-%d", turn+1), State: "completed",
		}}})
	}

	var history strings.Builder
	for section := range 90 {
		fmt.Fprintf(&history, "## 长记录 %d 👩‍💻\n\n- 使用 `go test` 验证\n- [链接](https://example.com/%d) 与 **加粗中文**\n\n", section, section)
	}
	m.commitUserDisplayLine("继续展开完整排查记录")
	m.Update(TranscriptEventsMsg{Events: []TranscriptEvent{{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionMain,
		TurnID: "long-turn", MessageID: "history", NarrativeKind: TranscriptNarrativeAssistant,
		Text: history.String(), Final: true,
	}}})
	m.Update(TranscriptEventsMsg{Events: []TranscriptEvent{{
		Kind: TranscriptEventTool, Scope: ACPProjectionMain, TurnID: "long-turn",
		ToolCallID: "cmd-1", ToolName: surfaceToolRunCommand, ToolArgs: "go test ./surfaces/tui/app",
		ToolOutput: strings.Repeat("ok  \tgithub.com/caelis-labs/caelis/surfaces/tui/app\t1.234s\n", 24),
		Final:      true,
	}}})
	m.Update(TranscriptEventsMsg{Events: []TranscriptEvent{{
		Kind: TranscriptEventTool, Scope: ACPProjectionMain, TurnID: "long-turn",
		ToolCallID: "edit-1", ToolName: surfaceToolWrite, ToolArgs: "remote_script_test.go +2 -0",
		ToolOutput: mutationSingleFileDiff, Final: true,
	}}})
	m.Update(TranscriptEventsMsg{Events: []TranscriptEvent{{
		Kind: TranscriptEventLifecycle, Scope: ACPProjectionMain, TurnID: "long-turn", State: "completed",
	}}})

	m.markViewportStructureDirty()
	m.syncViewportContent()
	_ = m.View()
	if len(m.viewportStyledLines) < 120 {
		tb.Fatalf("theme preview fixture too small: rows=%d blocks=%d", len(m.viewportStyledLines), m.doc.Len())
	}
	return m
}

func applySelectedThemePreview(m *Model, name string) {
	m.themeName = name
	m.resolveSelectedTheme()
	themeApplyPreviewSink = m.View().Content
}

func joinedViewportPlain(m *Model) string {
	return strings.Join(m.viewportPlainLines, "\n")
}

func joinedViewportStyled(m *Model) string {
	return strings.Join(m.viewportStyledLines, "\n")
}

func TestThemeApplyPreviewKeepsContentAndRepaintsColors(t *testing.T) {
	m := newThemeApplyPerformanceModel(t)
	baselineFrame := m.View().Content
	baselineStyled := joinedViewportStyled(m)
	baselinePlain := joinedViewportPlain(m)
	if !strings.Contains(baselinePlain, "请排查场景 1") || !strings.Contains(baselinePlain, "长记录 89") || !strings.Contains(baselinePlain, "remote_script_test.go") {
		t.Fatal("fixture missing historical markdown or mutation diff")
	}

	applySelectedThemePreview(m, "nord")
	nordFrame := themeApplyPreviewSink
	nordStyled := joinedViewportStyled(m)
	if nordFrame == baselineFrame || nordStyled == baselineStyled {
		t.Fatal("nord preview kept the previous theme's colors")
	}
	if got := joinedViewportPlain(m); got != baselinePlain {
		t.Fatal("nord preview changed transcript content")
	}
	applySelectedThemePreview(m, "catppuccin-latte")
	latteFrame := themeApplyPreviewSink
	latteStyled := joinedViewportStyled(m)
	if latteFrame == nordFrame || latteFrame == baselineFrame || latteStyled == nordStyled || latteStyled == baselineStyled {
		t.Fatal("light preview reused a stale theme frame")
	}
	if got := joinedViewportPlain(m); got != baselinePlain {
		t.Fatal("light preview lost earlier turns or transcript content")
	}

	applySelectedThemePreview(m, "nord")
	if joinedViewportStyled(m) != nordStyled || joinedViewportPlain(m) != baselinePlain {
		t.Fatal("warm nord preview used stale colors or content")
	}

	applySelectedThemePreview(m, "auto")
	if themeApplyPreviewSink != baselineFrame || joinedViewportStyled(m) != baselineStyled || joinedViewportPlain(m) != baselinePlain {
		t.Fatal("restoring auto left stale colors or content")
	}
}

func BenchmarkThemeApplyPreview(b *testing.B) {
	names := themeApplyPreviewNames()
	b.Run("cold", func(b *testing.B) {
		m := newThemeApplyPerformanceModel(b)
		b.ReportAllocs()
		for index := 0; b.Loop(); index++ {
			defaultGlamourOutputCache.clear()
			applySelectedThemePreview(m, names[index%len(names)])
		}
		b.ReportMetric(float64(len(m.viewportStyledLines)), "rows")
	})
	b.Run("warm", func(b *testing.B) {
		m := newThemeApplyPerformanceModel(b)
		for _, name := range names {
			applySelectedThemePreview(m, name)
		}
		b.ReportAllocs()
		for index := 0; b.Loop(); index++ {
			applySelectedThemePreview(m, names[index%len(names)])
		}
		b.ReportMetric(float64(len(m.viewportStyledLines)), "rows")
		if !strings.Contains(ansi.Strip(themeApplyPreviewSink), "长记录 89") {
			b.Fatal("preview dropped historical markdown")
		}
	})
}

// Measure the immediate interaction frame separately from the deferred palette
// work; commands are deliberately not delivered inside this benchmark.
func BenchmarkThemePickerNavigation(b *testing.B) {
	m := newThemeApplyPerformanceModel(b)
	m.submitThemeCommand("/theme dracula")
	m.submitThemeCommand("/theme")
	_ = m.View()
	version, renders := m.viewportContentVersion, m.diag.GlamourRenderCalls
	b.ReportAllocs()
	for b.Loop() {
		m.Update(keyPress("down"))
		themeApplyPreviewSink = m.View().Content
	}
	if m.themeName != "dracula" || m.viewportContentVersion != version || m.diag.GlamourRenderCalls != renders {
		b.Fatal("navigation synchronously rebuilt the transcript")
	}
}

func BenchmarkThemePickerHover(b *testing.B) {
	m := newThemeApplyPerformanceModel(b)
	m.submitThemeCommand("/theme nord")
	m.submitThemeCommand("/theme")
	if m.themePicker == nil {
		b.Fatal("theme picker did not open")
	}
	labels := []string{"Terminal", "Catppuccin", "Catppuccin Latte", "Catppuccin Mocha", "Dracula", "Nord"}
	points := make([]tea.Mouse, 0, len(labels))
	for _, label := range labels {
		points = append(points, themePickerNamedPoint(b, m, label))
	}
	themeBefore := m.themeName
	glamourBefore := m.diag.GlamourRenderCalls
	version := m.viewportContentVersion
	b.ReportAllocs()
	for index := 0; b.Loop(); index++ {
		m.Update(tea.MouseMotionMsg(points[index%len(points)]))
		themeApplyPreviewSink = m.View().Content
	}
	if m.themeName != themeBefore || m.diag.GlamourRenderCalls != glamourBefore || m.viewportContentVersion != version {
		b.Fatal("hover rethemed or rebuilt the transcript")
	}
}
