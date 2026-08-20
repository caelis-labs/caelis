package tuiapp

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	protocoltaskstream "github.com/caelis-labs/caelis/protocol/acp/taskstream"
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
	if model.openSubagentRosterOverlay() {
		t.Fatal("unresolved Spawn opened an empty roster")
	}
	if cmd := model.requestSubagentRosterRefresh(); cmd != nil {
		t.Fatal("unresolved Spawn started Task directory polling")
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

func TestSubagentRosterRowsKeepPromptAndTimeOnSingleLine(t *testing.T) {
	t.Parallel()

	model := newSubagentRosterTestModel()
	now := time.Date(2026, time.August, 4, 15, 0, 0, 0, time.Local)
	addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: audit task-stream ownership", "running", now.Add(-2*time.Minute-41*time.Second), time.Time{})
	addSubagentRosterTestView(model, "spawn-milo", "milo", "milo[breeze]: trace overlay interaction", "running", now.Add(-time.Minute-18*time.Second), time.Time{})
	addSubagentRosterTestView(model, "spawn-sena", "sena", "sena[zenith]: verify metadata contracts", "completed", now.Add(-10*time.Minute), time.Date(2026, time.August, 4, 14, 57, 0, 0, time.Local))

	rows := model.subagentRosterRows()
	model.subagentRosterOverlay = &subagentRosterOverlayState{rows: rows}
	lines, _ := model.renderSubagentRosterRows(rows, 88, 2, now)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	for _, expected := range []string{
		"Running  2",
		"Done  1",
		"rhea  [reviewer]",
		"milo  [breeze]",
		"sena  [zenith]",
		"audit task-stream ownership",
		"trace overlay interaction",
		"verify metadata contracts",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("roster omitted %q:\n%s", expected, plain)
		}
	}
	if !strings.Contains(plain, " • rhea") || !strings.Contains(plain, " ✓ sena") {
		t.Fatalf("roster omitted status markers:\n%s", plain)
	}

	assertSubagentRosterRowEndsWith(t, lines, "rhea", "02:41")
	assertSubagentRosterRowEndsWith(t, lines, "milo", "01:18")
	assertSubagentRosterRowEndsWith(t, lines, "sena", "14:57")
	assertSubagentRosterPromptSharesRow(t, lines, "rhea", "audit task-stream ownership")
	assertSubagentRosterPromptSharesRow(t, lines, "milo", "trace overlay interaction")
	assertSubagentRosterPromptSharesRow(t, lines, "sena", "verify metadata contracts")
	for _, line := range lines {
		if !strings.Contains(line, "rhea") && !strings.Contains(line, "milo") && !strings.Contains(line, "sena") {
			continue
		}
		lower := strings.ToLower(ansi.Strip(line))
		if strings.Contains(lower, "running") || strings.Contains(lower, "idle") || strings.Contains(lower, "last run") {
			t.Fatalf("row repeats lifecycle prose instead of time only: %q", lower)
		}
	}
}

func TestSubagentRosterPromptUsesBoundedMiddleFold(t *testing.T) {
	t.Parallel()

	summary := "You are sub-agent 3 of 3. Perform a detailed ownership audit across the complete runtime projection and return a brief status summary. No edits needed."
	got := subagentRosterPromptText(summary, 200)
	if width := displayColumns(got); width > subagentRosterPromptMaxColumns {
		t.Fatalf("prompt width = %d, want <= %d: %q", width, subagentRosterPromptMaxColumns, got)
	}
	for _, expected := range []string{"You are sub-agent", "...", "No edits needed."} {
		if !strings.Contains(got, expected) {
			t.Fatalf("middle-folded prompt omitted %q: %q", expected, got)
		}
	}
}

func TestSubagentRosterSelectedRowUsesSlashSelectionSurface(t *testing.T) {
	model := newSubagentRosterTestModel()
	model.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	now := time.Date(2026, time.August, 4, 15, 0, 0, 0, time.Local)
	addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: audit task-stream ownership", "running", now.Add(-2*time.Minute), time.Time{})
	addSubagentRosterTestView(model, "spawn-milo", "milo", "milo[breeze]: trace overlay interaction", "running", now.Add(-time.Minute), time.Time{})

	rows := model.subagentRosterRows()
	model.subagentRosterOverlay = &subagentRosterOverlayState{rows: rows}
	lines, _ := model.renderSubagentRosterRows(rows, 88, 2, now)
	selected := subagentRosterLineForHandle(t, lines, "rhea")
	unselected := subagentRosterLineForHandle(t, lines, "milo")
	selectionBG := sgrBackgroundCode(t, model.theme.SelectionBg)
	if !strings.Contains(selected, selectionBG) {
		t.Fatalf("selected roster row omitted selection background %q: %q", selectionBG, selected)
	}
	if strings.Contains(unselected, selectionBG) {
		t.Fatalf("unselected roster row used selection background %q: %q", selectionBG, unselected)
	}
	promptFG := sgrForegroundCode(t, model.theme.HelpHintTextStyle().GetForeground())
	if got := normalizeInlineStyleText(textWithSGRForeground(unselected, promptFG)); !strings.Contains(got, "trace overlay interaction") {
		t.Fatalf("unselected prompt did not use low-contrast text: %q", got)
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
	if !model.openSubagentRosterOverlay() {
		t.Fatal("openSubagentRosterOverlay() = false")
	}
	_ = model.renderSubagentRosterOverlay()
	_ = model.handleSubagentRosterOverlayKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if model.subagentRosterOverlay != nil {
		t.Fatal("roster remained open after workspace activation")
	}
	if model.subagentOutputOverlay == nil || model.subagentOutputOverlay.callID != "spawn-rhea" {
		t.Fatalf("output overlay = %#v, want retained spawn-rhea workspace", model.subagentOutputOverlay)
	}
}

func TestSubagentRosterColdResumeLoadsOnlySelectedWorkspace(t *testing.T) {
	t.Parallel()

	descriptors := []protocoltaskstream.TaskDescriptor{
		{
			SessionID: "session-old", TaskID: "task-kira", Handle: "kira", Kind: task.KindSubagent,
			State: task.StateCompleted, Running: false, UpdatedAt: time.Unix(103, 0),
			ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-kira", ToolName: "Spawn"},
		},
		{
			SessionID: "session-old", TaskID: "task-wen", Handle: "wen", Kind: task.KindSubagent,
			State: task.StateCompleted, Running: false, UpdatedAt: time.Unix(102, 0),
			ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-wen", ToolName: "Spawn"},
		},
		{
			SessionID: "session-old", TaskID: "task-yara", Handle: "yara", Kind: task.KindSubagent,
			State: task.StateCompleted, Running: false, UpdatedAt: time.Unix(101, 0),
			ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-yara", ToolName: "Spawn"},
		},
	}
	requests := make(chan protocoltaskstream.SubscribeRequest, len(descriptors))
	service := &subagentRosterTestTaskStreamService{
		list:              protocoltaskstream.ListResult{Tasks: descriptors},
		subscribeRequests: requests,
		subscription:      newTUIProtocolTaskSubscription(),
	}
	messages := make(chan tea.Msg, 8)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	model := NewModel(Config{
		Context: context.Background(), NoColor: true, NoAnimation: true,
		TaskStreams: bindTaskStreamTestClient(t, service), ProgramSender: sender,
	})
	model.currentSessionID = "session-old"
	model.width = 100
	model.height = 32
	for _, descriptor := range descriptors {
		addSubagentRosterTestView(
			model,
			descriptor.ParentTool.ToolCallID,
			descriptor.Handle,
			descriptor.Handle+"[self]: historical task",
			"running",
			time.Unix(90, 0),
			time.Time{},
		)
	}

	result := requireSubagentRosterRefreshResult(t, model.requestSubagentRosterRefresh())
	model.handleSubagentRosterRefreshResult(result)
	if !model.openSubagentRosterOverlay() {
		t.Fatal("cold roster did not open")
	}
	_ = model.renderSubagentRosterOverlay()
	select {
	case request := <-requests:
		t.Fatalf("metadata-only roster loaded child Task %q before selection", request.TaskID)
	default:
	}
	if !model.activateSubagentRosterSelection() {
		t.Fatal("selected roster row did not open its workspace")
	}
	selectedCallID := model.subagentOutputOverlay.callID
	selectedTaskID := model.subagentRosterTasks[selectedCallID].TaskID
	resolved := receiveTUITaskStreamMessage[taskStreamResolvedMsg](t, messages)
	if next, _ := model.Update(resolved); next != nil {
		model = next.(*Model)
	}
	select {
	case request := <-requests:
		if request.TaskID != selectedTaskID {
			t.Fatalf("selected workspace subscribed Task %q, want %q", request.TaskID, selectedTaskID)
		}
	case <-time.After(time.Second):
		t.Fatal("selected cold workspace did not subscribe for lazy history")
	}
	select {
	case request := <-requests:
		t.Fatalf("selecting one workspace also subscribed Task %q", request.TaskID)
	default:
	}
	opened := receiveTUITaskStreamMessage[taskStreamOpenedMsg](t, messages)
	if next, _ := model.Update(opened); next != nil {
		model = next.(*Model)
	}
	model.closeSubagentOutputOverlay()
	view := model.subagentOutputViews[selectedCallID]
	view.historyResolved = true
	if !model.openSubagentOutputOverlayView(selectedCallID, view) {
		t.Fatal("cached terminal workspace did not reopen")
	}
	if model.taskStreamWanted[selectedTaskID] {
		t.Fatal("cached terminal workspace reopened Task observation")
	}
	select {
	case request := <-requests:
		t.Fatalf("cached terminal workspace reloaded Task %q", request.TaskID)
	default:
	}
	model.closeSubagentOutputOverlay()
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
	if model.subagentRosterOverlay == nil {
		t.Fatal("footer click did not open roster")
	}

	_ = model.View()
	geometry := model.subagentRosterOverlay.geometry
	rowY := geometry.rows[1].top
	point = tea.Mouse{X: geometry.x + 4, Y: rowY, Button: tea.MouseLeft}
	_, _ = model.handleMouse(tea.MouseClickMsg(point))
	point.Button = tea.MouseNone
	_, _ = model.handleMouse(tea.MouseReleaseMsg(point))
	if model.subagentOutputOverlay == nil || model.subagentOutputOverlay.callID != "spawn-sena" {
		t.Fatalf("row click opened %#v, want spawn-sena", model.subagentOutputOverlay)
	}
}

func TestSubagentRosterFooterClickTogglesOverlay(t *testing.T) {
	model := newSubagentRosterTestModel()
	addSubagentRosterTestView(model, "spawn-sena", "sena", "sena[self]: verify metadata", "completed", time.Unix(90, 0), time.Unix(120, 0))
	model.ensureViewportLayout()
	_ = model.View()

	bounds, ok := model.subagentRosterFooterHitBounds()
	if !ok {
		t.Fatal("footer omitted subagent hit bounds")
	}
	clickSubagentRosterFooter(t, model, bounds)
	if model.subagentRosterOverlay == nil {
		t.Fatal("first footer click did not open roster")
	}
	_ = model.View()

	clickSubagentRosterFooter(t, model, bounds)
	if model.subagentRosterOverlay != nil {
		t.Fatal("second footer click did not close roster")
	}
}

func TestSubagentRosterRefreshUsesTerminalTaskDirectoryWithoutMutatingWorkspace(t *testing.T) {
	t.Parallel()

	endedAt := time.Date(2026, time.August, 4, 14, 57, 0, 0, time.Local)
	service := &subagentRosterTestTaskStreamService{list: protocoltaskstream.ListResult{Tasks: []protocoltaskstream.TaskDescriptor{{
		SessionID: "session-1", TaskID: "task-1", Handle: "rhea", Kind: task.KindSubagent,
		State: task.StateCompleted, Running: false, UpdatedAt: endedAt,
		ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-rhea", ToolName: "Spawn"},
	}}}}
	model := NewModel(Config{
		NoColor: true, NoAnimation: true, TaskStreams: bindTaskStreamTestClient(t, service),
	})
	model.currentSessionID = "session-1"
	view := addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: audit ownership", "running", time.Unix(100, 0), time.Time{})

	cmd := model.requestSubagentRosterRefresh()
	if cmd == nil {
		t.Fatal("requestSubagentRosterRefresh() = nil")
	}
	raw := cmd()
	msg, ok := raw.(subagentRosterRefreshResultMsg)
	if !ok {
		t.Fatalf("refresh result = %T", raw)
	}
	if next := model.handleSubagentRosterRefreshResult(msg); next != nil {
		t.Fatal("terminal directory unexpectedly scheduled another refresh")
	}
	if got := model.subagentRosterRunningCount(); got != 0 {
		t.Fatalf("running count = %d, want terminal directory to close stale hidden view", got)
	}
	rows := model.subagentRosterRows()
	if len(rows) != 1 || subagentRosterTimeText(rows[0], endedAt) != "14:57" {
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
			ParentTool: protocoltaskstream.ParentTool{ToolCallID: callIDs[index], ToolName: "Spawn"},
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
			ToolCallID: callIDs[index], ToolName: "Spawn", ToolTaskHandle: handles[index],
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

	result := requireSubagentRosterRefreshResult(t, model.requestSubagentRosterRefresh())
	if next := model.handleSubagentRosterRefreshResult(result); next != nil {
		t.Fatal("terminal resumed directory unexpectedly scheduled another refresh")
	}
	if got := model.subagentRosterRunningCount(); got != 0 {
		t.Fatalf("resumed running count = %d, want terminal directory to win", got)
	}
	if footer := ansi.Strip(model.footerRowText()); !strings.Contains(footer, "• 3 done") || strings.Contains(footer, "running") {
		t.Fatalf("resumed footer retained stale running state: %q", footer)
	}

	view := model.subagentOutputViews[callIDs[0]]
	plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 88, 20)), "\n")
	if strings.Contains(strings.ToLower(plain), "waiting") || !strings.Contains(plain, "No retained assistant messages") {
		t.Fatalf("terminal empty workspace rendered as active output:\n%s", plain)
	}
}

func TestSubagentRosterColdResumeLetsDirectorySupersedeFreshReplayShell(t *testing.T) {
	t.Parallel()

	endedAt := time.Unix(100, 0)
	service := &subagentRosterTestTaskStreamService{list: protocoltaskstream.ListResult{Tasks: []protocoltaskstream.TaskDescriptor{{
		SessionID: "session-old", TaskID: "task-kira", Handle: "kira", Kind: task.KindSubagent,
		State: task.StateCompleted, Running: false, CurrentTurnID: "task-kira:1", UpdatedAt: endedAt,
		ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-kira", ToolName: "Spawn"},
	}}}}
	model := NewModel(Config{
		NoColor: true, NoAnimation: true, Workspace: "caelis",
		TaskStreams: bindTaskStreamTestClient(t, service),
	})
	model.currentSessionID = "session-old"
	model.observeSubagentOutputEvents([]TranscriptEvent{{
		Kind: TranscriptEventTool, Scope: ACPProjectionMain,
		ToolCallID: "spawn-kira", ToolName: "Spawn", ToolTaskHandle: "kira",
		ToolArgs: "kira[self]: historical task",
	}})
	view := model.subagentOutputViews["spawn-kira"]
	if view == nil || !view.block.StartedAt.After(endedAt) {
		t.Fatalf("cold replay shell start = %v, want a fresh local timestamp after terminal Task", view.block.StartedAt)
	}

	result := requireSubagentRosterRefreshResult(t, model.requestSubagentRosterRefresh())
	model.handleSubagentRosterRefreshResult(result)
	if got := model.subagentRosterRunningCount(); got != 0 {
		t.Fatalf("cold resumed running count = %d, want terminal directory to own provisional shell", got)
	}
	if status, _, gotEndedAt := model.subagentRosterViewState("spawn-kira", view); status == subagentOutputRunning || !gotEndedAt.Equal(endedAt) {
		t.Fatalf("cold resumed state = (%v, %v), want terminal at %v", status, gotEndedAt, endedAt)
	}
}

func TestSubagentRosterRefreshTracksContinuedCompletedChild(t *testing.T) {
	t.Parallel()

	oldEndedAt := time.Date(2026, time.August, 4, 14, 57, 0, 0, time.Local)
	restartedAt := oldEndedAt.Add(time.Minute)
	continuedEndedAt := restartedAt.Add(2 * time.Minute)
	service := &subagentRosterTestTaskStreamService{list: protocoltaskstream.ListResult{Tasks: []protocoltaskstream.TaskDescriptor{transcriptTaskDescriptor(
		"turn-1", task.StateCompleted, false, oldEndedAt,
	)}}}
	model := NewModel(Config{
		NoColor: true, NoAnimation: true, TaskStreams: bindTaskStreamTestClient(t, service),
	})
	model.currentSessionID = "session-1"
	view := addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: audit ownership", "completed", oldEndedAt.Add(-time.Minute), oldEndedAt)
	view.block.SessionID = "turn-1"

	completed := schema.ToolStatusCompleted
	accepted := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "parent-turn-2", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "message-1", Status: &completed,
			RawOutput: map[string]any{"accepted": true, "to": "rhea", "state": "running", "turn_id": "turn-2", "started_turn": true},
			Meta:      acpToolNameMeta("SendMessage"),
		},
	})
	if len(accepted) != 1 || !model.successfulSendMessageTargetsSubagent(accepted[0]) {
		t.Fatalf("successful child-directed SendMessage did not resume roster refresh: %#v", accepted)
	}
	if model.successfulSendMessageTargetsSubagent(TranscriptEvent{
		Scope: ACPProjectionMain, Kind: TranscriptEventTool, ToolName: "SendMessage",
		ToolMessageTarget: "@rhea", Final: true, ToolError: true,
	}) {
		t.Fatal("failed SendMessage resumed roster refresh")
	}

	staleCmd := model.requestSubagentRosterRefresh()
	if staleCmd == nil {
		t.Fatal("initial roster refresh command = nil")
	}
	staleResult := requireSubagentRosterRefreshResult(t, staleCmd)
	if duplicate := model.requestSubagentRosterRefresh(); duplicate != nil || model.subagentRosterRefreshQueued {
		t.Fatal("ordinary refresh queued an unthrottled read behind the in-flight request")
	}
	service.list.Tasks[0] = transcriptTaskDescriptor("turn-2", task.StateRunning, true, restartedAt)
	next, _ := model.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: accepted})
	model = next.(*Model)
	if !model.subagentRosterRefreshQueued {
		t.Fatal("accepted SendMessage did not queue a refresh behind the in-flight directory read")
	}
	cmd := model.handleSubagentRosterRefreshResult(staleResult)
	if cmd == nil {
		t.Fatal("stale terminal directory result dropped queued continuation refresh")
	}
	result := requireSubagentRosterRefreshResult(t, cmd)
	if next := model.handleSubagentRosterRefreshResult(result); next == nil {
		t.Fatal("running continuation did not schedule another refresh")
	}
	if got := model.subagentRosterRunningCount(); got != 1 {
		t.Fatalf("running count = %d, want continued child restored to running", got)
	}
	rows := model.subagentRosterRows()
	if len(rows) != 1 || rows[0].status != subagentOutputRunning || !rows[0].startedAt.Equal(restartedAt) || !rows[0].endedAt.IsZero() {
		t.Fatalf("continued running row = %#v", rows)
	}

	service.list.Tasks[0] = transcriptTaskDescriptor("turn-2", task.StateCompleted, false, continuedEndedAt)
	cmd = model.handleSubagentRosterRefreshTick(subagentRosterRefreshTickMsg{
		sessionID: result.sessionID, generation: result.generation,
	})
	if cmd == nil {
		t.Fatal("scheduled continuation refresh = nil")
	}
	result = requireSubagentRosterRefreshResult(t, cmd)
	if next := model.handleSubagentRosterRefreshResult(result); next != nil {
		t.Fatal("completed continuation unexpectedly scheduled another refresh")
	}
	if got := model.subagentRosterRunningCount(); got != 0 {
		t.Fatalf("running count = %d, want completed continuation idle", got)
	}
	rows = model.subagentRosterRows()
	if len(rows) != 1 || rows[0].status != subagentOutputSucceeded || !rows[0].endedAt.Equal(continuedEndedAt) {
		t.Fatalf("continued terminal row = %#v", rows)
	}
	if view.block.SessionID != "turn-1" || view.block.Status != "completed" || !view.block.EndedAt.Equal(oldEndedAt) {
		t.Fatalf("directory continuation mutated retained child workspace: turn=%q status=%q ended=%v", view.block.SessionID, view.block.Status, view.block.EndedAt)
	}
}

func TestSubagentRosterRefreshRetriesAcceptedSendAfterTransientListFailure(t *testing.T) {
	t.Parallel()

	oldEndedAt := time.Date(2026, time.August, 5, 9, 0, 0, 0, time.Local)
	restartedAt := oldEndedAt.Add(time.Minute)
	service := &subagentRosterTestTaskStreamService{
		list: protocoltaskstream.ListResult{Tasks: []protocoltaskstream.TaskDescriptor{
			transcriptTaskDescriptor("turn-2", task.StateRunning, true, restartedAt),
		}},
		listErrors: []error{context.DeadlineExceeded},
	}
	model := NewModel(Config{
		NoColor: true, NoAnimation: true, TaskStreams: bindTaskStreamTestClient(t, service),
	})
	model.currentSessionID = "session-1"
	view := addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: continue audit", "completed", oldEndedAt.Add(-time.Minute), oldEndedAt)
	view.block.SessionID = "turn-1"
	model.subagentRosterTasks = subagentRosterTasksByCallID([]protocoltaskstream.TaskDescriptor{
		transcriptTaskDescriptor("turn-1", task.StateCompleted, false, oldEndedAt),
	})

	first := requireSubagentRosterRefreshResult(t, model.requestSubagentRosterRefreshAfterAcceptedSend())
	if first.err == nil {
		t.Fatal("first accepted-send roster refresh unexpectedly succeeded")
	}
	if cmd := model.handleSubagentRosterRefreshResult(first); cmd == nil {
		t.Fatal("transient accepted-send roster failure did not schedule a retry")
	}
	if !model.subagentRosterRefreshWake || model.subagentRosterRefreshWakeRetries != 1 {
		t.Fatalf("accepted-send retry state = (%v, %d), want pending wake and one failure", model.subagentRosterRefreshWake, model.subagentRosterRefreshWakeRetries)
	}

	retryCmd := model.handleSubagentRosterRefreshTick(subagentRosterRefreshTickMsg{
		sessionID: first.sessionID, generation: first.generation,
	})
	result := requireSubagentRosterRefreshResult(t, retryCmd)
	if result.err != nil {
		t.Fatalf("accepted-send roster retry error = %v", result.err)
	}
	if next := model.handleSubagentRosterRefreshResult(result); next == nil {
		t.Fatal("running continued child did not resume ordinary roster polling")
	}
	if model.subagentRosterRefreshWake || model.subagentRosterRefreshWakeRetries != 0 {
		t.Fatalf("accepted-send retry state survived successful refresh: (%v, %d)", model.subagentRosterRefreshWake, model.subagentRosterRefreshWakeRetries)
	}
	if got := model.subagentRosterRunningCount(); got != 1 {
		t.Fatalf("running count after retry = %d, want 1", got)
	}
	if service.listCalls != 2 {
		t.Fatalf("roster List calls = %d, want initial failure plus successful retry", service.listCalls)
	}
}

func TestSubagentRosterRefreshRetriesAcceptedSendAfterStaleSuccessfulList(t *testing.T) {
	t.Parallel()

	oldEndedAt := time.Date(2026, time.August, 5, 10, 0, 0, 0, time.Local)
	continuedEndedAt := oldEndedAt.Add(time.Minute)
	stale := transcriptTaskDescriptor("turn-1", task.StateCompleted, false, oldEndedAt)
	service := &subagentRosterTestTaskStreamService{list: protocoltaskstream.ListResult{Tasks: []protocoltaskstream.TaskDescriptor{stale}}}
	model := NewModel(Config{
		NoColor: true, NoAnimation: true, TaskStreams: bindTaskStreamTestClient(t, service),
	})
	model.currentSessionID = "session-1"
	view := addSubagentRosterTestView(model, "spawn-rhea", "rhea", "rhea[reviewer]: continue audit", "completed", oldEndedAt.Add(-time.Minute), oldEndedAt)
	view.block.SessionID = "turn-1"
	view.turnID = "turn-1"
	view.historyResolved = true
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-rhea"}
	model.subagentRosterTasks = subagentRosterTasksByCallID([]protocoltaskstream.TaskDescriptor{stale})

	first := requireSubagentRosterRefreshResult(t, model.requestSubagentRosterRefreshAfterAcceptedSend("spawn-rhea"))
	if first.err != nil {
		t.Fatalf("first List() error = %v", first.err)
	}
	if cmd := model.handleSubagentRosterRefreshResult(first); cmd == nil {
		t.Fatal("stale successful directory result did not schedule an accepted-send retry")
	}
	if !model.subagentRosterRefreshWake || model.subagentRosterRefreshWakeRetries != 1 {
		t.Fatalf("stale-success retry state = (%v, %d), want pending wake and one retry", model.subagentRosterRefreshWake, model.subagentRosterRefreshWakeRetries)
	}
	if demand := model.taskStreamDemandForOwner("spawn-rhea", "rhea"); demand != taskStreamDemandNone {
		t.Fatalf("stale terminal demand = %v, want cached history detached", demand)
	}

	service.list.Tasks[0] = transcriptTaskDescriptor("turn-2", task.StateCompleted, false, continuedEndedAt)
	retryCmd := model.handleSubagentRosterRefreshTick(subagentRosterRefreshTickMsg{
		sessionID: first.sessionID, generation: first.generation,
	})
	result := requireSubagentRosterRefreshResult(t, retryCmd)
	if cmd := model.handleSubagentRosterRefreshResult(result); cmd != nil {
		t.Fatal("new terminal activity unexpectedly kept roster polling")
	}
	if model.subagentRosterRefreshWake || model.subagentRosterRefreshWakeRetries != 0 {
		t.Fatalf("accepted-send wake survived new activity: (%v, %d)", model.subagentRosterRefreshWake, model.subagentRosterRefreshWakeRetries)
	}
	if demand := model.taskStreamDemandForOwner("spawn-rhea", "rhea"); demand != taskStreamDemandVisibleSubagent {
		t.Fatalf("new terminal activity demand = %v, want finite history subscription", demand)
	}
}

func requireSubagentRosterRefreshResult(t *testing.T, cmd tea.Cmd) subagentRosterRefreshResultMsg {
	t.Helper()
	commands := []tea.Cmd{cmd}
	for len(commands) > 0 {
		current := commands[0]
		commands = commands[1:]
		if current == nil {
			continue
		}
		switch msg := current().(type) {
		case subagentRosterRefreshResultMsg:
			return msg
		case tea.BatchMsg:
			commands = append(commands, msg...)
		}
	}
	t.Fatal("command omitted subagent roster refresh result")
	return subagentRosterRefreshResultMsg{}
}

func transcriptTaskDescriptor(turnID string, state task.State, running bool, updatedAt time.Time) protocoltaskstream.TaskDescriptor {
	return protocoltaskstream.TaskDescriptor{
		SessionID: "session-1", TaskID: "task-1", Handle: "rhea", Kind: task.KindSubagent,
		State: state, Running: running, CurrentTurnID: turnID, UpdatedAt: updatedAt,
		ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-rhea", ToolName: "Spawn"},
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

func assertSubagentRosterRowEndsWith(t *testing.T, lines []string, handle string, suffix string) {
	t.Helper()
	for _, line := range lines {
		plain := strings.TrimRight(ansi.Strip(line), " ")
		if strings.Contains(plain, handle) {
			if !strings.HasSuffix(plain, suffix) {
				t.Fatalf("%s row = %q, want suffix %q", handle, plain, suffix)
			}
			return
		}
	}
	t.Fatalf("missing %s row", handle)
}

func assertSubagentRosterPromptSharesRow(t *testing.T, lines []string, handle string, prompt string) {
	t.Helper()
	line := ansi.Strip(subagentRosterLineForHandle(t, lines, handle))
	if !strings.Contains(line, prompt) {
		t.Fatalf("%s row omitted inline prompt %q: %q", handle, prompt, line)
	}
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

func (*subagentRosterTestTaskStreamService) Events(context.Context, protocoltaskstream.Principal, protocoltaskstream.ReadRequest) (protocoltaskstream.Batch, error) {
	return protocoltaskstream.Batch{}, nil
}

func (s *subagentRosterTestTaskStreamService) Subscribe(_ context.Context, _ protocoltaskstream.Principal, request protocoltaskstream.SubscribeRequest) (protocoltaskstream.SubscribeResult, error) {
	if s.subscribeRequests != nil {
		s.subscribeRequests <- request
	}
	return protocoltaskstream.SubscribeResult{Subscription: s.subscription}, nil
}
