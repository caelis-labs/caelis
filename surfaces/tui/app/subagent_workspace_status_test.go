package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/charmbracelet/x/ansi"
)

func TestPaneStatusPartsUsesDirectorySnapshotChildGauge(t *testing.T) {
	m, _ := newPaneTestModel(t)
	m.setSubagentLayout(uipreferences.Right)
	m.statusContext = "99k / 100k · 99%"
	rawModel := "xai@default/xai/grok-4.6"
	descriptor := taskstream.TaskDescriptor{
		SessionID: "session-1", TaskID: "task-breeze", Handle: "breeze", Kind: "subagent", Running: true,
		Model:      rawModel,
		ParentTool: taskstream.ParentTool{ToolCallID: "spawn-breeze", ToolName: "StartThread"},
	}
	var frames []string
	for i, gauge := range []struct {
		used, size uint64
		want       string
	}{
		{0, 0, ""},
		{12000, 128000, "12k / 128k · 9%"},
		{8000, 200000, "8k / 200k · 4%"},
		{0, 200000, "0 / 200k · 0%"},
		{0, 0, ""},
	} {
		descriptor.ContextUsed, descriptor.ContextSize = gauge.used, gauge.size
		m.Update(subagentDirectorySnapshotMsg{
			sessionID: m.currentSessionID, generation: m.subagentDirectoryGeneration,
			snapshot: taskstream.DirectorySnapshot{Revision: uint64(i + 1), Tasks: []taskstream.TaskDescriptor{descriptor}},
		})
		if stored := m.subagentRosterTasks["spawn-breeze"]; stored.Model != rawModel {
			t.Fatalf("raw descriptor model = %q, want unchanged %q", stored.Model, rawModel)
		}
		model, usage := m.paneStatusParts(m.subagentOutputOverlay)
		if model != "xai/grok-4.6" || usage != gauge.want {
			t.Fatalf("pane metadata = %q, %q; want xai/grok-4.6 and %q", model, usage, gauge.want)
		}
		frame := m.View().Content
		frames = append(frames, frame)
		g := m.subagentOutputOverlay.geometry
		footer := sliceByDisplayColumns(ansi.Strip(strings.Split(frame, "\n")[g.footerY]), g.contentX, g.contentX+g.contentWidth)
		wantRight := "F7 Hide"
		if gauge.want != "" {
			wantRight += "  " + gauge.want
		}
		if !strings.HasPrefix(footer, "xai/grok-4.6") || !strings.HasSuffix(footer, wantRight) || strings.Contains(footer, "xai@default") || strings.Contains(footer, "99k") {
			t.Fatalf("rendered directory metadata = %q", footer)
		}
		if m.statusContext != "99k / 100k · 99%" {
			t.Fatal("child gauge changed parent usage")
		}
		t.Logf("directory footer: %s", footer)
	}
	updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
	for i, frame := range frames {
		assertPhysicalFullscreenFrame(t, m.width, m.height, frame, updates[:i+1])
	}
}

func TestPaneModelDisplayKeepsShortIdentity(t *testing.T) {
	for raw, want := range map[string]string{
		"xai@default/xai/grok-4.6": "xai/grok-4.6",
		"xai@work/xai/grok-4.6":    "xai/grok-4.6",
		"xai/grok-4.6":             "xai/grok-4.6",
		"grok-4.6":                 "grok-4.6",
		"vendor/model@version":     "vendor/model@version",
		"":                         "Model unavailable",
	} {
		if got := paneModelDisplay(raw); got != want {
			t.Fatalf("paneModelDisplay(%q) = %q, want %q", raw, got, want)
		}
	}
}
