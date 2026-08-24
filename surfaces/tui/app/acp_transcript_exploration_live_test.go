package tuiapp

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
	"github.com/charmbracelet/x/ansi"
)

func TestLiveExplorationKeepsCompletedGroupsStableAroundPendingStage(t *testing.T) {
	const (
		width  = 100
		height = 30
	)
	h := newLiveExplorationHarness(t, width, height)

	// Results may arrive out of start order. A later narrative moves the
	// physical model step out of the live tail, but does not complete a call
	// whose result is still absent from the Surface feed.
	h.start("read-1", "Read", "read", "a.go")
	h.start("read-2", "Read", "read", "b.go")
	h.reason("reason-1", "inspect the first batch")
	h.complete("read-1", "Read", "read", "a.go")

	h.start("search-middle", "Grep", "search", "needle") // deliberately remains pending
	h.start("read-middle", "Read", "read", "middle.go")
	h.complete("read-middle", "Read", "read", "middle.go")
	h.complete("read-2", "Read", "read", "b.go")
	h.reason("reason-2", "inspect the last batch")

	h.start("read-last-1", "Read", "read", "last-1.go")
	h.complete("read-last-1", "Read", "read", "last-1.go")
	h.start("read-last-2", "Read", "read", "last-2.go")
	h.complete("read-last-2", "Read", "read", "last-2.go")

	// The latest completed stage remains a live tail until this later step.
	beforeLater := ansi.Strip(h.model.View().Content)
	if !strings.Contains(beforeLater, "• Read last-1.go, last-2.go") {
		t.Fatalf("latest exploration stage did not remain live-tail before a later step:\n%s", beforeLater)
	}
	h.reason("reason-3", "continue after the last batch")

	block := requireMainACPTurnBlockForTest(t, h.model)
	pending := requireToolEventForTest(t, block.Events, "search-middle")
	if pending.Done {
		t.Fatal("later narrative incorrectly completed the pending middle exploration")
	}
	wantFirst := []string{"read-1", "read-2"}
	wantLast := []string{"read-last-1", "read-last-2"}
	stableRuns := collectStableExplorationRuns(block.Events, block.Status)
	if len(stableRuns) != 2 || !slices.Equal(stableRuns[0], wantFirst) || !slices.Equal(stableRuns[1], wantLast) {
		t.Fatalf("stable exploration runs = %#v, want completed runs %#v and %#v", stableRuns, wantFirst, wantLast)
	}
	if len(block.explorationProjection.Containers) != 2 {
		t.Fatalf("exploration containers = %#v, want two completed runs", block.explorationProjection.Containers)
	}

	pendingFrame := h.model.View().Content
	plain := ansi.Strip(pendingFrame)
	if got := countExactTrimmedLine(plain, "• Explored"); got != 2 {
		t.Fatalf("completed exploration groups = %d, want two stable groups:\n%s", got, plain)
	}
	if strings.Contains(plain, "• Exploring") {
		t.Fatalf("pending exploration introduced an aggregate Exploring state:\n%s", plain)
	}
	if !strings.Contains(plain, "needle") {
		t.Fatalf("pending exploration lifecycle disappeared beside stable groups:\n%s", plain)
	}
	updates := renderFullscreenFramesForTest(t, width, height, h.frames...)
	assertPhysicalFullscreenFrame(t, width, height, pendingFrame, updates)

	if !block.toggleExplorationExpanded("read-1") {
		t.Fatal("completed exploration container did not expand")
	}
	expanded := joinRenderedPlain(block.Render(h.model.blockRenderContext(width)))
	if !strings.Contains(expanded, "Explored") || !strings.Contains(expanded, "a.go") {
		t.Fatalf("expanded completed container lost its stable presentation:\n%s", expanded)
	}

	// Once the missing result arrives, the completed runs may coalesce without
	// ever downgrading their aggregate presentation to an active state.
	h.complete("search-middle", "Grep", "search", "needle")
	block = requireMainACPTurnBlockForTest(t, h.model)
	if event := requireToolEventForTest(t, block.Events, "search-middle"); !event.Done {
		t.Fatal("explicit result update did not complete the pending search")
	}
	wantRun := []string{"read-1", "read-2", "search-middle", "read-middle", "read-last-1", "read-last-2"}
	stableRuns = collectStableExplorationRuns(block.Events, block.Status)
	if len(stableRuns) != 1 || !slices.Equal(stableRuns[0], wantRun) {
		t.Fatalf("stable exploration runs = %#v, want %#v", stableRuns, [][]string{wantRun})
	}
	completedPlain := ansi.Strip(h.model.View().Content)
	if got := countExactTrimmedLine(completedPlain, "• Explored"); got != 1 {
		t.Fatalf("completed exploration groups = %d, want one:\n%s", got, completedPlain)
	}
	if strings.Contains(completedPlain, "• Exploring") {
		t.Fatalf("completed exploration retained active aggregate header:\n%s", completedPlain)
	}
}

func TestHiddenTaskWaitCannotDowngradeExploredSummary(t *testing.T) {
	h := newLiveExplorationHarness(t, 100, 30)
	completed := schema.ToolStatusCompleted

	h.start("search-1", "Grep", schema.ToolKindSearch, "message")
	h.apply(liveExplorationEnvelope(schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    "search-1",
		Status:        &completed,
		RawInput:      map[string]any{"pattern": "message", "path": "."},
		RawOutput:     map[string]any{"pattern": "message", "count": 100},
		Meta:          acpToolNameMeta("Grep"),
	}))
	h.start("read-1", "Read", schema.ToolKindRead, "agent_input.go")
	h.apply(liveExplorationEnvelope(schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    "read-1",
		Status:        &completed,
		RawInput:      map[string]any{"path": "agent_input.go", "offset": 0, "limit": 55},
		RawOutput:     map[string]any{"path": "agent_input.go", "start_line": 1, "end_line": 55},
		Meta:          acpToolNameMeta("Read"),
	}))
	h.reason("reason-after-first-batch", "inspect the completed exploration")

	beforePending := ansi.Strip(h.model.View().Content)
	if got := countExactTrimmedLine(beforePending, "• Explored"); got != 1 ||
		!strings.Contains(beforePending, `Search "message" 100 hits`) ||
		!strings.Contains(beforePending, "Read agent_input.go 1~55") {
		t.Fatalf("test setup did not establish the settled summary:\n%s", beforePending)
	}

	// This later Read has no terminal update. Task wait is hidden from the
	// transcript but appends a semantic boundary, which used to merge this live
	// tail into the completed container and re-tense the whole group.
	h.start("read-pending", "Read", schema.ToolKindRead, "later.go")
	h.apply(liveExplorationEnvelope(schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall,
		ToolCallID:    "task-wait",
		Title:         "Task wait orbit",
		Kind:          schema.ToolKindOther,
		Status:        schema.ToolStatusInProgress,
		RawInput: map[string]any{
			"action": "wait", "handle": "orbit", "target_kind": "subagent",
		},
		Meta: acpToolNameMeta("Task"),
	}))

	block := requireMainACPTurnBlockForTest(t, h.model)
	wantRun := []string{"search-1", "read-1"}
	stableRuns := collectStableExplorationRuns(block.Events, block.Status)
	if len(stableRuns) != 1 || !slices.Equal(stableRuns[0], wantRun) {
		t.Fatalf("stable exploration runs after Task wait = %#v, want %#v", stableRuns, [][]string{wantRun})
	}
	boundaries := 0
	for _, event := range block.Events {
		if event.Kind == SESemanticBoundary {
			boundaries++
		}
		if event.Kind == SEToolCall && event.Name == surfaceToolTask {
			t.Fatalf("hidden Task wait rendered a physical event: %#v", event)
		}
	}
	if boundaries != 1 {
		t.Fatalf("semantic boundaries = %d, want Task wait to append one", boundaries)
	}

	plain := ansi.Strip(h.model.View().Content)
	if got := countExactTrimmedLine(plain, "• Explored"); got != 1 {
		t.Fatalf("Explored group count after Task wait = %d, want one:\n%s", got, plain)
	}
	if strings.Contains(plain, "• Exploring") {
		t.Fatalf("Task wait downgraded the completed group to Exploring:\n%s", plain)
	}
	if !strings.Contains(plain, `Search "message" 100 hits`) || !strings.Contains(plain, "Read agent_input.go 1~55") {
		t.Fatalf("Task wait replaced settled Search/Read summaries with invocation arguments:\n%s", plain)
	}
	if !strings.Contains(plain, "• Read later.go") {
		t.Fatalf("pending exploration lifecycle disappeared:\n%s", plain)
	}
}

func TestFailedExplorationStaysInsideOneExploredGroup(t *testing.T) {
	h := newLiveExplorationHarness(t, 100, 30)
	h.start("read-1", "Read", "read", "a.go")
	h.complete("read-1", "Read", "read", "a.go")
	h.start("search-1", "Grep", "search", "first")
	h.complete("search-1", "Grep", "search", "first")
	h.start("read-failed", "Read", "read", "missing.go")
	h.apply(liveExplorationToolFailedEnvelope("read-failed", "Read", "read", "missing.go"))
	h.start("read-2", "Read", "read", "b.go")
	h.complete("read-2", "Read", "read", "b.go")
	h.start("search-2", "Grep", "search", "second")
	h.complete("search-2", "Grep", "search", "second")
	h.reason("reason-after-exploration", "continue after exploration")

	block := requireMainACPTurnBlockForTest(t, h.model)
	wantRun := []string{"read-1", "search-1", "read-failed", "read-2", "search-2"}
	stableRuns := collectStableExplorationRuns(block.Events, block.Status)
	if len(stableRuns) != 1 || !slices.Equal(stableRuns[0], wantRun) {
		t.Fatalf("stable exploration runs = %#v, want one run %#v", stableRuns, wantRun)
	}
	plain := ansi.Strip(h.model.View().Content)
	if got := countExactTrimmedLine(plain, "• Explored"); got != 1 {
		t.Fatalf("exploration groups = %d, want one across failed Read:\n%s", got, plain)
	}
	if headers := standaloneExplorationHeaders(plain); len(headers) != 0 {
		t.Fatalf("failed exploration rendered outside the Explored group as %q:\n%s", headers, plain)
	}
	if !strings.Contains(plain, "missing.go failed") {
		t.Fatalf("Explored summary lost failed Read outcome:\n%s", plain)
	}
}

func TestTerminalPendingExplorationDoesNotRenderAsActiveContainer(t *testing.T) {
	block := NewMainACPTurnBlock("turn-terminal-pending")
	block.UpdateToolWithMeta("read-1", "Read", "a.go", "", false, false, ToolUpdateMeta{ToolKind: "read"})
	block.UpdateToolWithMeta("read-2", "Read", "b.go", "", false, false, ToolUpdateMeta{ToolKind: "read"})
	block.AppendStreamEvent(SEReasoning, "later terminal narrative", narrativeTestSource())
	ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(100)
	running := joinRenderedPlain(block.Render(ctx))
	if strings.Contains(running, "• Exploring") || strings.Contains(running, "• Explored") {
		t.Fatalf("pending exploration rendered as an aggregate state:\n%s", running)
	}
	block.Status = "completed"

	rows := block.Render(ctx)
	plain := joinRenderedPlain(rows)
	if strings.Contains(plain, "• Exploring") || strings.Contains(plain, "• Explored") {
		t.Fatalf("terminal pending tools rendered as an exploration container:\n%s", plain)
	}
	if !strings.Contains(plain, "a.go") || !strings.Contains(plain, "b.go") {
		t.Fatalf("terminal pending tool lifecycle rows disappeared:\n%s", plain)
	}
}

func TestLiveExplorationReclassificationDoesNotDisturbCompletedContainer(t *testing.T) {
	h := newLiveExplorationHarness(t, 100, 30)
	h.start("read-1", "Read", "read", "a.go")
	h.complete("read-1", "Read", "read", "a.go")
	h.start("read-2", "Read", "read", "b.go")
	h.complete("read-2", "Read", "read", "b.go")
	h.start("reclassified", "Read", "read", "before.go")
	h.reason("reason-after-start", "continue after the pending batch")

	block := requireMainACPTurnBlockForTest(t, h.model)
	if len(block.explorationProjection.Containers) != 1 || !slices.Equal(block.explorationProjection.Containers[0].CallIDs, []string{"read-1", "read-2"}) {
		t.Fatalf("test setup did not preserve the completed exploration container: %#v", block.explorationProjection.Containers)
	}

	name := "RunCommand"
	kind := "execute"
	status := schema.ToolStatusCompleted
	h.apply(liveExplorationEnvelope(schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    "reclassified",
		Title:         &name,
		Kind:          &kind,
		Status:        &status,
		RawInput:      liveExplorationToolInput(kind, "echo reviewed"),
	}))

	block = requireMainACPTurnBlockForTest(t, h.model)
	reclassified := requireToolEventForTest(t, block.Events, "reclassified")
	if !reclassified.Done || reclassified.Name != "Read" || reclassified.ToolKind != kind || reclassified.Title != name || isExplorationToolEvent(reclassified) {
		t.Fatalf("standard kind patch did not reclassify presentation while preserving exact identity: %#v", reclassified)
	}
	plain := ansi.Strip(h.model.View().Content)
	for _, container := range block.explorationProjection.Containers {
		if slices.Contains(container.CallIDs, "reclassified") {
			t.Fatalf("reclassified tool remained in exploration container after render: %#v", container)
		}
	}
	if got := countExactTrimmedLine(plain, "• Explored"); got != 1 || strings.Contains(plain, "• Exploring") {
		t.Fatalf("completed exploration container changed after reclassification:\n%s", plain)
	}
	if !strings.Contains(plain, "echo reviewed") {
		t.Fatalf("ordinary tool lifecycle row was swallowed after reclassification:\n%s", plain)
	}
}

func TestRetryNoticeKeepsCompletedExplorationCollapsed(t *testing.T) {
	block := NewMainACPTurnBlock("turn-retry-notice")
	block.UpdateToolWithMeta("read-1", "Read", "a.go", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	block.UpdateToolWithMeta("search-1", "Grep", `"needle" 3 hits`, "", true, false, ToolUpdateMeta{ToolKind: "search"})
	block.AddNotice("Retrying model request (1/5, retry in 1s)", time.Time{}, transcript.NoticeKindModelRetry)

	wantRun := []string{"read-1", "search-1"}
	stableRuns := collectStableExplorationRuns(block.Events, block.Status)
	if len(stableRuns) != 1 || !slices.Equal(stableRuns[0], wantRun) {
		t.Fatalf("stable exploration runs = %#v, want %#v", stableRuns, [][]string{wantRun})
	}

	plain := joinRenderedPlain(block.Render(NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(100)))
	if got := countExactTrimmedLine(plain, "• Explored"); got != 1 {
		t.Fatalf("completed exploration groups = %d, want one after retry notice:\n%s", got, plain)
	}
	if strings.Contains(plain, "• Exploring") {
		t.Fatalf("retry notice rendered completed tools as live exploration:\n%s", plain)
	}
	if headers := standaloneExplorationHeaders(plain); len(headers) != 0 {
		t.Fatalf("retry notice flattened exploration into standalone headers %q:\n%s", headers, plain)
	}
	if !strings.Contains(plain, "! Retrying model request (1/5, retry in 1s)") {
		t.Fatalf("retry notice disappeared from the transcript:\n%s", plain)
	}
}

func TestAttemptResetRetryNoticeDoesNotFlattenSettledExploration(t *testing.T) {
	block := NewMainACPTurnBlock("turn-attempt-reset")
	block.UpdateToolWithMeta("read-1", "Read", "a.go", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	block.UpdateToolWithMeta("search-1", "Grep", `"needle" 3 hits`, "", true, false, ToolUpdateMeta{ToolKind: "search"})
	block.AppendStreamEvent(SEReasoning, "inspect the first batch", narrativeTestSource())

	ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(100)
	before := joinRenderedPlain(block.Render(ctx))
	if got := countExactTrimmedLine(before, "• Explored"); got != 1 {
		t.Fatalf("settled exploration groups = %d, want one before retry:\n%s", got, before)
	}

	// attempt_reset drops speculative later narrative, then the retry notice is
	// appended. The completed batch must stay compacted instead of flattening
	// back into standalone Read/Search rows.
	block.ClearActiveBuffers()
	block.AddNotice("Retrying model request (1/5, retry in 1s)", time.Time{}, transcript.NoticeKindModelRetry)

	wantRun := []string{"read-1", "search-1"}
	stableRuns := collectStableExplorationRuns(block.Events, block.Status)
	if len(stableRuns) != 1 || !slices.Equal(stableRuns[0], wantRun) {
		t.Fatalf("stable exploration runs after retry = %#v, want %#v", stableRuns, [][]string{wantRun})
	}
	plain := joinRenderedPlain(block.Render(ctx))
	if got := countExactTrimmedLine(plain, "• Explored"); got != 1 {
		t.Fatalf("exploration groups after retry = %d, want one compacted group:\n%s", got, plain)
	}
	if strings.Contains(plain, "inspect the first batch") {
		t.Fatalf("speculative later narrative survived attempt reset:\n%s", plain)
	}
	if headers := standaloneExplorationHeaders(plain); len(headers) != 0 {
		t.Fatalf("attempt reset flattened exploration into standalone headers %q:\n%s", headers, plain)
	}
	if !strings.Contains(plain, "! Retrying model request (1/5, retry in 1s)") {
		t.Fatalf("retry notice disappeared after attempt reset:\n%s", plain)
	}
}

func TestRetryRequestStartKeepsSettledExplorationCollapsed(t *testing.T) {
	h := newLiveExplorationHarness(t, 100, 30)
	h.start("read-1", "Read", "read", "a.go")
	h.complete("read-1", "Read", "read", "a.go")
	h.start("search-1", "Grep", "search", "needle")
	h.complete("search-1", "Grep", "search", "needle")
	h.reason("reason-before-retry", "inspect the completed exploration")

	beforeRetry := ansi.Strip(h.model.View().Content)
	if got := countExactTrimmedLine(beforeRetry, "• Explored"); got != 1 {
		t.Fatalf("settled exploration groups before retry = %d, want one:\n%s", got, beforeRetry)
	}

	reset := liveExplorationAttemptResetEnvelope(5, 5)
	h.apply(reset)
	h.drainNarrative()
	retrying := ansi.Strip(h.model.View().Content)
	if got := countExactTrimmedLine(retrying, "• Explored"); got != 1 {
		t.Fatalf("settled exploration groups during retry = %d, want one:\n%s", got, retrying)
	}
	if !strings.Contains(retrying, "Retrying model request (5/5)") {
		t.Fatalf("retry notice missing before the next request:\n%s", retrying)
	}

	// The next request's running lifecycle clears the transient retry notice
	// without appending a later transcript step. The previously settled
	// container must remain compact until the next model narrative arrives.
	running := liveExplorationLifecycleEnvelope("running")
	h.apply(running)
	h.drainNarrative()
	waiting := ansi.Strip(h.model.View().Content)
	if strings.Contains(waiting, "Retrying model request") {
		t.Fatalf("running lifecycle did not clear the transient retry notice:\n%s", waiting)
	}
	if got := countExactTrimmedLine(waiting, "• Explored"); got != 1 {
		t.Fatalf("settled exploration groups while awaiting retry result = %d, want one:\n%s", got, waiting)
	}
	if headers := standaloneExplorationHeaders(waiting); len(headers) != 0 {
		t.Fatalf("settled exploration expanded while awaiting retry result as %q:\n%s", headers, waiting)
	}
	updates := renderFullscreenFramesForTest(t, 100, 30, h.frames...)
	assertPhysicalFullscreenFrame(t, 100, 30, h.model.View().Content, updates)

	h.reason("reason-after-retry", "continue after the retry")
	afterResult := ansi.Strip(h.model.View().Content)
	if got := countExactTrimmedLine(afterResult, "• Explored"); got != 1 {
		t.Fatalf("settled exploration groups after retry result = %d, want one:\n%s", got, afterResult)
	}
}

func TestRetryRequestStartKeepsSettledExplorationCollapsedBeforeFirstRender(t *testing.T) {
	model := NewModel(Config{
		NoColor:            true,
		NoAnimation:        true,
		StreamTickInterval: time.Millisecond,
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(*Model)

	reset := liveExplorationAttemptResetEnvelope(5, 5)
	running := liveExplorationLifecycleEnvelope("running")

	envelopes := []eventstream.Envelope{
		liveExplorationToolStartEnvelope("read-1", "Read", "read", "a.go"),
		liveExplorationToolCompleteEnvelope("read-1", "Read", "read", "a.go"),
		liveExplorationToolStartEnvelope("search-1", "Grep", "search", "needle"),
		liveExplorationToolCompleteEnvelope("search-1", "Grep", "search", "needle"),
		liveExplorationNarrativeEnvelope("reason-before-retry", "inspect the completed exploration"),
		reset,
		running,
	}
	var batch []TranscriptEvent
	for _, envelope := range envelopes {
		batch = append(batch, ProjectACPEventToTranscriptEvents(envelope)...)
	}
	updated, _ = model.Update(TranscriptEventsMsg{Events: batch})
	model = updated.(*Model)
	updated, _ = model.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now()})
	model = updated.(*Model)

	waiting := ansi.Strip(model.View().Content)
	if strings.Contains(waiting, "Retrying model request") {
		t.Fatalf("running lifecycle did not clear the transient retry notice:\n%s", waiting)
	}
	if got := countExactTrimmedLine(waiting, "• Explored"); got != 1 {
		t.Fatalf("settled exploration groups before first render = %d, want one:\n%s", got, waiting)
	}
	if headers := standaloneExplorationHeaders(waiting); len(headers) != 0 {
		t.Fatalf("settled exploration expanded before first render as %q:\n%s", headers, waiting)
	}
}

func TestLaterWorkSuppressesRetryNoticeAndKeepsOneExploredGroup(t *testing.T) {
	block := NewMainACPTurnBlock("turn-retry-then-continue")
	block.UpdateToolWithMeta("read-1", "Read", "a.go", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	block.UpdateToolWithMeta("search-1", "Grep", `"needle" 3 hits`, "", true, false, ToolUpdateMeta{ToolKind: "search"})
	block.AddNotice("Retrying model request (1/5, retry in 1s)", time.Time{}, transcript.NoticeKindModelRetry)
	block.UpdateToolWithMeta("read-2", "Read", "b.go", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	block.UpdateToolWithMeta("search-2", "Grep", `"other" 2 hits`, "", true, false, ToolUpdateMeta{ToolKind: "search"})
	block.AppendStreamEvent(SEReasoning, "continue after retry", narrativeTestSource())

	for _, event := range block.Events {
		if isModelRetryNotice(event) {
			t.Fatalf("retry notice remained in events after later work: %#v", block.Events)
		}
	}
	wantRun := []string{"read-1", "search-1", "read-2", "search-2"}
	stableRuns := collectStableExplorationRuns(block.Events, block.Status)
	if len(stableRuns) != 1 || !slices.Equal(stableRuns[0], wantRun) {
		t.Fatalf("stable exploration runs = %#v, want one merged run %#v", stableRuns, [][]string{wantRun})
	}

	plain := joinRenderedPlain(block.Render(NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(100)))
	if got := countExactTrimmedLine(plain, "• Explored"); got != 1 {
		t.Fatalf("exploration groups = %d, want one merged group:\n%s", got, plain)
	}
	if strings.Contains(plain, "Retrying model request") {
		t.Fatalf("stale retry notice remained after later work:\n%s", plain)
	}
	if headers := standaloneExplorationHeaders(plain); len(headers) != 0 {
		t.Fatalf("later work flattened exploration into standalone headers %q:\n%s", headers, plain)
	}
	if !strings.Contains(plain, "continue after retry") {
		t.Fatalf("later reasoning disappeared:\n%s", plain)
	}
}

func TestTerminalLifecycleClearsRetryNotice(t *testing.T) {
	block := NewMainACPTurnBlock("turn-retry-terminal")
	block.UpdateToolWithMeta("read-1", "Read", "a.go", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	block.UpdateToolWithMeta("search-1", "Grep", `"needle" 3 hits`, "", true, false, ToolUpdateMeta{ToolKind: "search"})
	block.AddNotice("Retrying model request (1/5, retry in 1s)", time.Time{}, transcript.NoticeKindModelRetry)
	block.SetStatus("failed", "", "", time.Time{})

	for _, event := range block.Events {
		if isModelRetryNotice(event) {
			t.Fatalf("retry notice survived terminal lifecycle: %#v", block.Events)
		}
	}
	plain := joinRenderedPlain(block.Render(NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(100)))
	if strings.Contains(plain, "Retrying model request") {
		t.Fatalf("retry notice remained visible after terminal lifecycle:\n%s", plain)
	}
	if got := countExactTrimmedLine(plain, "• Explored"); got != 1 {
		t.Fatalf("exploration groups = %d, want one after terminal retry clear:\n%s", got, plain)
	}
}

type liveExplorationHarness struct {
	t      *testing.T
	model  *Model
	frames []string
}

func newLiveExplorationHarness(t *testing.T, width, height int) *liveExplorationHarness {
	t.Helper()
	model := NewModel(Config{
		NoColor:            true,
		NoAnimation:        true,
		StreamTickInterval: time.Millisecond,
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	model = updated.(*Model)
	return &liveExplorationHarness{
		t:      t,
		model:  model,
		frames: []string{model.View().Content},
	}
}

func (h *liveExplorationHarness) apply(env eventstream.Envelope) {
	h.t.Helper()
	updated, _ := h.model.Update(env)
	h.model = updated.(*Model)
	h.frames = append(h.frames, h.model.View().Content)
}

func (h *liveExplorationHarness) drainNarrative() {
	h.t.Helper()
	updated, _ := h.model.Update(frameTickMsg{kind: frameTickRenderDrain, at: time.Now().Add(time.Second)})
	h.model = updated.(*Model)
	h.frames = append(h.frames, h.model.View().Content)
	updated, _ = h.model.Update(frameTickMsg{kind: frameTickViewportSync, at: time.Now().Add(2 * time.Second)})
	h.model = updated.(*Model)
	h.frames = append(h.frames, h.model.View().Content)
}

func (h *liveExplorationHarness) reason(messageID, text string) {
	h.t.Helper()
	h.apply(liveExplorationNarrativeEnvelope(messageID, text))
	h.drainNarrative()
}

func (h *liveExplorationHarness) start(callID, name, kind, arg string) {
	h.t.Helper()
	h.apply(liveExplorationToolStartEnvelope(callID, name, kind, arg))
}

func (h *liveExplorationHarness) complete(callID, name, kind, arg string) {
	h.t.Helper()
	h.apply(liveExplorationToolCompleteEnvelope(callID, name, kind, arg))
}

func requireToolEventForTest(t *testing.T, events []SubagentEvent, callID string) SubagentEvent {
	t.Helper()
	for _, event := range events {
		if event.Kind == SEToolCall && event.CallID == callID {
			return event
		}
	}
	t.Fatalf("tool event %q not found in %#v", callID, events)
	return SubagentEvent{}
}

func liveExplorationNarrativeEnvelope(messageID, text string) eventstream.Envelope {
	return liveExplorationEnvelope(schema.ContentChunk{
		SessionUpdate: schema.UpdateAgentThought,
		MessageID:     messageID,
		Content:       schema.TextContent{Type: "text", Text: text},
	})
}

func liveExplorationToolStartEnvelope(callID, name, kind, arg string) eventstream.Envelope {
	return liveExplorationEnvelope(schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall,
		ToolCallID:    callID,
		Title:         name,
		Kind:          kind,
		Status:        schema.ToolStatusPending,
		RawInput:      liveExplorationToolInput(kind, arg),
		Meta:          acpToolNameMeta(name),
	})
}

func liveExplorationToolCompleteEnvelope(callID, name, kind, arg string) eventstream.Envelope {
	status := schema.ToolStatusCompleted
	return liveExplorationEnvelope(schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    callID,
		Title:         &name,
		Kind:          &kind,
		Status:        &status,
		RawInput:      liveExplorationToolInput(kind, arg),
		Meta:          acpToolNameMeta(name),
	})
}

func liveExplorationToolFailedEnvelope(callID, name, kind, arg string) eventstream.Envelope {
	status := schema.ToolStatusFailed
	return liveExplorationEnvelope(schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    callID,
		Title:         &name,
		Kind:          &kind,
		Status:        &status,
		RawInput:      liveExplorationToolInput(kind, arg),
		Meta:          acpToolNameMeta(name),
	})
}

func liveExplorationAttemptResetEnvelope(attempt, maxRetries int) eventstream.Envelope {
	envelope := liveExplorationLifecycleEnvelope("attempt_reset")
	envelope.Meta = map[string]any{
		"caelis": map[string]any{
			"runtime": map[string]any{
				"attempt_reset": map[string]any{
					"attempt":     attempt,
					"max_retries": maxRetries,
					"retrying":    true,
				},
			},
		},
	}
	return envelope
}

func liveExplorationLifecycleEnvelope(state string) eventstream.Envelope {
	envelope := liveExplorationEnvelope(nil)
	envelope.Kind = eventstream.KindLifecycle
	envelope.Lifecycle = &eventstream.Lifecycle{State: state}
	return envelope
}

func liveExplorationEnvelope(update schema.Update) eventstream.Envelope {
	return eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-live-exploration",
		HandleID:  "handle-live-exploration",
		RunID:     "run-live-exploration",
		TurnID:    "turn-live-exploration",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-live-exploration",
		Update:    update,
	}
}

func liveExplorationToolInput(kind, arg string) map[string]any {
	switch kind {
	case "search":
		return map[string]any{"pattern": arg, "path": "."}
	case "execute":
		return map[string]any{"command": arg}
	default:
		return map[string]any{"path": arg}
	}
}

func standaloneExplorationHeaders(rendered string) []string {
	var headers []string
	for _, line := range strings.Split(rendered, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "• Read ") || strings.HasPrefix(line, "• Search ") {
			headers = append(headers, line)
		}
	}
	return headers
}

func countExactTrimmedLine(rendered, want string) int {
	count := 0
	for _, line := range strings.Split(rendered, "\n") {
		if strings.TrimSpace(line) == want {
			count++
		}
	}
	return count
}
