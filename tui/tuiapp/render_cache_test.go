package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
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

	userIdx := indexPlainLineContaining(m.viewportPlainLines, "> review this rendering")
	answerIdx := indexPlainLineContaining(m.viewportPlainLines, "* I will inspect")
	readStartIdx := indexPlainLineContaining(m.viewportPlainLines, "▸ READ")
	readDoneIdx := indexPlainLineContaining(m.viewportPlainLines, "✓ READ")
	followupIdx := indexPlainLineContaining(m.viewportPlainLines, "* The tool output")
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

func TestModelViewRequestsAllMotionForViewportHover(t *testing.T) {
	m := NewModel(Config{})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	if got := updated.View().MouseMode; got != tea.MouseModeAllMotion {
		t.Fatalf("View().MouseMode = %v, want all-motion hover support", got)
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
