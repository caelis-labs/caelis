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

func TestSubagentRosterOverlayUsesExpandedWideBudgetAndFullWidthNarrowSheet(t *testing.T) {
	model := newSubagentRosterTestModel()
	addSubagentRosterTestView(
		model,
		"spawn-overlay-review",
		"overlay-ux-review",
		"overlay-ux-review[orbit]: inspect centered overlay composition and renderer performance",
		"completed",
		time.Unix(90, 0),
		time.Unix(120, 0),
	)
	if !model.openSubagentRosterOverlay() {
		t.Fatal("openSubagentRosterOverlay() = false")
	}

	for _, size := range []struct {
		width int
		want  int
	}{
		{width: 180, want: subagentRosterOverlayMaxWidth},
		{width: 100, want: 84},
		{width: 60, want: 60},
	} {
		model.width = size.width
		_ = model.renderSubagentRosterOverlay()
		if got := model.subagentRosterOverlay.geometry.width; got != size.want {
			t.Fatalf("terminal width %d roster width = %d, want %d", size.width, got, size.want)
		}
	}

	model.width = 180
	plain := ansi.Strip(model.renderSubagentRosterOverlay())
	if !strings.Contains(plain, "overlay-ux-review") || strings.Contains(plain, "overlay-ux-r...") {
		t.Fatalf("wide roster did not use expanded identity budget:\n%s", plain)
	}
	for _, want := range []string{"↑↓ select", "Enter open", "Esc close"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("wide roster footer omitted %q:\n%s", want, plain)
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
			State: task.StateCompleted, Running: false, ActivityID: "activity-kira", UpdatedAt: time.Unix(103, 0),
			ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-kira", ToolName: "Spawn"},
		},
		{
			SessionID: "session-old", TaskID: "task-wen", Handle: "wen", Kind: task.KindSubagent,
			State: task.StateCompleted, Running: false, ActivityID: "activity-wen", UpdatedAt: time.Unix(102, 0),
			ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-wen", ToolName: "Spawn"},
		},
		{
			SessionID: "session-old", TaskID: "task-yara", Handle: "yara", Kind: task.KindSubagent,
			State: task.StateCompleted, Running: false, ActivityID: "activity-yara", UpdatedAt: time.Unix(101, 0),
			ParentTool: protocoltaskstream.ParentTool{ToolCallID: "spawn-yara", ToolName: "Spawn"},
		},
	}
	requests := make(chan protocoltaskstream.ReadRequest, len(descriptors))
	service := &subagentRosterTestTaskStreamService{
		list:          protocoltaskstream.ListResult{Tasks: descriptors},
		eventRequests: requests,
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

	applySubagentDirectorySnapshotForTest(model, 1, descriptors)
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
	service.eventBatch = protocoltaskstream.ReadResult{
		ActivityID: model.subagentRosterTasks[selectedCallID].ActivityID,
		Deliveries: []protocoltaskstream.Delivery{{
			Kind: protocoltaskstream.DeliveryAppendPage, Source: protocoltaskstream.SourceExact,
			NextCursor: "history-cursor-2",
			Events: []eventstream.Envelope{tuiExactEnvelope(eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-old", TurnID: selectedTaskID + ":1",
				Scope: eventstream.ScopeSubagent, ScopeID: selectedTaskID,
				ParentTool: &eventstream.ParentToolRelation{ToolCallID: selectedCallID, ToolName: "Spawn"},
				Update: eventstream.ContentChunk{
					SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "history-answer",
					Content: eventstream.TextContent{Type: "text", Text: "restored complete child history"},
				},
			}, "history-cursor-1", 1), tuiExactEnvelope(eventstream.Envelope{
				Kind: eventstream.KindLifecycle, SessionID: "session-old", TurnID: selectedTaskID + ":1",
				Scope: eventstream.ScopeSubagent, ScopeID: selectedTaskID, Final: true,
				ParentTool: &eventstream.ParentToolRelation{ToolCallID: selectedCallID, ToolName: "Spawn"},
				Lifecycle:  &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted},
			}, "history-cursor-2", 2)},
		}},
	}
	resolved := receiveTUITaskStreamMessage[taskStreamResolvedMsg](t, messages)
	if next, _ := model.Update(resolved); next != nil {
		model = next.(*Model)
	}
	select {
	case request := <-requests:
		if request.TaskID != selectedTaskID {
			t.Fatalf("selected workspace loaded Task %q, want %q", request.TaskID, selectedTaskID)
		}
		if request.ExpectedActivityID != model.subagentRosterTasks[selectedCallID].ActivityID {
			t.Fatalf("selected workspace history activity = %q, want %q",
				request.ExpectedActivityID, model.subagentRosterTasks[selectedCallID].ActivityID)
		}
	case <-time.After(time.Second):
		t.Fatal("selected cold workspace did not request lazy history")
	}
	select {
	case request := <-requests:
		if request.TaskID != selectedTaskID || request.Cursor != "history-cursor-2" {
			t.Fatalf("history continuation = %#v, want selected Task after history-cursor-2", request)
		}
	case <-time.After(time.Second):
		t.Fatal("selected history did not verify its exact high-water mark")
	}
	select {
	case request := <-requests:
		t.Fatalf("selecting one workspace also loaded Task %q after exact EOF", request.TaskID)
	default:
	}
	historyBatch := receiveTUITaskStreamMessage[taskStreamHistoryBatchMsg](t, messages)
	if next, _ := model.Update(historyBatch); next != nil {
		model = next.(*Model)
	}
	historyClosed := receiveTUITaskStreamMessage[taskStreamHistoryClosedMsg](t, messages)
	if next, _ := model.Update(historyClosed); next != nil {
		model = next.(*Model)
	}
	view := model.subagentOutputViews[selectedCallID]
	plain := joinRenderedPlain(model.subagentOutputRows(view, 72, 16))
	if !view.idleHistorySettled || !strings.Contains(plain, "restored complete child history") ||
		strings.Contains(plain, "Loading subagent history") {
		t.Fatalf("cold history settled=%v output=%q", view.idleHistorySettled, plain)
	}
	model.closeSubagentOutputOverlay()
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

func TestSubagentDirectorySnapshotUsesTerminalStateWithoutMutatingWorkspace(t *testing.T) {
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

	applySubagentDirectorySnapshotForTest(model, 1, service.list.Tasks)
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
	view.idleHistorySettled = true
	view.directoryActivityID = "activity:activity-1"
	view.idleHistoryActivityID = view.directoryActivityID
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
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-rhea", ToolName: "Spawn"},
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
