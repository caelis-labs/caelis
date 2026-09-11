package tuiapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

type paneTestClient struct {
	mu          sync.Mutex
	requests    []appserver.SubagentInputRequest
	preferences uipreferences.Preferences
}

func (c *paneTestClient) SubmitSubagentInput(_ context.Context, req appserver.SubagentInputRequest) (collaboration.UserInputStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req)
	return collaboration.UserInputStatus{ID: req.OperationID, State: "queued"}, nil
}
func (*paneTestClient) SubagentInputStatuses(_ context.Context, req appserver.SubagentInputStatusRequest) ([]collaboration.UserInputStatus, error) {
	return []collaboration.UserInputStatus{{ID: req.IDs[0], State: "sent"}}, nil
}
func (c *paneTestClient) LoadUIPreferences(context.Context) (uipreferences.Preferences, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.preferences, nil
}
func (c *paneTestClient) SaveUIPreferences(_ context.Context, p uipreferences.Preferences) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.preferences = p
	return nil
}
func newPaneTestModel(t testing.TB) (*Model, *paneTestClient) {
	t.Helper()
	client := &paneTestClient{}
	model := NewModel(Config{NoColor: true, NoAnimation: true, SubagentInputs: client, UIPreferences: client})
	model.currentSessionID = "session-1"
	_, _ = model.Update(tea.WindowSizeMsg{Width: 160, Height: 48})
	for _, name := range []string{"breeze", "omar"} {
		view := model.ensureSubagentOutputView("spawn-" + name)
		view.taskHandle = name
		view.actor = name
		for i := range 50 {
			view.observeChildEvent(TranscriptEvent{Kind: TranscriptEventNotice, Scope: ACPProjectionSubagent, ScopeID: name, TurnID: name + "-turn", Text: fmt.Sprintf("%s line %02d: 读取并验证输出", name, i), NoticeKind: "test"})
		}
		model.subagentRosterTasks[view.callID] = taskstream.TaskDescriptor{SessionID: "session-1", TaskID: "task-" + name, ParticipantID: "participant-" + name, Handle: name, Running: true, Model: "test-model", ContextUsed: 12000, ContextSize: 128000}
	}
	model.commitLine("Main transcript remains visible")
	model.syncViewportContent()
	if !model.openSubagentOutputOverlayView("spawn-breeze", model.subagentOutputViews["spawn-breeze"]) {
		t.Fatal("open pane")
	}
	return model, client
}
func TestSubagentWorkspaceLayoutsAndBoundedDrag(t *testing.T) {
	for _, mode := range []uipreferences.Layout{uipreferences.Left, uipreferences.Right, uipreferences.Up, uipreferences.Down} {
		t.Run(string(mode), func(t *testing.T) {
			model, client := newPaneTestModel(t)
			cmd := model.setSubagentLayout(mode)
			runTeaCmds(t, model, cmd)
			layout := model.workspaceLayout()
			if !layout.split {
				t.Fatal("large terminal did not split")
			}
			if layout.main.contains(layout.child.x, layout.child.y) || layout.child.contains(layout.main.x, layout.main.y) {
				t.Fatalf("overlapping panes: %#v", layout)
			}
			frame := model.View().Content
			if !strings.Contains(ansi.Strip(frame), "Main transcript remains visible") || !strings.Contains(frame, "breeze") {
				t.Fatal("split omitted a transcript")
			}
			if cursor := model.View().Cursor; cursor == nil || !layout.child.contains(cursor.X, cursor.Y) {
				t.Fatalf("child cursor=%#v layout=%#v", cursor, layout)
			}
			mouse := tea.Mouse{X: layout.divider.x, Y: layout.divider.y, Button: tea.MouseLeft}
			_, _ = model.handleMouse(tea.MouseClickMsg(mouse))
			mouse.X, mouse.Y = -100, -100
			_, _ = model.handleMouse(tea.MouseMotionMsg(mouse))
			mouse.Button = tea.MouseNone
			_, cmd = model.handleMouse(tea.MouseReleaseMsg(mouse))
			runTeaCmds(t, model, cmd)
			p := client.preferences
			ratio := p.HorizontalRatio
			if mode == uipreferences.Up || mode == uipreferences.Down {
				ratio = p.VerticalRatio
			}
			if ratio < 30 || ratio > 70 || p.SubagentLayout != mode {
				t.Fatalf("persisted unbounded ratio=%#v", p)
			}
			_, _ = model.Update(tea.WindowSizeMsg{Width: 40, Height: 16})
			if model.workspaceLayout().split {
				t.Fatal("small terminal did not temporarily use overlay")
			}
			if model.workspace.preferences != p {
				t.Fatal("fallback overwrote preference")
			}
			_, _ = model.Update(tea.WindowSizeMsg{Width: 160, Height: 48})
			if !model.workspaceLayout().split {
				t.Fatal("split did not restore")
			}
		})
	}
}
func TestSubagentWorkspaceDraftFocusAndSendAreBoundToChild(t *testing.T) {
	model, client := newPaneTestModel(t)
	runTeaCmds(t, model, model.setSubagentLayout(uipreferences.Right))
	_, _ = model.handlePaste(tea.PasteMsg{Content: "你好 child\nsecond line"})
	breeze := model.subagentOutputOverlay
	model.scrollSubagentOutputOverlay(-8)
	_ = model.View()
	offset := breeze.offset
	cmd := model.submitPanePrompt()
	if cmd == nil {
		t.Fatal("no send command")
	}
	model.openSubagentOutputOverlayView("spawn-omar", model.subagentOutputViews["spawn-omar"])
	_, _ = model.handlePaste(tea.PasteMsg{Content: "omar draft"})
	_, follow := model.Update(cmd())
	if len(client.requests) != 1 || client.requests[0].TaskID != "task-breeze" || client.requests[0].ParticipantID != "participant-breeze" || client.requests[0].Input != "你好 child\nsecond line" {
		t.Fatalf("send retargeted=%#v", client.requests)
	}
	if model.subagentOutputOverlay.editor.Value() != "omar draft" || !strings.HasPrefix(breeze.inputStatus, "Queued") {
		t.Fatal("completion changed the selected child")
	}
	if follow == nil {
		t.Fatal("queued receipt is not observed")
	}
	_, _ = model.handleKey(tea.KeyPressMsg{Code: tea.KeyF6})
	_, _ = model.handlePaste(tea.PasteMsg{Content: "main draft"})
	if model.textarea.Value() != "main draft" || model.subagentOutputOverlay.editor.Value() != "omar draft" {
		t.Fatal("focus sent input to wrong composer")
	}
	model.openSubagentOutputOverlayView("spawn-breeze", model.subagentOutputViews["spawn-breeze"])
	_ = model.View()
	if breeze.offset != offset || breeze.followTail {
		t.Fatal("switch discarded pinned scroll")
	}
	model.closeSubagentOutputOverlay()
	model.openSubagentWorkspace()
	if model.subagentOutputOverlay != breeze {
		t.Fatal("reopen replaced retained state")
	}
}
func TestSubagentWorkspacePreferencesSerializeLatestChoice(t *testing.T) {
	model, client := newPaneTestModel(t)
	first := model.setSubagentLayout(uipreferences.Left)
	second := model.setSubagentLayout(uipreferences.Up)
	if second != nil {
		t.Fatal("concurrent saves can reorder preferences")
	}
	runTeaCmds(t, model, first)
	if client.preferences.SubagentLayout != uipreferences.Up {
		t.Fatalf("latest preference lost=%#v", client.preferences)
	}
	reopened := NewModel(Config{UIPreferences: client})
	runTeaCmds(t, reopened, reopened.loadPanePreferences())
	if reopened.workspace.preferences != client.preferences {
		t.Fatal("new UI did not load stored preference")
	}
}
func TestSubagentWorkspaceRenderedFrames(t *testing.T) {
	model, _ := newPaneTestModel(t)
	model.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	model.themeCacheKey = ""
	model.syncTextareaChrome()
	var frames []string
	for _, mode := range []uipreferences.Layout{uipreferences.Overlay, uipreferences.Right, uipreferences.Left, uipreferences.Down, uipreferences.Up} {
		model.setSubagentLayout(mode)
		frame := model.View().Content
		lines := strings.Split(frame, "\n")
		if len(lines) != 48 {
			t.Fatalf("%s frame height=%d", mode, len(lines))
		}
		for _, line := range lines {
			if width := displayColumns(line); width != 160 {
				t.Fatalf("%s row width=%d", mode, width)
			}
		}
		frames = append(frames, frame)
		if dir := os.Getenv("CAELIS_PANE_RENDER_DIR"); dir != "" {
			if err := os.WriteFile(filepath.Join(dir, string(mode)+".ansi"), []byte(frame), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	updates := renderFullscreenFramesForTest(t, model.width, model.height, frames...)
	assertPhysicalFullscreenFrame(t, model.width, model.height, frames[len(frames)-1], updates)
}
func BenchmarkSubagentWorkspaceDualFrame(b *testing.B) {
	for _, mode := range []uipreferences.Layout{uipreferences.Overlay, uipreferences.Right, uipreferences.Down} {
		b.Run(string(mode), func(b *testing.B) {
			model := newSubagentOutputPerformanceModel(b, 180, 52, 12)
			model.setSubagentLayout(mode)
			_ = model.View()
			b.ReportAllocs()
			index := 0
			for b.Loop() {
				subagentPerformanceWheel(model, index)
				if index%8 == 0 {
					appendSubagentPerformanceDelta(model, " 新增输出")
				}
				_ = model.View()
				index++
			}
		})
	}
}
func BenchmarkSubagentWorkspaceDividerDrag(b *testing.B) {
	model := newSubagentOutputPerformanceModel(b, 180, 52, 12)
	model.setSubagentLayout(uipreferences.Right)
	_ = model.View()
	r := model.workspaceLayout().divider
	_, _ = model.Update(tea.MouseClickMsg{X: r.x, Y: r.y, Button: tea.MouseLeft})
	index := 0
	b.ReportAllocs()
	for b.Loop() {
		_, _ = model.Update(tea.MouseMotionMsg{X: 75 + index%30, Y: 10, Button: tea.MouseLeft})
		_ = model.View()
		index++
	}
}

func TestSubagentWorkspaceActivityUsesChildObservations(t *testing.T) {
	m, _ := newPaneTestModel(t)
	view := m.subagentOutputViews["spawn-breeze"]
	view.observeChildEvent(TranscriptEvent{Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: "active", NarrativeKind: TranscriptNarrativeReasoning, Text: "thinking", Observation: true})
	if got := view.activity.visible(true).Phase; got != runningPhaseThinking {
		t.Fatalf("observed child phase=%v", got)
	}
	view.observeChildEvent(TranscriptEvent{Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: "next", NarrativeKind: TranscriptNarrativeAssistant, Text: "answer", Observation: true})
	view.observeChildEvent(TranscriptEvent{Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: "active", NarrativeKind: TranscriptNarrativeReasoning, Text: "late history", Observation: true})
	if got := view.activity.visible(true).Phase; got != runningPhaseResponding {
		t.Fatalf("history changed current activity=%v", got)
	}
}
func TestSubagentWorkspaceDropdownPinsMenuIdentity(t *testing.T) {
	m, _ := newPaneTestModel(t)
	m.openSubagentOutputOverlayView("spawn-omar", m.subagentOutputViews["spawn-omar"])
	m.openPaneMenu("agents")
	state := m.subagentOutputOverlay
	if got := m.paneMenuItems(state)[state.menuIndex].value; got != "spawn-omar" {
		t.Fatalf("current selection=%q", got)
	}
	before := append([]paneMenuItem(nil), m.paneMenuItems(state)...)
	descriptor := m.subagentRosterTasks["spawn-breeze"]
	descriptor.Running = false
	descriptor.State = "completed"
	m.subagentRosterTasks["spawn-breeze"] = descriptor
	_ = m.renderPaneMenu()
	for i, item := range m.paneMenuItems(state) {
		if item.value != before[i].value {
			t.Fatal("directory reorder retargeted open menu")
		}
	}
	m.activatePaneMenu()
	if m.subagentOutputOverlay.callID != "spawn-omar" {
		t.Fatal("menu selected wrong child")
	}
}

func TestSubagentWorkspaceResizeEndsDragAndFocusesFallback(t *testing.T) {
	m, client := newPaneTestModel(t)
	runTeaCmds(t, m, m.setSubagentLayout(uipreferences.Right))
	m.workspace.childFocused = false
	r := m.workspaceLayout().divider
	_, _ = m.Update(tea.MouseClickMsg{X: r.x, Y: r.y, Button: tea.MouseLeft})
	_, _ = m.Update(tea.MouseMotionMsg{X: 95, Y: 10, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 50, Height: 18})
	runTeaCmds(t, m, cmd)
	if m.workspace.dragging || !m.workspace.childFocused || m.workspaceLayout().split {
		t.Fatal("resize left a hidden focus or active divider")
	}
	_, _ = m.handlePaste(tea.PasteMsg{Content: "visible child"})
	if m.subagentOutputOverlay.editor.Value() != "visible child" || m.textarea.Value() != "" {
		t.Fatal("fallback paste entered hidden main composer")
	}
	if client.preferences != m.workspace.preferences {
		t.Fatal("terminal resize changed the committed ratio")
	}
}
