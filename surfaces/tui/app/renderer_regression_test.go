package tuiapp

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/charmbracelet/colorprofile"
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

	envelope := func(text string, final bool) eventstream.Envelope {
		msg := schedulerACPAssistantEnvelope(text)
		msg.Final = final
		update := msg.Update.(eventstream.ContentChunk)
		update.MessageID = "physical-markdown-stream"
		msg.Update = update
		return msg
	}
	const finalMarkdown = "**A🏳️‍🌈BC** [链接](https://example.com)"
	program.Send(envelope("**A🏳️‍🌈B", false))
	waitForPhysicalStreamLine(t, terminal, "**A🏳️‍🌈B")
	program.Send(envelope("C** [链接](https://example.com)", false))
	waitForPhysicalStreamLine(t, terminal, finalMarkdown)
	program.Send(envelope(finalMarkdown, true))
	if _, err := waitForProductScreen(t.Context(), terminal, func(screen string) bool {
		return strings.Contains(screen, "A🏳️‍🌈BC") &&
			strings.Contains(screen, "链接") &&
			!strings.Contains(screen, "**A") &&
			!strings.Contains(screen, "[链接](")
	}); err != nil {
		t.Fatalf("physical terminal did not converge from raw tail to Glamour: %v", err)
	}

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
	if len(block.Events) != 1 || block.Events[0].Text != finalMarkdown {
		t.Fatalf("final assistant events = %#v, want complete stream text", block.Events)
	}
	if buffer := block.Events[0].ActiveBuffer; buffer == nil || buffer.HasTail() {
		t.Fatalf("final assistant buffer = %#v, want sealed Glamour source", buffer)
	}
	for _, row := range block.Render(model.blockRenderContext(width)) {
		if row.activeTail {
			t.Fatalf("physical final render retained raw active tail: %#v", row)
		}
	}
	if plain := ansi.Strip(model.View().Content); !strings.Contains(plain, "A🏳️‍🌈BC") || strings.Contains(plain, "**A") {
		t.Fatalf("final model view lost stream text: %q", plain)
	}
}

func TestWideTranscriptLineMoveUsesHardScrollWithoutLosingText(t *testing.T) {
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
	if !strings.Contains(second, ansi.SetTopBottomMargins(1, 9)) {
		t.Fatalf("wide transcript line did not use hard-scroll optimization: %q", second)
	}
	assertPhysicalFullscreenFrame(t, width, height, after, updates)
}

func TestWideTranscriptTwoLineMoveUsesHardScrollWithoutLosingText(t *testing.T) {
	const (
		width  = 96
		height = 12
	)
	before, after := normalizedTranscriptScrollFramesWithToolsForTest(
		width,
		height,
		"  › 复现一下当时 Grep 返回 0 的调用方式，确认是工具问题还是参数用法问题。",
		"  • Read overview.md",
		`  • Search service_tag|resource_type|MonitorCloudInstance in overview`,
	)
	updates := renderFullscreenFramesForTest(t, width, height, before, after)
	second := updates[1]
	if !strings.Contains(second, ansi.SetTopBottomMargins(1, 9)) {
		t.Fatalf("wide transcript two-line move did not use hard-scroll optimization: %q", second)
	}
	assertPhysicalFullscreenFrame(t, width, height, after, updates)
}

func TestLiveExplorationScrollbarHardScrollPreservesWideReasoning(t *testing.T) {
	const (
		width     = 96
		height    = 20
		reasoning = "复现一下当时 Grep 返回 0 的调用方式，确认是工具问题还是参数用法问题。"
		search    = `service_tag|resource_type|MonitorCloudInstance in overview, service`
	)
	tests := []struct {
		name         string
		profile      colorprofile.Profile
		marginBottom int
	}{
		{name: "ANSI", profile: colorprofile.ANSI, marginBottom: height - 2},
		{name: "ANSI256", profile: colorprofile.ANSI256, marginBottom: height - 3},
		{name: "TrueColor", profile: colorprofile.TrueColor, marginBottom: height - 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := NewModel(Config{NoColor: false, NoAnimation: true, ColorProfile: tt.profile})
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
			block.UpdateToolWithMeta("glob-1", "Glob", "overview", "", true, false, ToolUpdateMeta{ToolKind: "search"})
			block.AppendStreamEvent(
				SEReasoning,
				reasoning,
				newNarrativeSourceIdentity("reasoning-current", "event-current", "projection-current"),
			)
			model.doc.Append(block)
			model.syncViewportContent()
			model.viewportScrollbarVisibleUntil = time.Now().Add(time.Hour)
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
			if !strings.Contains(second, ansi.SetTopBottomMargins(1, tt.marginBottom)) {
				t.Fatalf("live exploration did not use hard-scroll optimization: %q", second)
			}
			assertPhysicalFullscreenFrame(t, width, height, after, updates)
		})
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

func TestStableWideTranscriptLineMoveKeepsHardScrollOptimization(t *testing.T) {
	const (
		width  = 96
		height = 12
	)
	const reasoning = "  › 稳定会话中的中文 Markdown 应当随视口快速滚动。"
	before, after := normalizedTranscriptScrollFramesForTest(
		width,
		height,
		reasoning,
	)
	updates := renderFullscreenFramesForTest(t, width, height, before, after)
	second := updates[1]
	if !strings.Contains(second, ansi.SetTopBottomMargins(1, 9)) {
		t.Fatalf("stable wide transcript lost hard-scroll optimization: %q", second)
	}
	t.Logf("stable wide transcript scroll delta = %d bytes", len(second))
	assertPhysicalFullscreenFrame(t, width, height, after, updates)
}

func normalizedTranscriptScrollFramesForTest(width int, height int, reasoning string) (string, string) {
	return normalizedTranscriptScrollFramesWithToolsForTest(
		width,
		height,
		reasoning,
		`  • Search service_tag|resource_type|MonitorCloudInstance in overview`,
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

func TestComposerMixedWidthDeletePreservesPhysicalWideText(t *testing.T) {
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
	assertPhysicalComposerFrame(t, model.fixedRowWidth(), after, outputs)
}

func TestDynamicWideNarrativeAndSelectionUseBoundedRepaintProtection(t *testing.T) {
	const (
		width  = 48
		height = 8
	)
	model := NewModel(Config{NoAnimation: true})
	model.width = width
	model.height = height
	model.viewport.SetWidth(width - 3)
	model.viewport.SetHeight(2)
	model.viewportStyledLines = []string{"· 先确认未提交改动的范围"}
	model.viewportPlainLines = []string{"· 先确认未提交改动的范围"}
	model.viewportSelectionIndents = []int{2}
	model.viewport.SetContentLines(model.viewportStyledLines)

	stable := model.renderViewportView()
	if strings.Contains(stable, wideCellRendererSentinel()) {
		t.Fatalf("stable viewport unexpectedly used dynamic repaint protection: %q", stable)
	}

	activeBlock := NewMainACPTurnBlock("active-wide-narrative")
	activeBlock.AppendStreamEvent(
		SEAssistant,
		"先确认未提交改动的范围",
		newNarrativeSourceIdentity("active-wide-message", "", ""),
	)
	activeRows := activeBlock.Render(model.blockRenderContext(model.viewport.Width()))
	activeFrame := joinRenderedStyled(activeRows)
	if len(activeRows) != 1 || !strings.Contains(activeFrame, wideCellRendererSentinel()) {
		t.Fatalf("active wide narrative missing bounded repaint protection: %#v", activeRows)
	}
	finalBlock := NewMainACPTurnBlock("final-wide-narrative")
	finalBlock.ReplaceFinalStreamEvent(
		SEAssistant,
		"先确认未提交改动的范围",
		newNarrativeSourceIdentity("final-wide-message", "", ""),
	)
	finalRows := finalBlock.Render(model.blockRenderContext(model.viewport.Width()))
	if len(finalRows) != 1 || strings.Contains(joinRenderedStyled(finalRows), wideCellRendererSentinel()) {
		t.Fatalf("stable final narrative unexpectedly used repaint protection: %#v", finalRows)
	}

	model.selectionStart = textSelectionPoint{line: 0, col: 2}
	model.selectionEnd = textSelectionPoint{line: 0, col: displayColumns(model.viewportPlainLines[0])}
	model.bumpViewportSelectionVersion()
	selected := model.renderViewportView()
	if !strings.Contains(selected, wideCellRendererSentinel()) {
		t.Fatalf("selected wide viewport missing bounded repaint protection: %q", selected)
	}

	activeUpdates := renderComposerFramesForTest(t, model.viewport.Width(), activeFrame, selected)
	assertPhysicalComposerFrame(t, model.viewport.Width(), selected, activeUpdates)
}

func TestActiveNarrativeRepaintProtectionUsesFinalTranscriptRowWidth(t *testing.T) {
	const width = 24
	tests := []struct {
		name string
		kind SubagentEventKind
	}{
		{name: "assistant", kind: SEAssistant},
		{name: "reasoning", kind: SEReasoning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := NewModel(Config{NoAnimation: true})
			block := NewMainACPTurnBlock("active-multiline-" + tt.name)
			source := newNarrativeSourceIdentity("message-"+tt.name, "", "")
			block.AppendStreamEvent(tt.kind, "第一行\n", source)
			before := joinRenderedStyled(block.Render(model.blockRenderContext(width)))
			block.AppendStreamEvent(tt.kind, "第二行", source)
			rows := block.Render(model.blockRenderContext(width))
			after := joinRenderedStyled(rows)

			activeRows := 0
			for _, row := range rows {
				if !row.activeTail {
					continue
				}
				activeRows++
				if got := displayColumns(row.Styled); got != width {
					t.Fatalf("active %s row width = %d, want %d: plain=%q styled=%q", tt.name, got, width, row.Plain, row.Styled)
				}
				if !strings.Contains(row.Styled, wideCellRendererSentinel()) {
					t.Fatalf("active %s row missing repaint sentinel: plain=%q styled=%q", tt.name, row.Plain, row.Styled)
				}
			}
			if activeRows < 2 {
				t.Fatalf("active %s rows = %#v, want protected multiline tail", tt.name, rows)
			}

			updates := renderComposerFramesForTest(t, width, before, after)
			assertPhysicalComposerFrame(t, width, after, updates)
		})
	}
}

func TestNormalizeFullscreenFrameLineFitsDisplayWidth(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		width          int
		wantPlain      string
		wantANSI       bool
		forbiddenPlain string
	}{
		{
			name:      "narrow_ascii_pads_without_guard",
			input:     "abc",
			width:     10,
			wantPlain: "abc       ",
		},
		{
			name:      "narrow_wide_cell_pads_without_guard",
			input:     "甲",
			width:     4,
			wantPlain: "甲  ",
		},
		{
			name:           "overwide_styled_cjk_truncates_before_padding",
			input:          "\x1b[31m甲乙丙\x1b[0m",
			width:          5,
			wantPlain:      "甲乙 ",
			wantANSI:       true,
			forbiddenPlain: "丙",
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
		})
	}
}

func TestNormalizeFullscreenFrameLineReseatsRepaintSentinelAtScreenEdge(t *testing.T) {
	const (
		viewportWidth = 20
		screenWidth   = 28
	)
	protected := protectRepaintGuardLine("· 先看当前改动", viewportWidth)
	if !strings.Contains(protected, wideCellRendererSentinel()) {
		t.Fatalf("viewport line missing repaint sentinel: %q", protected)
	}
	indented := strings.Repeat(" ", 2) + protected + " │"
	got := normalizeFullscreenFrameLine(indented, screenWidth)
	if displayColumns(got) != screenWidth {
		t.Fatalf("normalized width = %d, want %d; raw=%q", displayColumns(got), screenWidth, got)
	}
	if !strings.HasSuffix(got, wideCellRendererSentinel()) {
		t.Fatalf("sentinel is not the last screen cell after gutter/scrollbar pad: %q", got)
	}
	if strings.Count(got, wideCellRendererSentinel()) != 1 {
		t.Fatalf("normalized line should keep one sentinel: %q", got)
	}
	if !strings.Contains(ansi.Strip(got), "先看当前改动") {
		t.Fatalf("normalized line lost CJK text: %q", got)
	}
	plain := ansi.Strip(got)
	border := strings.LastIndex(plain, "│")
	if border < 0 || displayColumns(plain[:border]) != 23 {
		t.Fatalf("interior suffix shifted while reseating sentinel: plain=%q raw=%q", plain, got)
	}
}

func TestSubagentRosterOverlayStaysOpaqueWhileTranscriptStreamsUnderIt(t *testing.T) {
	const (
		width  = 160
		height = 32
	)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	model = updated.(*Model)
	addSubagentRosterTestView(
		model,
		"spawn-overlay-review",
		"overlay-ux-review",
		"overlay-ux-review[orbit]: inspect floating roster composition",
		"completed",
		time.Unix(90, 0),
		time.Unix(120, 0),
	)

	transcriptLine := "underlay " + strings.Repeat("0123456789", 13) + " edge"
	for range 40 {
		model.doc.Append(NewUserNarrativeBlock(transcriptLine))
	}
	model.markViewportStructureDirty()
	model.syncViewportContent()
	model.viewport.GotoBottom()
	model.refreshViewportFollowStateFromOffset()
	if !model.openSubagentRosterOverlay() {
		t.Fatal("openSubagentRosterOverlay() = false")
	}

	frames := make([]string, 0, 6)
	previousOffset := model.viewport.YOffset()
	offsetAdvances := 0
	var fixedGeometry subagentRosterOverlayGeometry
	captureFrame := func(frameIndex int) {
		t.Helper()
		state := model.subagentRosterOverlay
		model.subagentRosterOverlay = nil
		base := model.View().Content
		model.subagentRosterOverlay = state

		normalizeBefore := model.diag.FullscreenNormalizeCalls
		frame := model.View().Content
		if got := model.diag.FullscreenNormalizeCalls - normalizeBefore; got != 2 {
			t.Fatalf("roster frame %d fullscreen normalization calls = %d, want 2", frameIndex, got)
		}
		overlay := model.renderSubagentRosterOverlay()
		geometry := model.subagentRosterOverlay.geometry
		if frameIndex == 0 {
			fixedGeometry = geometry
		} else if geometry.x != fixedGeometry.x || geometry.y != fixedGeometry.y ||
			geometry.width != fixedGeometry.width || geometry.height != fixedGeometry.height {
			t.Fatalf("roster geometry moved in frame %d: got (%d,%d %dx%d), want (%d,%d %dx%d)",
				frameIndex,
				geometry.x, geometry.y, geometry.width, geometry.height,
				fixedGeometry.x, fixedGeometry.y, fixedGeometry.width, fixedGeometry.height,
			)
		}

		lines := strings.Split(frame, "\n")
		if len(lines) != height {
			t.Fatalf("roster frame %d height = %d, want %d", frameIndex, len(lines), height)
		}
		for row, line := range lines {
			if got := displayColumns(line); got != width {
				t.Fatalf("roster frame %d row %d width = %d, want %d", frameIndex, row, got, width)
			}
		}

		baseScreen := uv.NewScreenBuffer(width, height)
		baseScreen.Method = ansi.GraphemeWidth
		uv.NewStyledString(base).Draw(baseScreen, baseScreen.Bounds())
		frameScreen := uv.NewScreenBuffer(width, height)
		frameScreen.Method = ansi.GraphemeWidth
		uv.NewStyledString(frame).Draw(frameScreen, frameScreen.Bounds())
		overlayScreen := uv.NewScreenBuffer(geometry.width, geometry.height)
		overlayScreen.Method = ansi.GraphemeWidth
		uv.NewStyledString(overlay).Draw(overlayScreen, overlayScreen.Bounds())

		preservedTranscriptCells := 0
		coveredTranscriptCells := 0
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				baseCell := baseScreen.CellAt(x, y)
				frameCell := frameScreen.CellAt(x, y)
				inside := x >= geometry.x && x < geometry.x+geometry.width &&
					y >= geometry.y && y < geometry.y+geometry.height
				if !inside {
					if frameCell == nil || !frameCell.Equal(baseCell) {
						t.Fatalf("roster frame %d changed base cell outside overlay at (%d,%d): got %#v, want %#v", frameIndex, x, y, frameCell, baseCell)
					}
					if y >= geometry.y && y < geometry.y+geometry.height &&
						baseCell != nil && baseCell.Content != "" && baseCell.Content != " " {
						preservedTranscriptCells++
					}
					continue
				}

				overlayCell := overlayScreen.CellAt(x-geometry.x, y-geometry.y)
				if frameCell == nil || !frameCell.Equal(overlayCell) {
					t.Fatalf("roster frame %d leaked base through overlay at (%d,%d): got %#v, want %#v", frameIndex, x, y, frameCell, overlayCell)
				}
				if baseCell != nil && baseCell.Content != "" && baseCell.Content != " " {
					coveredTranscriptCells++
				}
			}
		}
		if preservedTranscriptCells == 0 {
			t.Fatalf("roster frame %d did not exercise transcript text beside the overlay", frameIndex)
		}
		if coveredTranscriptCells == 0 {
			t.Fatalf("roster frame %d did not cover transcript text inside the overlay rectangle", frameIndex)
		}
		frames = append(frames, frame)
	}

	captureFrame(0)
	for index, suffix := range []string{"alpha", "beta", "gamma", "delta", "epsilon"} {
		model.doc.Append(NewUserNarrativeBlock("stream " + suffix + " " + strings.Repeat("abcdefghij", 13) + " edge"))
		model.markViewportStructureDirty()
		model.syncViewportContent()
		if current := model.viewport.YOffset(); current > previousOffset {
			offsetAdvances++
			previousOffset = current
		}
		captureFrame(index + 1)
	}
	if offsetAdvances < 3 {
		t.Fatalf("streaming transcript advanced viewport offset %d time(s), want at least three", offsetAdvances)
	}

	updates := renderFullscreenFramesForTest(t, width, height, frames...)
	for index := range frames {
		if index > 0 && frameContainsCSICommand(updates[index], "@LMP") {
			t.Fatalf("streaming-under-roster update %d used ICH/IL/DL/DCH while the base moved: %q", index, updates[index])
		}
		assertPhysicalFullscreenFrame(t, width, height, frames[index], updates[:index+1])
	}
}

func TestSubagentOutputOverlayActiveNarrativeKeepsPhysicalBorderAligned(t *testing.T) {
	const (
		width  = 100
		height = 22
	)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	model = updated.(*Model)
	view := model.ensureSubagentOutputView("spawn-border")
	source := newNarrativeSourceIdentity("message-border", "", "")
	view.block.AppendStreamEvent(SEAssistant, "第一行", source)
	view.touch(true)
	model.subagentOutputOverlay = &subagentOutputOverlayState{
		callID: "spawn-border", followTail: true,
		selectStart: textSelectionPoint{line: -1, col: -1},
		selectEnd:   textSelectionPoint{line: -1, col: -1},
	}
	before := model.View().Content

	view.block.AppendStreamEvent(SEAssistant, "\n第二行", source)
	view.touch(true)
	after := model.View().Content
	layout := model.subagentOutputOverlay.layout
	wantRightBorder := layout.startX + layout.frameWidth - 1
	assertOverlayContentRightBorderColumn(t, ansi.Strip(after), "第二行", wantRightBorder)

	updates := renderFullscreenFramesForTest(t, width, height, before, after)
	assertPhysicalFullscreenFrame(t, width, height, after, updates)
}

func TestSubagentOutputOverlayLongStreamingTailKeepsPhysicalFrameExact(t *testing.T) {
	const (
		width  = 84
		height = 18
	)
	model := NewModel(Config{NoAnimation: true, NoColor: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	model = updated.(*Model)
	view := model.ensureSubagentOutputView("spawn-long-stream")
	source := newNarrativeSourceIdentity("message-long-stream", "", "")
	model.subagentOutputOverlay = &subagentOutputOverlayState{
		callID: "spawn-long-stream", followTail: true,
		selectStart: textSelectionPoint{line: -1, col: -1},
		selectEnd:   textSelectionPoint{line: -1, col: -1},
	}

	chunks := []string{
		"## 审查结论\n\n",
		"这是第一段，包含中文、emoji 🧭 和 `inline code`。\n\n",
		"- 检查点 01：确认 Surface 只负责展示。\n",
		"- 检查点 02：确认 Control 持有生命周期。\n",
		"- 检查点 03：保留合法重复文本 haha。\n",
		"- 检查点 04：继续追加稳定 Markdown。\n",
		"- 检查点 05：窗口即将开始跟随尾部。\n",
		"- 检查点 06：这一行会推动整个窗口上移。\n",
		"- 检查点 07：第二次推动可见窗口。\n",
		"- 检查点 08：第三次推动可见窗口。\n",
		"\n```text\n",
		"stream frame alpha\n",
		"stream frame beta 中文\n",
		"stream frame gamma 🚀\n",
		"```\n\n",
		"最后一段仍处于 streaming tail，不能依赖 Final 全量重绘恢复。",
	}
	frames := make([]string, 0, len(chunks))
	var exact strings.Builder
	offsetAdvances := 0
	previousOffset := 0
	for index, chunk := range chunks {
		exact.WriteString(chunk)
		view.block.AppendStreamEvent(SEAssistant, chunk, source)
		view.touch(true)
		if len(view.block.Events) != 1 || view.block.Events[0].Text != exact.String() {
			t.Fatalf("semantic stream after chunk %d = %#v, want one exact-delta narrative %q", index, view.block.Events, exact.String())
		}
		frame := model.View().Content
		frames = append(frames, frame)
		if current := model.subagentOutputOverlay.offset; current > previousOffset {
			offsetAdvances++
			previousOffset = current
		}
	}
	if offsetAdvances < 3 {
		t.Fatalf("long stream advanced follow-tail offset %d time(s), want at least three", offsetAdvances)
	}

	updates := renderFullscreenFramesForTest(t, width, height, frames...)
	for index := range frames {
		if index > 0 && frameContainsCSICommand(updates[index], "@LM") {
			t.Fatalf("long overlay stream update %d used ICH/IL/DL while its visible window moved: %q", index, updates[index])
		}
		assertPhysicalFullscreenFrame(t, width, height, frames[index], updates[:index+1])
	}
}

func assertOverlayContentRightBorderColumn(t *testing.T, frame string, needle string, want int) {
	t.Helper()
	for _, line := range strings.Split(frame, "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		border := strings.LastIndex(line, "│")
		if border < 0 {
			t.Fatalf("overlay row containing %q has no right border: %q", needle, line)
		}
		if got := displayColumns(line[:border]); got != want {
			t.Fatalf("overlay right border column = %d, want %d: %q", got, want, line)
		}
		return
	}
	t.Fatalf("fullscreen frame omitted overlay row %q:\n%s", needle, frame)
}

func TestActiveASCIINarrativeGetsRepaintGuard(t *testing.T) {
	const width = 48
	model := NewModel(Config{NoAnimation: true})
	block := NewMainACPTurnBlock("active-ascii-narrative")
	block.AppendStreamEvent(
		SEReasoning,
		"Need to follow the repo commit style.",
		newNarrativeSourceIdentity("ascii-message", "", ""),
	)
	rows := block.Render(model.blockRenderContext(width))
	found := false
	for _, row := range rows {
		if !row.activeTail {
			continue
		}
		found = true
		if !strings.Contains(row.Styled, wideCellRendererSentinel()) {
			t.Fatalf("active ASCII tail missing repaint sentinel: plain=%q styled=%q", row.Plain, row.Styled)
		}
		if got := displayColumns(row.Styled); got != width {
			t.Fatalf("active ASCII tail width = %d, want %d: %q", got, width, row.Styled)
		}
	}
	if !found {
		t.Fatalf("expected an active ASCII reasoning tail: %#v", rows)
	}
}

func TestGrowingLiveNarrativeFullscreenFramesAvoidInsertCharacter(t *testing.T) {
	const (
		width  = 80
		height = 18
	)
	model := NewModel(Config{NoAnimation: true, NoColor: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	model = updated.(*Model)

	block := NewMainACPTurnBlock("turn-live-cjk")
	reasoningSource := newNarrativeSourceIdentity("live-cjk-reasoning", "event-live-reasoning", "projection-live-reasoning")
	assistantSource := newNarrativeSourceIdentity("live-cjk-assistant", "event-live-assistant", "projection-live-assistant")
	block.AppendStreamEvent(SEReasoning, "The user wants me to commit the changes directly. I need to follow", reasoningSource)
	block.AppendStreamEvent(SEAssistant, "先看当前", assistantSource)
	model.doc.Append(block)
	model.syncViewportContent()
	before := model.View().Content

	block.AppendStreamEvent(SEAssistant, "改动和仓库的提交风格，再按同样格式提交。", assistantSource)
	model.markViewportBlockDirty(block.BlockID())
	model.syncViewportContent()
	after := model.View().Content

	afterPlain := ansi.Strip(after)
	for _, want := range []string{
		"I need to follow",
		"先看当前改动和仓库的提交风格，再按同样格式提交。",
	} {
		if !strings.Contains(afterPlain, want) {
			t.Fatalf("final view missing %q:\n%s", want, afterPlain)
		}
	}
	if !fullscreenFrameHasTrailingRepaintSentinel(after, "先看当前改动") {
		t.Fatalf("live CJK line lost screen-edge repaint sentinel:\n%s", after)
	}

	updates := renderFullscreenFramesForTest(t, width, height, before, after)
	if frameContainsInsertCharacter(updates[1]) {
		t.Fatalf("growing live narrative used ICH; Ghostty erases split CJK cells: %q", updates[1])
	}
	assertPhysicalFullscreenFrame(t, width, height, after, updates)
}

func fullscreenFrameHasTrailingRepaintSentinel(frame string, needle string) bool {
	for _, line := range strings.Split(frame, "\n") {
		if !strings.Contains(ansi.Strip(line), needle) {
			continue
		}
		return strings.HasSuffix(line, wideCellRendererSentinel())
	}
	return false
}

func frameContainsInsertCharacter(s string) bool {
	return frameContainsCSICommand(s, "@")
}

func frameContainsCSICommand(s string, commands string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '\x1b' || i+1 >= len(s) || s[i+1] != '[' {
			continue
		}
		j := i + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
			j++
		}
		if j < len(s) && strings.ContainsRune(commands, rune(s[j])) {
			return true
		}
	}
	return false
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

func assertPhysicalComposerFrame(t *testing.T, width int, want string, updates []string) {
	t.Helper()
	height := strings.Count(want, "\n") + 1
	terminal := vt.NewSafeEmulator(width, height)
	t.Cleanup(func() { _ = terminal.Close() })
	for idx, update := range updates {
		if _, err := terminal.Write([]byte(update)); err != nil {
			t.Fatalf("write composer update %d to physical terminal: %v", idx, err)
		}
	}
	got := trimPhysicalFramePadding(ansi.Strip(terminal.Render()))
	want = trimPhysicalFramePadding(ansi.Strip(want))
	if got != want {
		t.Fatalf("physical composer frame mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

func assertPhysicalFullscreenFrame(t *testing.T, width int, height int, want string, updates []string) {
	t.Helper()
	terminal := vt.NewSafeEmulator(width, height)
	t.Cleanup(func() { _ = terminal.Close() })
	for idx, update := range updates {
		if _, err := terminal.Write([]byte(update)); err != nil {
			t.Fatalf("write fullscreen update %d to physical terminal: %v", idx, err)
		}
	}
	got := trimPhysicalFramePadding(ansi.Strip(terminal.Render()))
	want = trimPhysicalFramePadding(ansi.Strip(want))
	if got != want {
		t.Fatalf("physical fullscreen frame mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

func trimPhysicalFramePadding(frame string) string {
	lines := strings.Split(frame, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n")
}
