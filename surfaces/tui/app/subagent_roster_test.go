package tuiapp

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	protocoltaskstream "github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestSubagentRosterFooterSummarizesActiveAndTerminalStates(t *testing.T) {
	t.Parallel()

	model := newSubagentRosterTestModel()
	model.statusView.Tokens = "41k / 128k"
	addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: audit ownership", "running", time.Unix(100, 0), time.Time{})
	addSubagentRosterTestView(model, "spawn-milo", "milo", "milo[breeze]: trace overlay interaction", "running", time.Unix(110, 0), time.Time{})
	addSubagentRosterTestView(model, "spawn-sena", "sena", "sena[zenith]: verify metadata", "completed", time.Unix(90, 0), time.Unix(120, 0))

	footer := ansi.Strip(model.footerRowText())
	if !strings.Contains(footer, "• 2 running") {
		t.Fatalf("footer omitted running count:\n%q", footer)
	}
	for _, unwanted := range []string{"2/3", "3 done", "idle"} {
		if strings.Contains(strings.ToLower(footer), strings.ToLower(unwanted)) {
			t.Fatalf("active footer contains %q, want only the running count:\n%q", unwanted, footer)
		}
	}

	model.subagentOutputViews["spawn-rhea"].block.SetStatus("completed", "", "", time.Unix(130, 0))
	model.subagentOutputViews["spawn-milo"].block.SetStatus("completed", "", "", time.Unix(140, 0))
	footer = ansi.Strip(model.footerRowText())
	if !strings.Contains(footer, "• 3 done") {
		t.Fatalf("completed workspaces are no longer discoverable:\n%q", footer)
	}

	addSubagentRosterTestView(model, "spawn-nora", "nora", "nora[breeze]: inspect again", "running", time.Unix(150, 0), time.Time{})
	footer = ansi.Strip(model.footerRowText())
	if !strings.Contains(footer, "• 1 running") || strings.Contains(footer, "done") {
		t.Fatalf("new activity did not restore the active summary:\n%q", footer)
	}
}

func TestSubagentRosterFooterUsesRunningStateLabel(t *testing.T) {
	t.Parallel()

	model := newSubagentRosterTestModel()
	addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: audit ownership", "running", time.Unix(100, 0), time.Time{})

	if footer := ansi.Strip(model.footerRowText()); !strings.Contains(footer, "• 1 running") {
		t.Fatalf("footer omitted running state label: %q", footer)
	}
}

func TestSubagentRosterFooterRunningDotBreathesGreen(t *testing.T) {
	model := NewModel(Config{Workspace: "caelis"})
	model.width = 100
	model.height = 32
	model.ready = true
	model.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	model.themeCacheKey = ""
	addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: audit ownership", "running", time.Unix(100, 0), time.Time{})

	model.spinner.Spinner.Frames = []string{runningSpinnerFrames[0]}
	bright := model.renderFooterSubagentText(model.footerSubagentText())
	model.spinner.Spinner.Frames = []string{runningSpinnerFrames[len(runningSpinnerFrames)/2]}
	dim := model.renderFooterSubagentText(model.footerSubagentText())
	if bright == dim {
		t.Fatal("running subagent footer dot did not change breathing phase")
	}
	successFG := sgrForegroundCode(t, model.theme.Tokens().Success.GetForeground())
	if got := normalizeInlineStyleText(textWithSGRForeground(bright, successFG)); got != "•" {
		t.Fatalf("success foreground covered %q, want only the running dot", got)
	}
	if !model.subagentOutputPulseActive() || !model.animationIndicatorActive() {
		t.Fatal("running roster did not activate footer breathing animation")
	}

	model.subagentOutputViews["spawn-rhea"].block.SetStatus("completed", "", "", time.Unix(110, 0))
	model.spinner.Spinner.Frames = []string{runningSpinnerFrames[0]}
	terminalBright := model.renderFooterSubagentText(model.footerSubagentText())
	model.spinner.Spinner.Frames = []string{runningSpinnerFrames[len(runningSpinnerFrames)/2]}
	terminalDim := model.renderFooterSubagentText(model.footerSubagentText())
	if terminalBright != terminalDim {
		t.Fatal("terminal subagent footer dot retained a breathing phase")
	}
	if model.subagentOutputPulseActive() || model.animationIndicatorActive() {
		t.Fatal("terminal-only roster retained the running animation")
	}
}

func TestSubagentRosterMetadataFallsBackToParticipant(t *testing.T) {
	t.Parallel()

	handle, binding := subagentRosterMetadata(&subagentOutputView{title: "inspect workspace"})
	if handle != "Participant" || binding != "" {
		t.Fatalf("fallback identity = %q %q, want Participant", handle, binding)
	}
	if got := subagentTranscriptActor(TranscriptEvent{}); got != "Participant" {
		t.Fatalf("subagentTranscriptActor() = %q, want Participant", got)
	}
}

func TestSubagentRosterOmitsSpawnWithoutChildHandle(t *testing.T) {
	t.Parallel()

	model := newSubagentRosterTestModel()
	view := model.ensureSubagentOutputView("spawn-failed")
	view.title = "reviewer: unavailable child"
	view.block.Status = "running"
	if got := model.subagentRosterCount(); got != 0 {
		t.Fatalf("roster count = %d, want unresolved Spawn excluded", got)
	}
	if text := strings.TrimSpace(model.footerSubagentText()); text != "" {
		t.Fatalf("unresolved Spawn produced footer affordance: %q", model.footerRowText())
	}
	if model.openSubagentWorkspace() {
		t.Fatal("unresolved Spawn opened an empty roster")
	}
	if cmd := model.ensureSubagentDirectoryWatch(); cmd != nil {
		t.Fatal("unresolved Spawn started a Task directory watch")
	}
}

func TestSubagentRosterFooterCompactsToIdentifiableMarker(t *testing.T) {
	t.Parallel()

	left, subagents, right := fitStatusFooterParts(
		16,
		"gpt-5.6 · caelis",
		"• 2 running",
		"• 2",
		"41k / 128k",
	)
	if subagents != "• 2" {
		t.Fatalf("subagent footer = %q, want compact active summary • 2", subagents)
	}
	if right != "41k / 128k" {
		t.Fatalf("context footer = %q, want context preserved", right)
	}
	if got := displayColumns(composeStatusFooter(16, left, subagents, right)); got != 16 {
		t.Fatalf("compacted footer width = %d, want 16", got)
	}
}

func TestSubagentRosterMenuOnlyShowsIdentity(t *testing.T) {
	m := newSubagentRosterTestModel()
	now := time.Date(2026, time.August, 4, 15, 0, 0, 0, time.Local)
	addSubagentRosterTestView(m, "spawn-rhea", "rhea", "rhea[reviewer]: audit ownership", "running", now.Add(-time.Minute), time.Time{})
	addSubagentRosterTestView(m, "spawn-sena", "sena", "sena[zenith]: verify metadata", "completed", now.Add(-10*time.Minute), now)
	m.openSubagentWorkspace()
	m.openPaneMenu("agents")
	plain := ansi.Strip(m.renderPaneMenu())
	lines := strings.Split(plain, "\n")
	if len(lines) != 4 || strings.Trim(lines[1], "│ ") != "rhea[reviewer]" || strings.Trim(lines[2], "│ ") != "sena[zenith]" {
		t.Fatalf("menu should contain only identities: %q", plain)
	}
	if width := m.subagentOutputOverlay.menuRect.width; width != displayColumns("rhea[reviewer]")+4 {
		t.Fatalf("menu width=%d, want longest identity plus padding and border", width)
	}
}

func TestSubagentRosterSelectedRowUsesSlashSelectionSurface(t *testing.T) {
	model := newSubagentRosterTestModel()
	model.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	now := time.Date(2026, time.August, 4, 15, 0, 0, 0, time.Local)
	addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: audit task-stream ownership", "running", now.Add(-2*time.Minute), time.Time{})
	addSubagentRosterTestView(model, "spawn-milo", "milo", "milo[breeze]: trace overlay interaction", "running", now.Add(-time.Minute), time.Time{})

	rows := model.subagentRosterRows()
	var lines []string
	for i, row := range rows {
		lines = append(lines, model.renderSubagentRosterRow(row, i == 0, 88))
	}
	selected := subagentRosterLineForHandle(t, lines, "rhea")
	unselected := subagentRosterLineForHandle(t, lines, "milo")
	selectionBG := sgrBackgroundCode(t, model.theme.SelectionBg)
	if !strings.Contains(selected, selectionBG) {
		t.Fatalf("selected roster row omitted selection background %q: %q", selectionBG, selected)
	}
	if strings.Contains(unselected, selectionBG) {
		t.Fatalf("unselected roster row used selection background %q: %q", selectionBG, unselected)
	}
	bindingFG := sgrForegroundCode(t, model.theme.MutedText)
	if got := normalizeInlineStyleText(textWithSGRForeground(unselected, bindingFG)); got != "[breeze]" {
		t.Fatalf("binding did not use low-contrast text: %q", got)
	}
}

func TestSubagentRosterHidesRedundantSelfBinding(t *testing.T) {
	t.Parallel()

	if got := subagentRosterBindingText("self"); got != "" {
		t.Fatalf("self binding = %q, want hidden", got)
	}
	if got := subagentRosterBindingText("reviewer"); got != "[reviewer]" {
		t.Fatalf("reviewer binding = %q, want [reviewer]", got)
	}
}

func TestSubagentRosterEnterOpensRetainedWorkspaceWithoutTranscriptOwner(t *testing.T) {
	t.Parallel()

	model := newSubagentRosterTestModel()
	addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: audit ownership", "running", time.Unix(100, 0), time.Time{})
	if !model.openSubagentWorkspace() {
		t.Fatal("retained workspace did not open")
	}

	if model.subagentOutputOverlay == nil || model.subagentOutputOverlay.callID != "spawn-rhea" {
		t.Fatalf("output overlay = %#v, want retained spawn-rhea workspace", model.subagentOutputOverlay)
	}
}

func TestSubagentRosterFooterAndRowsAreMouseNavigable(t *testing.T) {
	model := newSubagentRosterTestModel()
	addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: audit ownership", "running", time.Unix(100, 0), time.Time{})
	addSubagentRosterTestView(model, "spawn-sena", "sena", "sena[zenith]: verify metadata", "completed", time.Unix(90, 0), time.Unix(120, 0))
	model.ensureViewportLayout()
	_ = model.View()

	bounds, ok := model.subagentRosterFooterHitBounds()
	if !ok {
		t.Fatal("footer omitted subagent hit bounds")
	}
	point := tea.Mouse{X: bounds.x + 1, Y: bounds.y, Button: tea.MouseLeft}
	_, _ = model.handleMouse(tea.MouseClickMsg(point))
	point.Button = tea.MouseNone
	_, _ = model.handleMouse(tea.MouseReleaseMsg(point))
	if model.subagentOutputOverlay == nil || model.subagentOutputOverlay.callID != "spawn-rhea" {
		t.Fatal("footer did not directly open running child")
	}
	state := model.subagentOutputOverlay
	_ = model.View()
	point = tea.Mouse{X: state.geometry.contentX + 2, Y: state.geometry.headerY, Button: tea.MouseLeft}
	_, _ = model.handleMouse(tea.MouseClickMsg(point))
	point.Button = tea.MouseNone
	_, _ = model.handleMouse(tea.MouseReleaseMsg(point))
	_ = model.View()
	if state.menu != "agents" {
		t.Fatal("title did not open agent menu")
	}
	point = tea.Mouse{X: state.menuRect.x + 2, Y: state.menuRect.y + state.menuInset + 1, Button: tea.MouseLeft}
	_, _ = model.handleMouse(tea.MouseClickMsg(point))
	point.Button = tea.MouseNone
	_, _ = model.handleMouse(tea.MouseReleaseMsg(point))
	if model.subagentOutputOverlay == nil || model.subagentOutputOverlay.callID != "spawn-sena" {
		t.Fatal("dropdown did not switch the single pane")
	}

}

func TestSubagentRosterFooterReopensLastPane(t *testing.T) {
	model := newSubagentRosterTestModel()
	addSubagentRosterTestView(model, "spawn-sena", "sena", "sena[self]: verify metadata", "completed", time.Unix(90, 0), time.Unix(120, 0))
	model.ensureViewportLayout()
	_ = model.View()
	bounds, ok := model.subagentRosterFooterHitBounds()
	if !ok {
		t.Fatal("missing footer")
	}
	clickSubagentRosterFooter(t, model, bounds)
	if model.subagentOutputOverlay == nil {
		t.Fatal("footer did not open pane")
	}
	state := model.subagentOutputOverlay
	state.editor.SetValue("retained draft")
	model.closeSubagentOutputOverlay()
	_ = model.View()
	clickSubagentRosterFooter(t, model, bounds)
	if model.subagentOutputOverlay != state || state.editor.Value() != "retained draft" {
		t.Fatal("reopen discarded pane state")
	}
}

func TestSubagentDirectorySnapshotUsesTerminalStateWithoutMutatingWorkspace(t *testing.T) {
	t.Parallel()

	endedAt := time.Date(2026, time.August, 4, 14, 57, 0, 0, time.Local)
	service := &subagentRosterTestTaskStreamService{list: protocoltaskstream.ListResult{Tasks: []protocoltaskstream.TaskDescriptor{{
		SessionID: "session-1", TaskID: "task-1", Handle: "rhea", Kind: task.KindSubagent,
		State: task.StateCompleted, Running: false, UpdatedAt: endedAt,
		ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-rhea", ToolName: "StartThread"},
	}}}}
	model := NewModel(Config{
		NoColor: true, NoAnimation: true, TaskStreams: bindTaskStreamTestClient(t, service),
	})
	model.currentSessionID = "session-1"
	view := addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: audit ownership", "running", time.Unix(100, 0), time.Time{})

	applySubagentDirectorySnapshotForTest(model, 1, service.list.Tasks)
	if got := model.subagentRosterRunningCount(); got != 0 {
		t.Fatalf("running count = %d, want terminal directory to close stale hidden view", got)
	}
	rows := model.subagentRosterRows()
	if len(rows) != 1 || !rows[0].endedAt.Equal(endedAt) {
		t.Fatalf("terminal roster row = %#v", rows)
	}
	if view.block.Status != "running" || !view.block.EndedAt.IsZero() {
		t.Fatalf("directory refresh mutated retained child workspace: status=%q ended=%v", view.block.Status, view.block.EndedAt)
	}
}

func TestSubagentRosterResumeLetsTerminalDirectorySupersedeHistoricalSpawn(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 4, 20, 40, 57, 0, time.Local)
	endedAt := startedAt.Add(12 * time.Second)
	callIDs := []string{"spawn-ravi", "spawn-tari", "spawn-inez"}
	handles := []string{"ravi", "tari", "inez"}
	descriptors := make([]protocoltaskstream.TaskDescriptor, 0, len(callIDs))
	for index := range callIDs {
		descriptors = append(descriptors, protocoltaskstream.TaskDescriptor{
			SessionID: "session-old", TaskID: "task-" + handles[index], Handle: handles[index], Kind: task.KindSubagent,
			State: task.StateCompleted, Running: false, CurrentTurnID: "task-" + handles[index] + ":1", UpdatedAt: endedAt,
			ParentTool: protocoltaskstream.ParentTool{ToolCallID: callIDs[index], ToolName: "StartThread"},
		})
	}
	service := &subagentRosterTestTaskStreamService{list: protocoltaskstream.ListResult{Tasks: descriptors}}
	model := NewModel(Config{
		NoColor: true, NoAnimation: true, Workspace: "caelis",
		TaskStreams: bindTaskStreamTestClient(t, service),
	})
	model.currentSessionID = "session-old"
	model.width = 100
	model.height = 32
	model.ready = true

	for index := range callIDs {
		model.observeSubagentOutputEvents([]TranscriptEvent{{
			Kind: TranscriptEventTool, Scope: ACPProjectionMain, OccurredAt: startedAt,
			ToolCallID: callIDs[index], ToolName: "StartThread", ToolTaskHandle: handles[index],
			ToolArgs: handles[index] + "[breeze]: historical task",
		}})
		view := model.subagentOutputViews[callIDs[index]]
		if view == nil || !view.block.StartedAt.Equal(startedAt) {
			t.Fatalf("replayed %s start = %v, want historical %v", handles[index], view.block.StartedAt, startedAt)
		}
	}
	if got := model.subagentRosterRunningCount(); got != 3 {
		t.Fatalf("pre-directory running count = %d, want provisional 3", got)
	}

	applySubagentDirectorySnapshotForTest(model, 1, descriptors)
	if got := model.subagentRosterRunningCount(); got != 0 {
		t.Fatalf("resumed running count = %d, want terminal directory to win", got)
	}
	if footer := ansi.Strip(model.footerRowText()); !strings.Contains(footer, "• 3 done") || strings.Contains(footer, "running") {
		t.Fatalf("resumed footer retained stale running state: %q", footer)
	}

	view := model.subagentOutputViews[callIDs[0]]
	plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 88, 20)), "\n")
	if strings.TrimSpace(plain) != "" {
		t.Fatalf("terminal empty workspace rendered a synthetic message:\n%s", plain)
	}
}

func TestSubagentRosterColdResumeLetsDirectorySupersedeFreshReplayShell(t *testing.T) {
	t.Parallel()

	endedAt := time.Unix(100, 0)
	service := &subagentRosterTestTaskStreamService{list: protocoltaskstream.ListResult{Tasks: []protocoltaskstream.TaskDescriptor{{
		SessionID: "session-old", TaskID: "task-kira", Handle: "kira", Kind: task.KindSubagent,
		State: task.StateCompleted, Running: false, CurrentTurnID: "task-kira:1", UpdatedAt: endedAt,
		ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-kira", ToolName: "StartThread"},
	}}}}
	model := NewModel(Config{
		NoColor: true, NoAnimation: true, Workspace: "caelis",
		TaskStreams: bindTaskStreamTestClient(t, service),
	})
	model.currentSessionID = "session-old"
	model.observeSubagentOutputEvents([]TranscriptEvent{{
		Kind: TranscriptEventTool, Scope: ACPProjectionMain,
		ToolCallID: "spawn-kira", ToolName: "StartThread", ToolTaskHandle: "kira",
		ToolArgs: "kira[self]: historical task",
	}})
	view := model.subagentOutputViews["spawn-kira"]
	if view == nil || !view.block.StartedAt.After(endedAt) {
		t.Fatalf("cold replay shell start = %v, want a fresh local timestamp after terminal Task", view.block.StartedAt)
	}

	applySubagentDirectorySnapshotForTest(model, 1, service.list.Tasks)
	if got := model.subagentRosterRunningCount(); got != 0 {
		t.Fatalf("cold resumed running count = %d, want terminal directory to own provisional shell", got)
	}
	if status, _, gotEndedAt := model.subagentRosterViewState("spawn-kira", view); status == subagentOutputRunning || !gotEndedAt.Equal(endedAt) {
		t.Fatalf("cold resumed state = (%v, %v), want terminal at %v", status, gotEndedAt, endedAt)
	}
}

func TestSubagentDirectoryTracksRepeatedActivityWithoutMutatingWorkspace(t *testing.T) {
	t.Parallel()

	oldEndedAt := time.Date(2026, time.August, 4, 14, 57, 0, 0, time.Local)
	restartedAt := oldEndedAt.Add(time.Minute)
	continuedEndedAt := restartedAt.Add(2 * time.Minute)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	view := addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: audit ownership", "completed", oldEndedAt.Add(-time.Minute), oldEndedAt)
	view.block.SessionID = "turn-1"

	running := transcriptTaskDescriptor("turn-2", task.StateRunning, true, restartedAt)
	running.ActivityID = "activity-2"
	applySubagentDirectorySnapshotForTest(model, 1, []protocoltaskstream.TaskDescriptor{running})
	rows := model.subagentRosterRows()
	if len(rows) != 1 || rows[0].status != subagentOutputRunning || !rows[0].startedAt.Equal(restartedAt) {
		t.Fatalf("continued running row = %#v", rows)
	}

	completed := transcriptTaskDescriptor("turn-2", task.StateCompleted, false, continuedEndedAt)
	completed.ActivityID = "activity-2"
	applySubagentDirectorySnapshotForTest(model, 2, []protocoltaskstream.TaskDescriptor{completed})
	if got := model.subagentRosterRunningCount(); got != 0 {
		t.Fatalf("running count = %d, want completed child idle", got)
	}
	rows = model.subagentRosterRows()
	if len(rows) != 1 || rows[0].status != subagentOutputSucceeded || !rows[0].endedAt.Equal(continuedEndedAt) {
		t.Fatalf("continued terminal row = %#v", rows)
	}

	third := transcriptTaskDescriptor("turn-3", task.StateRunning, true, continuedEndedAt.Add(time.Minute))
	third.ActivityID = "activity-3"
	applySubagentDirectorySnapshotForTest(model, 3, []protocoltaskstream.TaskDescriptor{third})
	if got := model.subagentRosterRunningCount(); got != 1 {
		t.Fatalf("third activity running count = %d, want 1", got)
	}
	if view.block.SessionID != "turn-1" || view.block.Status != "completed" || !view.block.EndedAt.Equal(oldEndedAt) {
		t.Fatalf("directory snapshots mutated retained workspace: turn=%q status=%q ended=%v", view.block.SessionID, view.block.Status, view.block.EndedAt)
	}
}

func TestSubagentDirectoryClosedRetriesWithoutContentSubscription(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: audit", "running", time.Unix(100, 0), time.Time{})
	model.subagentDirectoryGeneration = 7
	model.subagentDirectoryRevision = 12
	cmd := model.handleSubagentDirectoryClosed(subagentDirectoryClosedMsg{
		sessionID: "session-1", generation: 7,
		err: errorcode.New(errorcode.Unavailable, "status stream interrupted"),
	})
	if cmd == nil || !model.subagentDirectoryRetryScheduled || model.subagentDirectoryRetries != 1 {
		t.Fatalf("retry state = cmd:%v scheduled:%v attempts:%d", cmd != nil, model.subagentDirectoryRetryScheduled, model.subagentDirectoryRetries)
	}
	if len(model.taskStreamSubscriptions) != 0 {
		t.Fatal("Task directory retry created a child content subscription")
	}
	if model.subagentDirectoryGeneration != 8 || model.subagentDirectoryRevision != 0 {
		t.Fatalf("closed directory boundary = generation %d revision %d, want 8/0", model.subagentDirectoryGeneration, model.subagentDirectoryRevision)
	}

	// A queued snapshot from the closed connection must not repopulate the
	// model after the boundary was invalidated.
	model.handleSubagentDirectorySnapshot(subagentDirectorySnapshotMsg{
		sessionID: "session-1", generation: 7,
		snapshot: protocoltaskstream.DirectorySnapshot{Revision: 13, Tasks: []protocoltaskstream.TaskDescriptor{
			transcriptTaskDescriptor("stale-turn", task.StateCompleted, false, time.Unix(130, 0)),
		}},
	})
	if model.subagentDirectoryRevision != 0 {
		t.Fatalf("stale closed-generation revision = %d, want ignored", model.subagentDirectoryRevision)
	}

	// Starting the replacement watch allocates its own generation. Its complete
	// initial snapshot may legitimately restart at revision 1.
	model.subagentDirectoryGeneration++
	model.handleSubagentDirectorySnapshot(subagentDirectorySnapshotMsg{
		sessionID: "session-1", generation: model.subagentDirectoryGeneration,
		snapshot: protocoltaskstream.DirectorySnapshot{Revision: 1, Tasks: []protocoltaskstream.TaskDescriptor{
			transcriptTaskDescriptor("turn-after-reconnect", task.StateRunning, true, time.Unix(140, 0)),
		}},
	})
	if model.subagentDirectoryRevision != 1 || model.subagentRosterRunningCount() != 1 {
		t.Fatalf("replacement directory = revision %d running %d, want 1/1", model.subagentDirectoryRevision, model.subagentRosterRunningCount())
	}
}

func TestSubagentDirectoryNewActivityReopensVisibleContentBeforeParentFinal(t *testing.T) {
	t.Parallel()

	requests := make(chan protocoltaskstream.SubscribeRequest, 1)
	subscription := newTUIProtocolTaskSubscription()
	service := &subagentRosterTestTaskStreamService{subscribeRequests: requests, subscription: subscription}
	messages := make(chan tea.Msg, 8)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	model := NewModel(Config{
		Context: context.Background(), NoColor: true, NoAnimation: true,
		TaskStreams: bindTaskStreamTestClient(t, service), ProgramSender: sender,
	})
	model.currentSessionID = "session-1"
	view := addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: continue audit", "completed", time.Unix(90, 0), time.Unix(100, 0))
	view.block.SessionID = "turn-1"
	view.turnID = "turn-1"
	view.historyResolved = true
	view.directoryActivityID = "activity:activity-1"
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-rhea"}
	model.taskStreamHandlesByID["task-1"] = "rhea"
	model.taskStreamIDsByCallID["spawn-rhea"] = "task-1"
	model.taskStreamCallIDsByID["task-1"] = "spawn-rhea"
	initial := transcriptTaskDescriptor("turn-1", task.StateCompleted, false, time.Unix(100, 0))
	initial.ActivityID = "activity-1"
	applySubagentDirectorySnapshotForTest(model, 1, []protocoltaskstream.TaskDescriptor{initial})
	if demand := model.taskStreamDemandForOwner("spawn-rhea", "rhea"); demand != taskStreamDemandVisibleSubagent {
		t.Fatalf("idle cached demand = %v, want visible observation ownership", demand)
	}

	running := transcriptTaskDescriptor("turn-2", task.StateRunning, true, time.Unix(110, 0))
	running.ActivityID = "activity-2"
	applySubagentDirectorySnapshotForTest(model, 2, []protocoltaskstream.TaskDescriptor{running})
	select {
	case request := <-requests:
		if request.TaskID != "task-1" || !request.Follow {
			t.Fatalf("new activity Subscribe request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("new child activity waited for the parent SendMessage Final before subscribing")
	}
	opened := receiveTUITaskStreamMessage[taskStreamOpenedMsg](t, messages)
	if next, _ := model.Update(opened); next != nil {
		model = next.(*Model)
	}
	base := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-2",
		Scope: eventstream.ScopeSubagent, ScopeID: "task-1", OccurredAt: time.Unix(111, 0),
		Delivery:   &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-rhea", ToolName: "StartThread"},
	}
	first := base
	first.Cursor = "cursor-turn-2-1"
	first.Update = eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "answer-turn-2",
		Content: eventstream.TextContent{Type: "text", Text: "Here's the "},
	}
	second := base
	second.Cursor = "cursor-turn-2-2"
	second.Update = eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "answer-turn-2",
		Content: eventstream.TextContent{Type: "text", Text: "result:"},
	}
	go func() {
		subscription.events <- first
		subscription.events <- second
	}()
	for model.taskStreamCursors["task-1"] != second.Cursor {
		batch := receiveTUITaskStreamMessage[taskStreamBatchMsg](t, messages)
		if next, _ := model.Update(batch); next != nil {
			model = next.(*Model)
		}
	}
	plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 20)), "\n")
	if !strings.Contains(plain, "Here's the result:") || strings.Contains(plain, "Here's the Here's the") {
		t.Fatalf("second activity streaming projection = %q, want both chunks before parent Final without overlap", plain)
	}
	model.closeSubagentOutputOverlay()
}

func applySubagentDirectorySnapshotForTest(
	model *Model,
	revision uint64,
	tasks []protocoltaskstream.TaskDescriptor,
) tea.Cmd {
	if model.subagentDirectoryGeneration == 0 {
		model.subagentDirectoryGeneration = 1
	}
	return model.handleSubagentDirectorySnapshot(subagentDirectorySnapshotMsg{
		sessionID:  model.currentSessionID,
		generation: model.subagentDirectoryGeneration,
		snapshot:   protocoltaskstream.DirectorySnapshot{Revision: revision, Tasks: tasks},
	})
}

func transcriptTaskDescriptor(turnID string, state task.State, running bool, updatedAt time.Time) protocoltaskstream.TaskDescriptor {
	return protocoltaskstream.TaskDescriptor{
		SessionID: "session-1", TaskID: "task-1", Handle: "rhea", Kind: task.KindSubagent,
		State: state, Running: running, CurrentTurnID: turnID, UpdatedAt: updatedAt,
		ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-rhea", ToolName: "StartThread"},
	}
}

func newSubagentRosterTestModel() *Model {
	model := NewModel(Config{NoColor: true, NoAnimation: true, Workspace: "caelis"})
	model.width = 100
	model.height = 32
	model.ready = true
	return model
}

func addSubagentRosterTestView(
	model *Model,
	callID string,
	handle string,
	title string,
	status string,
	startedAt time.Time,
	endedAt time.Time,
) *subagentOutputView {
	view := model.ensureSubagentOutputView(callID)
	view.taskHandle = handle
	view.title = title
	view.block.Status = status
	view.block.StartedAt = startedAt
	view.block.EndedAt = endedAt
	return view
}

func subagentRosterLineForHandle(t *testing.T, lines []string, handle string) string {
	t.Helper()
	for _, line := range lines {
		if strings.Contains(ansi.Strip(line), handle) {
			return line
		}
	}
	t.Fatalf("missing %s row", handle)
	return ""
}

func clickSubagentRosterFooter(t *testing.T, model *Model, bounds subagentRosterFooterBounds) {
	t.Helper()
	point := tea.Mouse{X: bounds.x + minInt(1, bounds.width-1), Y: bounds.y, Button: tea.MouseLeft}
	_, _ = model.handleMouse(tea.MouseClickMsg(point))
	point.Button = tea.MouseNone
	_, _ = model.handleMouse(tea.MouseReleaseMsg(point))
}

type subagentRosterTestTaskStreamService struct {
	list              protocoltaskstream.ListResult
	listErrors        []error
	listCalls         int
	eventRequests     chan protocoltaskstream.ReadRequest
	eventBatch        protocoltaskstream.ReadResult
	eventErr          error
	subscribeRequests chan protocoltaskstream.SubscribeRequest
	subscription      protocoltaskstream.Subscription
}

func (s *subagentRosterTestTaskStreamService) List(context.Context, protocoltaskstream.Principal, protocoltaskstream.ListRequest) (protocoltaskstream.ListResult, error) {
	index := s.listCalls
	s.listCalls++
	if index < len(s.listErrors) && s.listErrors[index] != nil {
		return protocoltaskstream.ListResult{}, s.listErrors[index]
	}
	return s.list, nil
}

func (s *subagentRosterTestTaskStreamService) Events(_ context.Context, _ protocoltaskstream.Principal, request protocoltaskstream.ReadRequest) (protocoltaskstream.ReadResult, error) {
	if s.eventRequests != nil {
		s.eventRequests <- request
	}
	batch := s.eventBatch
	if batch.ActivityID == "" {
		batch.ActivityID = request.ExpectedActivityID
	}
	if request.Cursor != "" {
		batch.Deliveries = nil
	}
	return batch, s.eventErr
}

func (s *subagentRosterTestTaskStreamService) Subscribe(_ context.Context, _ protocoltaskstream.Principal, request protocoltaskstream.SubscribeRequest) (protocoltaskstream.SubscribeResult, error) {
	if s.subscribeRequests != nil {
		s.subscribeRequests <- request
	}
	return protocoltaskstream.SubscribeResult{Subscription: s.subscription}, nil
}
