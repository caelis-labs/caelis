package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

func TestViewportRhythmAddsDisplayOnlyGapsWithoutSplittingToolRuns(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	m.theme = tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	m.viewport.SetWidth(90)
	m.viewport.SetHeight(30)

	m.doc.Append(NewUserNarrativeBlock("review this rendering"))
	answer := NewAssistantBlock()
	answer.Raw = "I will inspect the current transcript rendering."
	answer.Streaming = false
	m.doc.Append(answer)
	m.doc.Append(NewTranscriptBlock("▸ READ render_cache.go", tuikit.LineStyleTool))
	m.doc.Append(NewTranscriptBlock("✓ READ render_cache.go 1~20", tuikit.LineStyleTool))
	followup := NewAssistantBlock()
	followup.Raw = "The tool output should stay visually connected."
	followup.Streaming = false
	m.doc.Append(followup)
	docLen := m.doc.Len()

	m.syncViewportContent()

	if got := m.doc.Len(); got != docLen {
		t.Fatalf("syncViewportContent changed document length: got %d, want %d", got, docLen)
	}
	assertViewportCacheLengthsMatch(t, m)

	userIdx := indexPlainLineContaining(m.viewportPlainLines, "▌ review this rendering")
	answerIdx := indexPlainLineContaining(m.viewportPlainLines, "· I will inspect")
	readStartIdx := indexPlainLineContaining(m.viewportPlainLines, "▸ READ")
	readDoneIdx := indexPlainLineContaining(m.viewportPlainLines, "✓ READ")
	followupIdx := indexPlainLineContaining(m.viewportPlainLines, "· The tool output")
	for label, idx := range map[string]int{
		"user":       userIdx,
		"answer":     answerIdx,
		"read start": readStartIdx,
		"read done":  readDoneIdx,
		"followup":   followupIdx,
	} {
		if idx < 0 {
			t.Fatalf("missing %s line in viewport plain lines: %#v", label, m.viewportPlainLines)
		}
	}
	if !hasBlankLineBetween(m.viewportPlainLines, userIdx, answerIdx) {
		t.Fatalf("expected display gap between user and assistant, got %#v", m.viewportPlainLines)
	}
	if !hasBlankLineBetween(m.viewportPlainLines, answerIdx, readStartIdx) {
		t.Fatalf("expected display gap between assistant and tool output, got %#v", m.viewportPlainLines)
	}
	if hasBlankLineBetween(m.viewportPlainLines, readStartIdx, readDoneIdx) {
		t.Fatalf("consecutive tool lines should remain compact, got %#v", m.viewportPlainLines)
	}
	if !hasBlankLineBetween(m.viewportPlainLines, readDoneIdx, followupIdx) {
		t.Fatalf("expected display gap between tool run and assistant followup, got %#v", m.viewportPlainLines)
	}
}

func TestViewportCacheKeepsClickTokenLengthForStreamingLines(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	m.theme = tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	m.viewport.SetWidth(24)
	m.viewport.SetHeight(8)
	m.streamLine = "streaming answer that wraps across multiple viewport rows"

	m.syncViewportContent()

	assertViewportCacheLengthsMatch(t, m)
	if len(m.viewportStyledLines) < 2 {
		t.Fatalf("expected wrapped stream content, got %#v", m.viewportStyledLines)
	}
}

func TestACPHeaderViewportWrapSupportsDynamicToolNames(t *testing.T) {
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	block := NewMainACPTurnBlock("session-1")
	width := 32
	ctx := m.blockRenderContext(width)
	plain := `• lookup_weather "Shanghai" --units metric --include-hourly forecast`
	row := renderACPTranscriptHeaderRow(block.BlockID(), plain, width, ctx, "tool:weather")

	_, plainLines, clickTokens := m.wrapRenderedRowsForViewport(block, []RenderedRow{row}, width, ctx)

	if len(plainLines) < 2 {
		t.Fatalf("dynamic ACP header did not wrap as a header: %#v", plainLines)
	}
	continuationPrefix := strings.Repeat(" ", displayColumns(`• lookup_weather `))
	if !strings.HasPrefix(plainLines[1], continuationPrefix) {
		t.Fatalf("dynamic ACP header continuation = %q, want prefix %q\nall lines=%#v", plainLines[1], continuationPrefix, plainLines)
	}
	for i, token := range clickTokens {
		if token != "tool:weather" {
			t.Fatalf("click token %d = %q, want propagated header token", i, token)
		}
	}
}

func TestACPReasoningSummaryViewportWrapAlignsContinuation(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	m.theme = tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	block := NewMainACPTurnBlock("session-1")
	width := 34
	ctx := m.blockRenderContext(width)
	plain := "› Good, no whitespace issues. Now let me compile the comprehensive review."
	styled := ctx.Theme.ReasoningStyle().Render("›") + ctx.Theme.ReasoningStyle().Render(" Good, no whitespace issues. Now let me compile the comprehensive review.")
	row := StyledPlainClickableRow(block.BlockID(), plain, styled, "acp_reasoning:0")

	_, plainLines, clickTokens := m.wrapRenderedRowsForViewport(block, []RenderedRow{row}, width, ctx)

	if len(plainLines) < 2 {
		t.Fatalf("reasoning summary did not wrap: %#v", plainLines)
	}
	if !strings.HasPrefix(plainLines[0], "› ") {
		t.Fatalf("first summary line = %q, want marker prefix", plainLines[0])
	}
	if !strings.HasPrefix(plainLines[1], "  ") || strings.HasPrefix(plainLines[1], "›") {
		t.Fatalf("summary continuation = %q, want body-column indentation\nall lines=%#v", plainLines[1], plainLines)
	}
	for i, token := range clickTokens {
		if token != "acp_reasoning:0" {
			t.Fatalf("click token %d = %q, want propagated reasoning token", i, token)
		}
	}
}

func TestACPAssistantNarrativeViewportWrapPreservesStyledBody(t *testing.T) {
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	block := NewMainACPTurnBlock("session-1")
	width := 24
	ctx := m.blockRenderContext(width)
	plain := "· Keep formatted assistant text visible after wrapping"
	styledBody := "\x1b[38;5;201mKeep formatted assistant text visible after wrapping\x1b[0m"
	styled := ctx.Theme.AssistantStyle().Render("· ") + styledBody
	row := StyledPlainClickableRow(block.BlockID(), plain, styled, "assistant:0")

	styledLines, plainLines, clickTokens := m.wrapRenderedRowsForViewport(block, []RenderedRow{row}, width, ctx)

	if len(plainLines) < 2 {
		t.Fatalf("assistant narrative did not wrap: %#v", plainLines)
	}
	if !strings.Contains(strings.Join(styledLines, "\n"), "\x1b[38;5;201m") {
		t.Fatalf("wrapped styled lines lost body ANSI style: %#v", styledLines)
	}
	if !strings.HasPrefix(plainLines[1], "  ") || strings.HasPrefix(plainLines[1], "·") {
		t.Fatalf("assistant continuation = %q, want body-column indentation\nall lines=%#v", plainLines[1], plainLines)
	}
	for i, token := range clickTokens {
		if token != "assistant:0" {
			t.Fatalf("click token %d = %q, want propagated assistant token", i, token)
		}
	}
}

func TestViewportHeightChangeDoesNotInvalidateGenericBlockCache(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	m.theme = tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	m.viewport.SetWidth(80)
	m.viewport.SetHeight(24)
	for i := 0; i < 20; i++ {
		m.doc.Append(NewTranscriptBlock("stable transcript line", tuikit.LineStyleDefault))
	}

	m.syncViewportContent()
	fullSyncs := m.diag.ViewportFullSyncs

	m.viewport.SetHeight(18)
	m.syncViewportContent()

	if got := m.diag.ViewportFullSyncs; got != fullSyncs {
		t.Fatalf("height-only change full syncs = %d, want %d", got, fullSyncs)
	}
}

func TestViewportHeightChangeInvalidatesActiveACPReasoningBudget(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	m.theme = tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	m.viewport.SetWidth(96)
	m.viewport.SetHeight(3)
	block := NewMainACPTurnBlock("session-1")
	block.Events = append(block.Events, SubagentEvent{Kind: SEReasoning, Text: numberedReasoningLines(30)})
	m.doc.Append(block)

	m.syncViewportContent()
	initialReasoningRows := countRenderCacheRowsContaining(m.viewportPlainLines, "reasoning line ")
	if initialReasoningRows != liveReasoningRowBudget(m.blockRenderContext(96))-1 {
		t.Fatalf("initial reasoning rows = %d, lines = %#v", initialReasoningRows, m.viewportPlainLines)
	}

	m.viewport.SetHeight(5)
	m.syncViewportContent()

	expandedReasoningRows := countRenderCacheRowsContaining(m.viewportPlainLines, "reasoning line ")
	if expandedReasoningRows != liveReasoningRowBudget(m.blockRenderContext(96))-1 {
		t.Fatalf("expanded reasoning rows = %d, lines = %#v", expandedReasoningRows, m.viewportPlainLines)
	}
	if expandedReasoningRows <= initialReasoningRows {
		t.Fatalf("reasoning budget did not refresh after height change: initial=%d expanded=%d lines=%#v", initialReasoningRows, expandedReasoningRows, m.viewportPlainLines)
	}
}

func TestViewportMouseWheelUsesReadableScrollStep(t *testing.T) {
	m := NewModel(Config{})
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(10)
	m.viewport.SetContentLines(numberedViewportLines(60))
	m.viewport.SetYOffset(0)

	updated, _ := m.handleMouse(tea.MouseWheelMsg(tea.Mouse{Button: tea.MouseWheelDown}))
	m = updated.(*Model)

	if got, want := m.viewport.YOffset(), 5; got != want {
		t.Fatalf("wheel down offset = %d, want %d", got, want)
	}
	if !m.userScrolledUp {
		t.Fatal("wheeling away from tail should mark userScrolledUp")
	}
}

func TestViewportIgnoresHorizontalWheelScrolling(t *testing.T) {
	m := NewModel(Config{})
	m.viewport.SetWidth(20)
	m.viewport.SetHeight(6)
	m.viewport.SetContentLines([]string{"this is a deliberately long line that must not pan horizontally"})

	updated, _ := m.handleMouse(tea.MouseWheelMsg(tea.Mouse{Button: tea.MouseWheelRight}))
	m = updated.(*Model)
	if got := m.viewport.XOffset(); got != 0 {
		t.Fatalf("horizontal wheel changed viewport XOffset to %d, want 0", got)
	}

	updated, _ = m.handleMouse(tea.MouseWheelMsg(tea.Mouse{Button: tea.MouseWheelDown, Mod: tea.ModShift}))
	m = updated.(*Model)
	if got := m.viewport.XOffset(); got != 0 {
		t.Fatalf("shift+wheel changed viewport XOffset to %d, want 0", got)
	}
}

func TestShiftPageKeysHalfPageViewport(t *testing.T) {
	m := NewModel(Config{})
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(10)
	m.viewport.SetContentLines(numberedViewportLines(60))
	m.viewport.SetYOffset(0)

	updated, _ := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown, Mod: tea.ModShift}))
	m = updated.(*Model)

	if got, want := m.viewport.YOffset(), 5; got != want {
		t.Fatalf("shift+pgdown offset = %d, want half page %d", got, want)
	}
	updated, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}))
	m = updated.(*Model)
	if got := m.viewport.YOffset(); got != 0 {
		t.Fatalf("shift+pgup offset = %d, want 0", got)
	}
}

func TestModelViewUsesCellMotionByDefault(t *testing.T) {
	m := NewModel(Config{})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	if got := updated.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("View().MouseMode = %v, want cell-motion mouse support", got)
	}
}

func assertViewportCacheLengthsMatch(t *testing.T, m *Model) {
	t.Helper()
	if len(m.viewportStyledLines) != len(m.viewportPlainLines) ||
		len(m.viewportStyledLines) != len(m.viewportBlockIDs) ||
		len(m.viewportStyledLines) != len(m.viewportClickTokens) {
		t.Fatalf("viewport cache length mismatch styled=%d plain=%d blockIDs=%d clickTokens=%d",
			len(m.viewportStyledLines),
			len(m.viewportPlainLines),
			len(m.viewportBlockIDs),
			len(m.viewportClickTokens),
		)
	}
}

func indexPlainLineContaining(lines []string, needle string) int {
	for i, line := range lines {
		if strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}

func countRenderCacheRowsContaining(lines []string, needle string) int {
	count := 0
	for _, line := range lines {
		if strings.Contains(line, needle) {
			count++
		}
	}
	return count
}

func hasBlankLineBetween(lines []string, start int, end int) bool {
	if start < 0 || end < 0 || start >= end {
		return false
	}
	for _, line := range lines[start+1 : end] {
		if strings.TrimSpace(line) == "" {
			return true
		}
	}
	return false
}

func numberedViewportLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = "line"
	}
	return lines
}

func TestIncrementalSyncWithEmptyPrecedingBlockCorruptsLineStarts(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	m.theme = tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	m.viewport.SetWidth(90)
	m.viewport.SetHeight(30)

	// Add an empty block that renders to 0 lines initially
	emptyBlock := NewAssistantBlock()
	emptyBlock.Streaming = true
	m.doc.Append(emptyBlock)
	m.syncViewportContent()

	// Add a divider block
	divider := NewDividerBlock("Turn completed")
	m.doc.Append(divider)
	m.syncViewportContent()

	t.Logf("emptyBlock: start=%v, count=%v", m.viewportRenderEntries[0].lineStart, m.viewportRenderEntries[0].lineCount)
	t.Logf("divider: start=%v, count=%v", m.viewportRenderEntries[1].lineStart, m.viewportRenderEntries[1].lineCount)

	// Now modify the empty block to have 2 lines
	emptyBlock.Raw = "hello\nworld"
	emptyBlock.Streaming = false
	if m.dirtyViewportBlocks == nil {
		m.dirtyViewportBlocks = make(map[string]struct{})
	}
	m.dirtyViewportBlocks[emptyBlock.BlockID()] = struct{}{}

	// Trigger incremental sync
	m.syncViewportContent()

	// The empty block now has 2 lines, so divider should have lineStart shifted by 2
	expectedLineStart := m.viewportRenderEntries[0].lineStart + m.viewportRenderEntries[0].lineCount
	if m.viewportRenderEntries[1].lineStart != expectedLineStart {
		t.Fatalf("expected divider lineStart to be %v, but got %v", expectedLineStart, m.viewportRenderEntries[1].lineStart)
	}
	t.Logf("emptyBlock final: start=%v, count=%v", m.viewportRenderEntries[0].lineStart, m.viewportRenderEntries[0].lineCount)
	t.Logf("divider final: start=%v, count=%v", m.viewportRenderEntries[1].lineStart, m.viewportRenderEntries[1].lineCount)
}
