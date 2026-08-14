package tuiapp

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/transcript"
	"github.com/charmbracelet/x/ansi"
)

func TestLiveExplorationGroupsSettledPendingStageWithoutCompletingIt(t *testing.T) {
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
	wantRun := []string{"read-1", "read-2", "search-middle", "read-middle", "read-last-1", "read-last-2"}
	if stableRuns := collectStableExplorationRuns(block.Events, block.Status); len(stableRuns) != 0 {
		t.Fatalf("pending exploration was reported as stable: %#v", stableRuns)
	}
	if len(block.explorationProjection.Containers) != 1 {
		t.Fatalf("exploration containers = %#v, want one pending run", block.explorationProjection.Containers)
	}
	container := block.explorationProjection.Containers[0]
	if !container.Pending || !slices.Equal(container.CallIDs, wantRun) {
		t.Fatalf("pending exploration container = %#v, want call IDs %#v", container, wantRun)
	}

	pendingFrame := h.model.View().Content
	plain := ansi.Strip(pendingFrame)
	if got := countExactTrimmedLine(plain, "• Exploring"); got != 1 {
		t.Fatalf("pending exploration groups = %d, want one merged live group:\n%s", got, plain)
	}
	if strings.Contains(plain, "• Explored") {
		t.Fatalf("pending tool was rendered under past-tense Explored:\n%s", plain)
	}
	if !strings.Contains(plain, "needle") {
		t.Fatalf("pending exploration summary hid the pending search:\n%s", plain)
	}
	if headers := standaloneExplorationHeaders(plain); len(headers) != 0 {
		t.Fatalf("settled pending batch leaked standalone headers %q:\n%s", headers, plain)
	}
	updates := renderFullscreenFramesForTest(t, width, height, h.frames...)
	assertPhysicalFullscreenFrame(t, width, height, pendingFrame, updates)

	if !block.toggleExplorationExpanded("read-1") {
		t.Fatal("pending exploration container did not expand")
	}
	expanded := joinRenderedPlain(block.Render(h.model.blockRenderContext(width)))
	if !strings.Contains(expanded, "Exploring") || !strings.Contains(expanded, "needle") {
		t.Fatalf("expanded pending container lost its live lifecycle:\n%s", expanded)
	}

	// Only the explicit result update promotes the same stable container key to
	// the past-tense completed representation.
	h.complete("search-middle", "Grep", "search", "needle")
	block = requireMainACPTurnBlockForTest(t, h.model)
	if event := requireToolEventForTest(t, block.Events, "search-middle"); !event.Done {
		t.Fatal("explicit result update did not complete the pending search")
	}
	stableRuns := collectStableExplorationRuns(block.Events, block.Status)
	if len(stableRuns) != 1 || !slices.Equal(stableRuns[0], wantRun) {
		t.Fatalf("stable exploration runs = %#v, want %#v", stableRuns, [][]string{wantRun})
	}
	completedPlain := ansi.Strip(h.model.View().Content)
	if got := countExactTrimmedLine(completedPlain, "• Explored"); got != 1 {
		t.Fatalf("completed exploration groups = %d, want one:\n%s", got, completedPlain)
	}
	if strings.Contains(completedPlain, "• Exploring") {
		t.Fatalf("completed exploration retained live header:\n%s", completedPlain)
	}
}

func TestTerminalPendingExplorationDoesNotRenderAsActiveContainer(t *testing.T) {
	block := NewMainACPTurnBlock("turn-terminal-pending")
	block.UpdateToolWithMeta("read-1", "Read", "a.go", "", false, false, ToolUpdateMeta{ToolKind: "read"})
	block.UpdateToolWithMeta("read-2", "Read", "b.go", "", false, false, ToolUpdateMeta{ToolKind: "read"})
	block.AppendStreamEvent(SEReasoning, "later terminal narrative", narrativeTestSource())
	block.Status = "completed"

	rows := block.Render(NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(100))
	plain := joinRenderedPlain(rows)
	if strings.Contains(plain, "• Exploring") || strings.Contains(plain, "• Explored") {
		t.Fatalf("terminal pending tools rendered as an exploration container:\n%s", plain)
	}
	if !strings.Contains(plain, "a.go") || !strings.Contains(plain, "b.go") {
		t.Fatalf("terminal pending tool lifecycle rows disappeared:\n%s", plain)
	}
}

func TestLiveExplorationReclassificationRemovesPendingContainerMembership(t *testing.T) {
	h := newLiveExplorationHarness(t, 100, 30)
	h.start("read-1", "Read", "read", "a.go")
	h.complete("read-1", "Read", "read", "a.go")
	h.start("reclassified", "Read", "read", "before.go")
	h.reason("reason-after-start", "continue after the pending batch")

	block := requireMainACPTurnBlockForTest(t, h.model)
	if len(block.explorationProjection.Containers) != 1 || !block.explorationProjection.Containers[0].Pending {
		t.Fatalf("test setup did not create a pending exploration container: %#v", block.explorationProjection.Containers)
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
	if !reclassified.Done || reclassified.Name == "Read" || reclassified.ToolKind != kind || isExplorationToolEvent(reclassified) {
		t.Fatalf("final tool identity was not applied: %#v", reclassified)
	}
	for _, container := range block.explorationProjection.Containers {
		if slices.Contains(container.CallIDs, "reclassified") {
			t.Fatalf("reclassified tool remained in exploration container: %#v", container)
		}
	}
	plain := ansi.Strip(h.model.View().Content)
	if strings.Contains(plain, "• Exploring") || strings.Contains(plain, "• Explored") {
		t.Fatalf("stale exploration container survived reclassification:\n%s", plain)
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
	})
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
