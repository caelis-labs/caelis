package tuiapp

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestForceRendererGraphemeWidthCmdPinsUnicodeCoreReport(t *testing.T) {
	msg := forceRendererGraphemeWidthCmd()()
	report, ok := msg.(tea.ModeReportMsg)
	if !ok {
		t.Fatalf("force renderer width message = %T, want tea.ModeReportMsg", msg)
	}
	if report.Mode != ansi.ModeUnicodeCore {
		t.Fatalf("renderer width mode = %d, want Unicode core mode %d", report.Mode, ansi.ModeUnicodeCore)
	}
	if report.Value != ansi.ModeReset {
		t.Fatalf("renderer width setting = %d, want settable reset state %d", report.Value, ansi.ModeReset)
	}
}

func TestStreamAppendPhysicalScreenPreservesTextAfterComplexGrapheme(t *testing.T) {
	const (
		width  = 48
		height = 14
	)
	started := make(chan struct{}, 1)
	model := NewModel(Config{
		NoColor:            true,
		NoAnimation:        true,
		StreamTickInterval: 16 * time.Millisecond,
		OnStart: func() {
			select {
			case started <- struct{}{}:
			default:
			}
		},
	})
	terminal := vt.NewSafeEmulator(width, height)
	t.Cleanup(func() { _ = terminal.Close() })
	// Match multiplexers that render extended graphemes as two cells but do
	// not answer Bubble Tea's DEC mode queries.
	terminal.RegisterCsiHandler(ansi.Command('?', '$', 'p'), func(ansi.Params) bool {
		return true
	})
	// Avoid opening an input pipe solely for auto-theme color-query replies.
	terminal.RegisterOscHandler(10, func([]byte) bool { return true })
	terminal.RegisterOscHandler(11, func([]byte) bool { return true })
	terminal.RegisterOscHandler(12, func([]byte) bool { return true })
	program := tea.NewProgram(
		model,
		tea.WithInput(nil),
		tea.WithOutput(terminal),
		tea.WithWindowSize(width, height),
		tea.WithFPS(120),
		tea.WithoutSignalHandler(),
	)

	var (
		finalModel tea.Model
		runErr     error
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		finalModel, runErr = program.Run()
	}()
	t.Cleanup(func() {
		program.Quit()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			program.Kill()
		}
	})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("TUI program did not run OnStart")
	}

	program.Send(schedulerACPAssistantEnvelope("A🏳️‍🌈B"))
	waitForPhysicalStreamLine(t, terminal, "A🏳️‍🌈B")
	program.Send(schedulerACPAssistantEnvelope("C"))
	waitForPhysicalStreamLine(t, terminal, "A🏳️‍🌈BC")

	program.Quit()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		program.Kill()
		t.Fatal("TUI program did not stop")
	}
	if runErr != nil {
		t.Fatalf("run TUI program: %v", runErr)
	}
	model, ok := finalModel.(*Model)
	if !ok {
		t.Fatalf("final model = %T, want *Model", finalModel)
	}
	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 || block.Events[0].Text != "A🏳️‍🌈BC" {
		t.Fatalf("final assistant events = %#v, want complete stream text", block.Events)
	}
	if plain := ansi.Strip(model.View().Content); !strings.Contains(plain, "A🏳️‍🌈BC") {
		t.Fatalf("final model view lost stream text: %q", plain)
	}
}

func TestWideTranscriptLineMoveRepaintsInsteadOfHardScroll(t *testing.T) {
	const (
		width  = 96
		height = 12
	)
	before, after := normalizedTranscriptScrollFramesForTest(
		width,
		height,
		"  › 复现一下当时 Grep 返回 0 的调用方式，确认是工具问题还是参数用法问题。",
	)
	updates := renderFullscreenFramesForTest(t, width, height, before, after)
	second := updates[1]
	if !strings.Contains(second, "复现一下当时 Grep") {
		t.Fatalf("wide transcript line was moved without repainting its text: %q", second)
	}
	if strings.Contains(second, ansi.SetTopBottomMargins(1, 9)) {
		t.Fatalf("wide transcript line was moved through a hard-scroll region: %q", second)
	}
}

func TestWideTranscriptTwoLineMoveRepaintsInsteadOfHardScroll(t *testing.T) {
	const (
		width  = 96
		height = 12
	)
	before, after := normalizedTranscriptScrollFramesWithToolsForTest(
		width,
		height,
		"  › 复现一下当时 Grep 返回 0 的调用方式，确认是工具问题还是参数用法问题。",
		"  • Read overview.md",
		`  • Search "service_tag|resource_type|MonitorCloudInstance" in overview`,
	)
	updates := renderFullscreenFramesForTest(t, width, height, before, after)
	second := updates[1]
	if !strings.Contains(second, "复现一下当时 Grep") {
		t.Fatalf("wide transcript moved by two rows without repainting its text: %q", second)
	}
	if strings.Contains(second, ansi.SetTopBottomMargins(1, 9)) {
		t.Fatalf("wide transcript moved by two rows through a hard-scroll region: %q", second)
	}
}

func TestLiveExplorationScrollbarRepaintsWideReasoning(t *testing.T) {
	const (
		width     = 96
		height    = 20
		reasoning = "复现一下当时 Grep 返回 0 的调用方式，确认是工具问题还是参数用法问题。"
		search    = `"service_tag|resource_type|MonitorCloudInstance" in overview, "service"`
	)
	model := NewModel(Config{NoColor: false, NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	model = updated.(*Model)
	for range 60 {
		model.doc.Append(NewUserNarrativeBlock("historical line that keeps the viewport pinned at the tail"))
	}

	block := NewMainACPTurnBlock("turn-renderer-repro")
	block.AppendStreamEvent(
		SEReasoning,
		"先读取相关文件并定位 Grep 的实现。",
		newNarrativeSourceIdentity("reasoning-old", "event-old", "projection-old"),
	)
	block.UpdateToolWithMeta("read-1", "Read", "overview.md", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	block.UpdateToolWithMeta("list-1", "List", "overview", "", true, false, ToolUpdateMeta{ToolKind: "search"})
	block.AppendStreamEvent(
		SEReasoning,
		reasoning,
		newNarrativeSourceIdentity("reasoning-current", "event-current", "projection-current"),
	)
	model.doc.Append(block)
	model.syncViewportContent()
	model.viewportScrollbarVisibleUntil = time.Now().Add(time.Hour)
	scrollRegionBottom := model.viewport.Height()
	before := model.View().Content
	if plain := ansi.Strip(before); !strings.Contains(plain, "Explored") ||
		!strings.Contains(plain, reasoning) ||
		(!strings.Contains(plain, "▏") && !strings.Contains(plain, "▎")) {
		t.Fatalf("setup frame missing stable exploration or reasoning: %q", plain)
	}

	block.UpdateToolWithMeta("grep-1", "Grep", search, "", false, false, ToolUpdateMeta{ToolKind: "search"})
	model.markViewportBlockDirty(block.BlockID())
	model.syncViewportContent()
	model.viewportScrollbarVisibleUntil = time.Now().Add(time.Hour)
	after := model.View().Content
	plain := ansi.Strip(after)
	if !strings.Contains(plain, reasoning) || !strings.Contains(plain, "Search "+search) {
		t.Fatalf("live exploration frame missing expected rows: %q", plain)
	}

	updates := renderFullscreenFramesForTest(t, width, height, before, after)
	second := updates[1]
	if !strings.Contains(second, reasoning) {
		t.Fatalf("live exploration moved wide reasoning without repainting it: %q", second)
	}
	if strings.Contains(second, ansi.SetTopBottomMargins(1, scrollRegionBottom)) {
		t.Fatalf("live exploration moved wide reasoning through a hard-scroll region: %q", second)
	}
}

func TestASCIITranscriptLineMoveKeepsHardScrollOptimization(t *testing.T) {
	const (
		width  = 96
		height = 12
	)
	before, after := normalizedTranscriptScrollFramesForTest(
		width,
		height,
		"  > reproduce the Grep call that returned zero",
	)
	updates := renderFullscreenFramesForTest(t, width, height, before, after)
	if second := updates[1]; !strings.Contains(second, ansi.SetTopBottomMargins(1, 9)) {
		t.Fatalf("ASCII-only frame lost hard-scroll optimization: %q", second)
	}
}

func normalizedTranscriptScrollFramesForTest(width int, height int, reasoning string) (string, string) {
	return normalizedTranscriptScrollFramesWithToolsForTest(
		width,
		height,
		reasoning,
		`  • Search "service_tag|resource_type|MonitorCloudInstance" in overview`,
	)
}

func normalizedTranscriptScrollFramesWithToolsForTest(
	width int,
	height int,
	reasoning string,
	toolLines ...string,
) (string, string) {
	beforeLines := []string{
		"history 0",
		"history 1",
		"history 2",
		"history 3",
		"history 4",
		"• Explored",
		reasoning,
		"",
		"",
		"> ",
		"",
		"status",
	}
	if len(toolLines) > 5 {
		panic("test helper only supports up to five inserted tool lines")
	}
	afterLines := append([]string(nil), beforeLines[len(toolLines):7]...)
	afterLines = append(afterLines, toolLines...)
	afterLines = append(afterLines, beforeLines[7:]...)
	return normalizeFullscreenFrame(strings.Join(beforeLines, "\n"), width, height),
		normalizeFullscreenFrame(strings.Join(afterLines, "\n"), width, height)
}

func waitForPhysicalStreamLine(t *testing.T, terminal *vt.SafeEmulator, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		screen := ansi.Strip(terminal.Render())
		for _, line := range strings.Split(screen, "\n") {
			if strings.Contains(line, want) {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("physical terminal never rendered complete stream text %q; screen=%q", want, ansi.Strip(terminal.Render()))
}

func TestComposerMixedWidthDeleteAvoidsRendererDCHInsideWideGlyph(t *testing.T) {
	model := NewModel(Config{})
	model.width = 24
	model.textarea.SetValue("甲a乙b丙c丁d")
	model.moveTextareaCursorToIndex(len([]rune("甲a乙b")))
	model.syncInputFromTextarea()
	before := model.renderInputBar()

	updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	m := updated.(*Model)
	after := m.renderInputBar()

	if got := m.textarea.Value(); got != "甲a乙丙c丁d" {
		t.Fatalf("textarea value = %q, want 甲a乙丙c丁d", got)
	}
	outputs := renderComposerFramesForTest(t, model.fixedRowWidth(), before, after)
	second := outputs[1]
	if stringsContainDeleteCharacter(second) {
		t.Fatalf("renderer update = %q, must not delete inside shifted CJK glyph", second)
	}
	if !bytes.Contains([]byte(second), []byte("丙c丁d")) {
		t.Fatalf("renderer update = %q, want shifted CJK tail to be repainted", second)
	}
}

func TestWideCellRepaintSentinelDoesNotEmitHyperlink(t *testing.T) {
	got := protectWideCellRepaintLine("甲", 4)
	if strings.Contains(got, "\x1b]8;;") {
		t.Fatalf("wide-cell sentinel emitted OSC 8 hyperlink: %q", got)
	}
	if strings.Contains(got, "caelis://") {
		t.Fatalf("wide-cell sentinel emitted URI: %q", got)
	}
	if got == ansi.Strip(got) {
		t.Fatalf("wide-cell sentinel should keep an ANSI guard, got plain text %q", got)
	}
	if stripped := ansi.Strip(got); displayColumns(stripped) != 4 {
		t.Fatalf("stripped width = %d, want 4; stripped=%q raw=%q", displayColumns(stripped), stripped, got)
	}
}

func TestWideFrameHardScrollGuardBindsHeightPaddingRows(t *testing.T) {
	const (
		width  = 6
		height = 3
	)
	lines := strings.Split(normalizeFullscreenFrame("甲", width, height), "\n")
	if len(lines) != height {
		t.Fatalf("normalized lines = %d, want %d: %#v", len(lines), height, lines)
	}
	for row, line := range lines {
		if got := displayColumns(ansi.Strip(line)); got != width {
			t.Fatalf("row %d width = %d, want %d: %q", row, got, width, line)
		}
		if !strings.Contains(line, hardScrollRowSentinel(row)) {
			t.Fatalf("row %d missing its distinct physical-row identity: %q", row, line)
		}
		for other := range lines {
			if other != row && strings.Contains(line, hardScrollRowSentinel(other)) {
				t.Fatalf("row %d reused row %d identity: %q", row, other, line)
			}
		}
	}
}

func TestHardScrollIdentityColumnUsesFramePadding(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		width      int
		wantColumn int
		wantOK     bool
	}{
		{name: "trailing padding", line: "abcd  ", width: 6, wantColumn: 5, wantOK: true},
		{name: "scrollbar gutter", line: "abcd ▏", width: 6, wantColumn: 4, wantOK: true},
		{name: "scrollbar thumb gutter", line: "abcd ▎", width: 6, wantColumn: 4, wantOK: true},
		{name: "semantic penultimate space", line: "abcd X", width: 6},
		{name: "full content", line: "abcdef", width: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			column, ok := hardScrollIdentityColumn(tt.line, tt.width)
			if column != tt.wantColumn || ok != tt.wantOK {
				t.Fatalf("identity column = (%d, %v), want (%d, %v)", column, ok, tt.wantColumn, tt.wantOK)
			}
		})
	}
}

func TestNormalizeFullscreenFrameLineFitsDisplayWidth(t *testing.T) {
	tests := []struct {
		name             string
		input            string
		width            int
		wantPlain        string
		wantANSI         bool
		forbiddenPlain   string
		wantVisibleGuard bool
	}{
		{
			name:      "narrow_ascii_pads_without_guard",
			input:     "abc",
			width:     10,
			wantPlain: "abc       ",
		},
		{
			name:             "narrow_wide_cell_keeps_repaint_guard",
			input:            "甲",
			width:            4,
			wantPlain:        "甲  ",
			wantANSI:         true,
			wantVisibleGuard: true,
		},
		{
			name:             "overwide_styled_cjk_truncates_before_padding",
			input:            "\x1b[31m甲乙丙\x1b[0m",
			width:            5,
			wantPlain:        "甲乙 ",
			wantANSI:         true,
			forbiddenPlain:   "丙",
			wantVisibleGuard: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeFullscreenFrameLine(tt.input, tt.width)
			stripped := ansi.Strip(got)
			if !utf8.ValidString(stripped) {
				t.Fatalf("normalized line is invalid UTF-8: %q", stripped)
			}
			if stripped != tt.wantPlain {
				t.Fatalf("plain line = %q, want %q; raw=%q", stripped, tt.wantPlain, got)
			}
			if width := displayColumns(stripped); width != tt.width {
				t.Fatalf("normalized width = %d, want %d; stripped=%q raw=%q", width, tt.width, stripped, got)
			}
			if tt.forbiddenPlain != "" && strings.Contains(stripped, tt.forbiddenPlain) {
				t.Fatalf("normalized line kept forbidden text %q: stripped=%q raw=%q", tt.forbiddenPlain, stripped, got)
			}
			if hasANSI := got != stripped; hasANSI != tt.wantANSI {
				t.Fatalf("ANSI presence = %v, want %v; stripped=%q raw=%q", hasANSI, tt.wantANSI, stripped, got)
			}
			if tt.wantVisibleGuard && !strings.Contains(got, "\x1b[8m \x1b[28m") {
				t.Fatalf("normalized line missing wide-cell repaint guard: stripped=%q raw=%q", stripped, got)
			}
		})
	}
}

func renderComposerFramesForTest(t *testing.T, width int, frames ...string) []string {
	t.Helper()
	var buf bytes.Buffer
	renderer := uv.NewTerminalRenderer(&buf, []string{"TERM=xterm-256color", "TTY_FORCE=1"})
	renderer.SetRelativeCursor(true)

	height := 1
	for _, frame := range frames {
		if lines := strings.Count(frame, "\n") + 1; lines > height {
			height = lines
		}
	}
	screen := uv.NewScreenBuffer(width, height)
	outputs := make([]string, 0, len(frames))
	for idx, frame := range frames {
		screen.Clear()
		uv.NewStyledString(frame).Draw(screen, screen.Bounds())
		renderer.Render(screen.RenderBuffer)
		if err := renderer.Flush(); err != nil {
			t.Fatalf("flush frame %d: %v", idx, err)
		}
		outputs = append(outputs, buf.String())
		buf.Reset()
	}
	return outputs
}

func renderFullscreenFramesForTest(t *testing.T, width int, height int, frames ...string) []string {
	t.Helper()
	var buf bytes.Buffer
	renderer := uv.NewTerminalRenderer(&buf, []string{"TERM=xterm-256color", "TTY_FORCE=1"})
	renderer.SetFullscreen(true)
	renderer.SetScrollOptim(true)

	screen := uv.NewScreenBuffer(width, height)
	screen.Method = ansi.GraphemeWidth
	outputs := make([]string, 0, len(frames))
	for idx, frame := range frames {
		screen.Clear()
		uv.NewStyledString(frame).Draw(screen, screen.Bounds())
		renderer.Render(screen.RenderBuffer)
		if err := renderer.Flush(); err != nil {
			t.Fatalf("flush fullscreen frame %d: %v", idx, err)
		}
		outputs = append(outputs, buf.String())
		buf.Reset()
	}
	return outputs
}

func stringsContainDeleteCharacter(text string) bool {
	return bytes.Contains([]byte(text), []byte(ansi.DeleteCharacter(1))) ||
		bytes.Contains([]byte(text), []byte("\x1b[1P"))
}
