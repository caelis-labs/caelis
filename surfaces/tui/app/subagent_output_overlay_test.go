package tuiapp

import (
	"errors"
	"fmt"
	"image/color"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestSubagentOutputOverlayRendersFullAnchoredACPTranscript(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 100
	model.height = 32
	model.ready = true
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(300, 0))

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "spawn-1",
			Title:         "Spawn explorer: inspect task streams",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"agent": "explorer", "prompt": "inspect task streams"},
			Meta:          acpToolNameMeta("Spawn"),
		},
	})
	running := schema.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "spawn-1",
			Status:        &running,
			RawOutput:     map[string]any{"handle": "zuri", "state": "running"},
			Meta:          acpToolNameMeta("Spawn"),
		},
	})

	child := func(update schema.Update) eventstream.Envelope {
		return eventstream.Envelope{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			TurnID:    "child-turn-1",
			Scope:     eventstream.ScopeSubagent,
			ScopeID:   "zuri",
			Actor:     "explorer",
			ParentTool: &eventstream.ParentToolRelation{
				ToolCallID: "spawn-1",
				ToolName:   "Spawn",
			},
			Update: update,
		}
	}
	model = applyACPEnvelopeForTest(t, model, child(schema.ContentChunk{
		SessionUpdate: schema.UpdateAgentThought,
		MessageID:     "thought-1",
		Content:       schema.TextContent{Type: "text", Text: "checking the task directory"},
	}))
	model = applyACPEnvelopeForTest(t, model, child(schema.PlanUpdate{
		SessionUpdate: schema.UpdatePlan,
		Entries: []schema.PlanEntry{{
			Content: "inspect stream ownership",
			Status:  "in_progress",
		}},
	}))
	model = applyACPEnvelopeForTest(t, model, child(schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall,
		ToolCallID:    "child-tool-1",
		Title:         "Search taskstream",
		Kind:          schema.ToolKindRead,
		Status:        schema.ToolStatusInProgress,
		RawInput:      map[string]any{"query": "taskstream"},
	}))
	completed := schema.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, child(schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    "child-tool-1",
		Status:        &completed,
		RawOutput:     map[string]any{"matches": 4},
	}))
	model = applyACPEnvelopeForTest(t, model, child(schema.ContentChunk{
		SessionUpdate: schema.UpdateAgentMessage,
		MessageID:     "answer-1",
		Content:       schema.TextContent{Type: "text", Text: "The stream is Control-owned."},
	}))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindNotice,
		SessionID: "session-1",
		TurnID:    "child-turn-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "zuri",
		Actor:     "explorer",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "Spawn",
		},
		Notice: "retrying child request",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindError,
		SessionID: "session-1",
		TurnID:    "child-turn-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "zuri",
		Actor:     "explorer",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "Spawn",
		},
		Err: errors.New("child transport failed"),
	})

	block := requireMainACPTurnBlockForTest(t, model)
	model.syncViewportContent()
	mainTranscript := strings.Join(model.viewportPlainLines, "\n")
	if !viewportHasClickToken(model, subagentOutputOverlayClickToken("spawn-1")) {
		t.Fatalf("Spawn row omitted overlay click token:\n%s", mainTranscript)
	}
	for _, forbidden := range []string{"↗", "↗ output", " running", " done", " failed"} {
		if strings.Contains(mainTranscript, forbidden) {
			t.Fatalf("Spawn row retained visible status/output label %q:\n%s", forbidden, mainTranscript)
		}
	}
	if strings.Contains(mainTranscript, "checking the task directory") ||
		strings.Contains(mainTranscript, "The stream is Control-owned.") ||
		strings.Contains(mainTranscript, "retrying child request") ||
		strings.Contains(mainTranscript, "child transport failed") {
		t.Fatalf("full child transcript leaked into the main Spawn row:\n%s", mainTranscript)
	}

	if !model.tryToggleFoldToken(block.BlockID(), subagentOutputOverlayClickToken("spawn-1")) {
		t.Fatal("Spawn output click did not open the overlay")
	}
	if model.subagentOutputOverlay == nil {
		t.Fatal("subagent output overlay is nil after opening")
	}
	overlay := model.renderSubagentOutputOverlay()
	for _, want := range []string{
		"explorer",
		"checking the task directory",
		"inspect stream ownership",
		"Read",
		"The stream is Control-owned.",
		"retrying child request",
		"child transport failed",
		"esc close",
	} {
		if !strings.Contains(overlay, want) {
			t.Fatalf("subagent output overlay omitted %q:\n%s", want, overlay)
		}
	}

	next, _ := model.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = next.(*Model)
	if model.subagentOutputOverlay != nil {
		t.Fatal("Esc did not close the subagent output overlay")
	}
}

func TestSubagentOutputOverlayAnchorsApprovalReviewToObservedChildTool(t *testing.T) {
	t.Parallel()

	newModel := func(t *testing.T) *Model {
		t.Helper()
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model.width = 100
		model.height = 32
		model.ready = true
		model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(310, 0))
		return applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			TurnID:    "parent-turn",
			Scope:     eventstream.ScopeMain,
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall,
				ToolCallID:    "spawn-1",
				Title:         "Spawn breeze: inspect process state",
				Kind:          schema.ToolKindExecute,
				Status:        schema.ToolStatusInProgress,
				RawInput:      map[string]any{"agent": "breeze", "prompt": "inspect process state"},
				Meta:          acpToolNameMeta("Spawn"),
			},
		})
	}
	childTool := eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "child-turn",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "task-1",
		Actor:     "breeze",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "Spawn",
		},
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "child-command-1",
			Title:         "ps aux | head -5",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"command": "ps aux | head -5"},
		},
	}
	approvalReview := eventstream.Envelope{
		Kind:      eventstream.KindApprovalReview,
		SessionID: "session-1",
		// Approval review is emitted through the parent Turn even though its
		// scope and ToolCallID identify the child tool invocation.
		TurnID:  "parent-turn",
		Scope:   eventstream.ScopeSubagent,
		ScopeID: "task-1",
		Actor:   "breeze",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "Spawn",
		},
		ApprovalReview: &eventstream.ApprovalReview{
			ToolCallID:    "child-command-1",
			ToolName:      "RunCommand",
			RawInput:      map[string]any{"command": "ps aux | head -5"},
			Status:        "denied",
			Risk:          "low",
			Authorization: "high",
			Text:          "approval denied",
		},
	}

	t.Run("observed tool", func(t *testing.T) {
		t.Parallel()
		model := newModel(t)
		model = applyACPEnvelopeForTest(t, model, childTool)
		model = applyACPEnvelopeForTest(t, model, approvalReview)

		view := requireSubagentOutputViewForTest(t, model, "spawn-1")
		if view.document.Len() != 1 {
			t.Fatalf("overlay blocks = %d, want approval in the existing child Turn block", view.document.Len())
		}
		block, _ := view.document.Blocks()[0].(*ParticipantTurnBlock)
		if block == nil || len(block.Events) != 2 ||
			block.Events[0].Kind != SEToolCall || block.Events[0].CallID != "child-command-1" ||
			block.Events[1].Kind != SEApproval || block.Events[1].CallID != "child-command-1" {
			t.Fatalf("overlay events = %#v, want child tool followed by its approval review", block)
		}
		plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 20)), "\n")
		toolAt := strings.Index(plain, "ps aux | head -5")
		reviewAt := strings.Index(plain, "Automatic approval review denied")
		if toolAt < 0 || reviewAt <= toolAt {
			t.Fatalf("approval review did not render after its child tool:\n%s", plain)
		}
	})

	t.Run("review arrives before tool", func(t *testing.T) {
		t.Parallel()
		model := newModel(t)
		model = applyACPEnvelopeForTest(t, model, approvalReview)
		model = applyACPEnvelopeForTest(t, model, childTool)

		view := requireSubagentOutputViewForTest(t, model, "spawn-1")
		plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 20)), "\n")
		if strings.Contains(plain, "Automatic approval review") || strings.Contains(plain, "approval denied") {
			t.Fatalf("unanchored approval review was rendered in the overlay:\n%s", plain)
		}
		if !strings.Contains(plain, "ps aux | head -5") {
			t.Fatalf("child tool missing after an early approval review:\n%s", plain)
		}
	})
}

func TestSubagentOutputViewUsesSpawnOnlyForIdentityAndDropsSyntheticNoOutput(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	view := model.ensureSubagentOutputView("spawn-1")
	initialStatus := view.block.Status
	view.observeOwnerIdentity(SubagentEvent{
		Kind: SEToolCall, CallID: "spawn-1", Name: "Spawn",
		Args: "xena[breeze]: inspect", TaskHandle: "xena",
		Done: true, Output: "(no output)", OutputSynthetic: true,
	})
	view.observeChildEvent(TranscriptEvent{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent,
		Actor: "self", TurnID: "task-1:1", AnchorToolCallID: "spawn-1", AnchorToolName: "Spawn",
		NarrativeKind: TranscriptNarrativeReasoning, Text: "child reasoning",
	})

	if view.actor != "xena[breeze]" || view.block.Actor != "xena[breeze]" {
		t.Fatalf("overlay actor = %q/%q, want stable Spawn identity", view.actor, view.block.Actor)
	}
	if view.block.Status != initialStatus {
		t.Fatalf("Spawn owner changed child transcript status to %q", view.block.Status)
	}
	plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 20)), "\n")
	if !strings.Contains(plain, "child reasoning") || strings.Contains(plain, "(no output)") {
		t.Fatalf("overlay rows did not preserve child transcript without placeholder:\n%s", plain)
	}
}

func TestSubagentOutputWorkspaceRendersMultipleTurnsAsOneChronologicalTranscript(t *testing.T) {
	t.Parallel()

	startedAt := time.Unix(100, 0)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "zuri"
	view.actor = "zuri[breeze]"

	view.observeChildEvent(TranscriptEvent{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: "child-turn-1",
		NarrativeKind: TranscriptNarrativeUser, Actor: "user", Text: "inspect the repository",
	})
	view.observeChildEvent(TranscriptEvent{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: "child-turn-1",
		NarrativeKind: TranscriptNarrativeReasoning, Text: "first turn reasoning", OccurredAt: startedAt,
	})
	view.observeChildEvent(TranscriptEvent{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: "child-turn-1",
		NarrativeKind: TranscriptNarrativeAssistant, Text: "first turn complete", Final: true, OccurredAt: startedAt.Add(3 * time.Second),
	})
	view.observeChildEvent(TranscriptEvent{
		Kind: TranscriptEventLifecycle, Scope: ACPProjectionSubagent, TurnID: "child-turn-1",
		State: eventstream.LifecycleStateCompleted, OccurredAt: startedAt.Add(4 * time.Second),
	})
	view.observeChildEvent(TranscriptEvent{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: "child-turn-2",
		NarrativeKind: TranscriptNarrativeUser, Actor: "parent", Text: "check the follow-up",
	})
	view.observeChildEvent(TranscriptEvent{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: "child-turn-2",
		NarrativeKind: TranscriptNarrativeReasoning, Text: "second turn reasoning", OccurredAt: startedAt.Add(10 * time.Second),
	})
	view.observeChildEvent(TranscriptEvent{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: "child-turn-2",
		NarrativeKind: TranscriptNarrativeAssistant, Text: "second turn complete", Final: true, OccurredAt: startedAt.Add(17 * time.Second),
	})
	view.observeChildEvent(TranscriptEvent{
		Kind: TranscriptEventLifecycle, Scope: ACPProjectionSubagent, TurnID: "child-turn-2",
		State: eventstream.LifecycleStateCompleted, OccurredAt: startedAt.Add(18 * time.Second),
	})

	plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 40)), "\n")
	for _, want := range []string{
		"inspect the repository",
		"first turn reasoning",
		"first turn complete",
		"parent: check the follow-up",
		"second turn reasoning",
		"second turn complete",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("subagent workspace omitted %q:\n%s", want, plain)
		}
	}
	for _, forbidden := range []string{"Turn 1", "Turn 2", "Message from parent", "(no output)"} {
		if strings.Contains(plain, forbidden) {
			t.Fatalf("subagent workspace exposed internal lifecycle label %q:\n%s", forbidden, plain)
		}
	}
	for _, duration := range []string{"4.0s", "8.0s"} {
		if !strings.Contains(plain, duration) {
			t.Fatalf("subagent workspace omitted Turn footer %q:\n%s", duration, plain)
		}
	}
	if len(view.turnBlocks) != 2 {
		t.Fatalf("internal Turn groups = %d, want two", len(view.turnBlocks))
	}
}

func TestSubagentOutputOverlayPaintsOneBackgroundAcrossStyledTranscript(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoAnimation: true})
	model.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	model.theme.ModalBg = lipgloss.Color("#263244")
	model.theme.InvalidateTokens()
	model.themeCacheKey = ""
	model.width, model.height = 96, 28
	view := model.ensureSubagentOutputView("spawn-1")
	view.observeChildEvent(TranscriptEvent{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: "turn-1",
		NarrativeKind: TranscriptNarrativeReasoning, Text: "检查 Shell 的 ACP 线序",
	})
	view.observeChildEvent(TranscriptEvent{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: "turn-1",
		NarrativeKind: TranscriptNarrativeAssistant, Text: "统一渲染完成",
	})
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1", followTail: true}

	frame := model.renderSubagentOutputOverlay()
	layout := model.subagentOutputOverlay.layout
	assertOverlayCellBackgrounds(t, frame, layout.frameWidth, layout.frameHeight, model.theme.ModalBg)
}

func TestOpeningSubagentOutputOverlayDoesNotCompleteRunningChild(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	block := NewMainACPTurnBlock("turn-1")
	// The Spawn tool invocation is complete once it returns the handle. Its
	// child Task is independently still running.
	block.UpdateToolWithMeta(
		"spawn-1",
		"Spawn",
		"zuri[breeze]: inspect",
		"(no output)",
		true,
		false,
		ToolUpdateMeta{TaskHandle: "zuri", OutputSynthetic: true},
	)
	model.doc.Append(block)
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "zuri"
	view.block.Status = eventstream.LifecycleStateRunning

	if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-1") {
		t.Fatal("openSubagentOutputOverlay() = false")
	}
	if view.block.Status != eventstream.LifecycleStateRunning {
		t.Fatalf("opening overlay changed child state to %q, want running", view.block.Status)
	}
	if demand := model.taskStreamDemandForOwner("spawn-1", "zuri"); demand != taskStreamDemandVisibleSubagent {
		t.Fatalf("opening overlay changed Task stream demand to %v, want background observation", demand)
	}
	if view.actor != "zuri[breeze]" || view.taskHandle != "zuri" {
		t.Fatalf("opening overlay identity = actor %q handle %q", view.actor, view.taskHandle)
	}
}

func TestSubagentOutputOverlayKeepsPromptAboveOutput(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 100
	model.height = 28
	model.ready = true
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1", followTail: true}
	model.activePrompt = newPromptState(PromptRequestMsg{
		Title:  "Approval",
		Prompt: "Allow child tool?",
		Choices: []PromptChoice{{
			Label: "Allow",
			Value: "allow",
		}},
		Response: make(chan PromptResponse, 1),
	})

	frame := model.View().Content
	promptIndex := strings.LastIndex(frame, "Approval")
	outputIndex := strings.LastIndex(frame, "Subagent")
	if promptIndex < 0 || outputIndex < 0 {
		t.Fatalf("frame omitted prompt or subagent overlay:\n%s", frame)
	}
	if promptIndex < outputIndex {
		t.Fatalf("approval prompt rendered beneath subagent output overlay:\n%s", frame)
	}
}

func TestSubagentOutputOverlayDeduplicatesSameProjectionAcrossDeliveryPaths(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	event := TranscriptEvent{
		Kind:               TranscriptEventNarrative,
		Scope:              ACPProjectionSubagent,
		ScopeID:            "zuri",
		Actor:              "reviewer",
		AnchorToolCallID:   "spawn-1",
		AnchorToolName:     "Spawn",
		SourceEventID:      "child-event-1",
		SourceProjectionID: "child-event-1:0",
		MessageID:          "child-message-1",
		NarrativeKind:      TranscriptNarrativeAssistant,
		Text:               "one physical child chunk",
	}
	if changed := model.observeSubagentOutputEvents([]TranscriptEvent{event}); !changed {
		t.Fatal("first delivery did not mutate the subagent output view")
	}
	if changed := model.observeSubagentOutputEvents([]TranscriptEvent{event}); changed {
		t.Fatal("duplicate delivery reported a second subagent output mutation")
	}

	view := requireSubagentOutputViewForTest(t, model, "spawn-1")
	plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 20)), "\n")
	if got := strings.Count(plain, event.Text); got != 1 {
		t.Fatalf("same child projection rendered %d times across delivery paths:\n%s", got, plain)
	}
}

func TestSubagentOutputOverlayLatePlanDoesNotReopenTerminalStatus(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "zuri"
	view.observeChildEvent(TranscriptEvent{
		Kind:             TranscriptEventLifecycle,
		Scope:            ACPProjectionSubagent,
		ScopeID:          "zuri",
		AnchorToolCallID: "spawn-1",
		AnchorToolName:   "Spawn",
		State:            eventstream.LifecycleStateCompleted,
	})
	view.observeChildEvent(TranscriptEvent{
		Kind:             TranscriptEventPlan,
		Scope:            ACPProjectionSubagent,
		ScopeID:          "zuri",
		AnchorToolCallID: "spawn-1",
		AnchorToolName:   "Spawn",
		PlanEntries: []transcript.PlanEntry{{
			Content: "late plan projection",
			Status:  "completed",
		}},
	})

	if view.block.Status != eventstream.LifecycleStateCompleted {
		t.Fatalf("late PlanUpdate changed terminal status to %q", view.block.Status)
	}
	if demand := model.taskStreamDemandForOwner("spawn-1", "zuri"); demand != taskStreamDemandNone {
		t.Fatalf("late PlanUpdate restored Task stream demand: %v", demand)
	}
}

func TestSubagentOutputOverlayScrollUsesCachedRows(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 100
	model.height = 28
	view := model.ensureSubagentOutputView("spawn-1")
	for index := range 1_000 {
		view.observeChildEvent(TranscriptEvent{
			Kind:       TranscriptEventNotice,
			Scope:      ACPProjectionSubagent,
			ScopeID:    "zuri",
			Text:       fmt.Sprintf("child output row %04d", index),
			NoticeKind: "",
		})
	}
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1", followTail: true}

	_ = model.renderSubagentOutputOverlay()
	if got := view.renderCache.renders; got != 1 {
		t.Fatalf("initial overlay render count = %d, want one", got)
	}
	for range 40 {
		model.scrollSubagentOutputOverlay(-1)
		_ = model.renderSubagentOutputOverlay()
	}
	if got := view.renderCache.renders; got != 1 {
		t.Fatalf("scroll rerendered the full child transcript %d times, want cached one", got)
	}

	view.observeChildEvent(TranscriptEvent{
		Kind:          TranscriptEventNarrative,
		Scope:         ACPProjectionSubagent,
		ScopeID:       "zuri",
		NarrativeKind: TranscriptNarrativeAssistant,
		Text:          "new live child output",
	})
	_ = model.renderSubagentOutputOverlay()
	if got := view.renderCache.renders; got != 1 {
		t.Fatalf("live event bypassed render coalescing: renders=%d", got)
	}
	refresh := model.requestSubagentOutputRender()
	if refresh == nil {
		t.Fatal("visible dirty child transcript did not schedule a render")
	}
	next, _ := model.handleSubagentOutputRenderTick(refresh().(subagentOutputRenderTickMsg))
	model = next.(*Model)
	model.subagentOutputOverlay.followTail = true
	rendered := model.renderSubagentOutputOverlay()
	if got := view.renderCache.renders; got != 2 {
		t.Fatalf("coalesced child refresh render count = %d, want two total", got)
	}
	if !strings.Contains(rendered, "new live child output") {
		t.Fatalf("coalesced child refresh omitted new output:\n%s", rendered)
	}
}

func TestVisibleSubagentOutputOverlaySchedulesRefreshFromTranscriptPath(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 100
	model.height = 28
	view := model.ensureSubagentOutputView("spawn-1")
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1", followTail: true}
	_ = model.renderSubagentOutputOverlay()

	next, cmd := model.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: []TranscriptEvent{{
		Kind:             TranscriptEventNarrative,
		Scope:            ACPProjectionSubagent,
		ScopeID:          "zuri",
		Actor:            "reviewer",
		AnchorToolCallID: "spawn-1",
		AnchorToolName:   "Spawn",
		NarrativeKind:    TranscriptNarrativeAssistant,
		Text:             "live child output from the Session projection",
	}}})
	model = next.(*Model)
	if cmd == nil || !view.renderScheduled {
		t.Fatal("visible child transcript mutation did not schedule a coalesced overlay refresh")
	}

	next, _ = model.handleSubagentOutputRenderTick(subagentOutputRenderTickMsg{callID: "spawn-1"})
	model = next.(*Model)
	rendered := model.renderSubagentOutputOverlay()
	if !strings.Contains(rendered, "live child output from the Session projection") {
		t.Fatalf("coalesced Session-projected child refresh omitted new output:\n%s", rendered)
	}
}

func TestSubagentOutputOverlayMouseCloseDoesNotInterruptRunningTurn(t *testing.T) {
	var interruptCalls atomic.Int32
	model := NewModel(Config{
		NoColor:     true,
		NoAnimation: true,
		CancelRunning: func() bool {
			interruptCalls.Add(1)
			return true
		},
	})
	model.width = 100
	model.height = 28
	model.ready = true
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(326, 0))
	model.ensureSubagentOutputView("spawn-1")
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1", followTail: true}
	_ = model.renderSubagentOutputOverlay()

	geometry := model.subagentOutputOverlay.geometry
	closeMouse := tea.Mouse{Button: tea.MouseLeft, X: geometry.closeX, Y: geometry.closeY}
	next, _ := model.handleMouse(tea.MouseClickMsg(closeMouse))
	model = next.(*Model)
	next, _ = model.handleMouse(tea.MouseReleaseMsg(closeMouse))
	model = next.(*Model)
	if model.subagentOutputOverlay != nil {
		t.Fatal("mouse release on × did not close the subagent output overlay")
	}

	for range 3 {
		next, _ = model.handleMouse(tea.MouseClickMsg(closeMouse))
		model = next.(*Model)
		next, _ = model.handleMouse(tea.MouseReleaseMsg(closeMouse))
		model = next.(*Model)
	}
	if got := interruptCalls.Load(); got != 0 {
		t.Fatalf("repeated overlay close clicks requested %d main-Turn interrupts", got)
	}
}

func TestSubagentOutputOverlayDragSelectionCopiesWideText(t *testing.T) {
	var copied string
	model := NewModel(Config{
		NoAnimation: true,
		WriteClipboardText: func(text string) error {
			copied = text
			return nil
		},
	})
	model.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	model.themeCacheKey = ""
	model.width = 100
	model.height = 28
	view := model.ensureSubagentOutputView("spawn-1")
	source := newNarrativeSourceIdentity("message-1", "", "")
	view.block.AppendStreamEvent(SEAssistant, "前缀 甲乙abc 后缀", source, time.Unix(327, 0))
	model.subagentOutputOverlay = &subagentOutputOverlayState{
		callID:      "spawn-1",
		followTail:  false,
		selectStart: textSelectionPoint{line: -1, col: -1},
		selectEnd:   textSelectionPoint{line: -1, col: -1},
	}

	before := model.renderSubagentOutputOverlay()
	rowIndex, startCol := subagentOutputMarkerPositionForTest(t, model, "甲乙abc")
	geometry := model.subagentOutputOverlay.geometry
	startMouse := tea.Mouse{
		Button: tea.MouseLeft,
		X:      geometry.contentX + startCol,
		Y:      geometry.contentY + rowIndex - model.subagentOutputOverlay.offset,
	}
	endMouse := startMouse
	endMouse.X += displayColumns("甲乙abc")

	next, cmd := model.handleMouse(tea.MouseClickMsg(startMouse))
	model = next.(*Model)
	if cmd != nil {
		t.Fatal("selection press unexpectedly returned a command")
	}
	next, _ = model.handleMouse(tea.MouseMotionMsg(endMouse))
	model = next.(*Model)
	if !model.subagentOutputOverlay.selecting {
		t.Fatal("overlay did not enter selecting state")
	}
	if highlighted := model.renderSubagentOutputOverlay(); highlighted == before {
		t.Fatal("overlay selection did not change the rendered frame")
	}

	endMouse.Button = tea.MouseNone
	next, cmd = model.handleMouse(tea.MouseReleaseMsg(endMouse))
	model = next.(*Model)
	if cmd == nil {
		t.Fatal("overlay selection release did not return a clipboard command")
	}
	if got, ok := cmd().(clipboardCopyResultMsg); !ok {
		t.Fatalf("clipboard command returned %T, want clipboardCopyResultMsg", got)
	} else if got.err != nil {
		t.Fatalf("clipboard command returned error: %v", got.err)
	}
	if copied != "甲乙abc" {
		t.Fatalf("clipboard text = %q, want %q", copied, "甲乙abc")
	}
	if model.subagentOutputOverlay.selecting ||
		model.subagentOutputOverlay.selectStart.line >= 0 ||
		model.subagentOutputOverlay.selectEnd.line >= 0 {
		t.Fatalf("overlay selection was not cleared after copy: %#v", model.subagentOutputOverlay)
	}
}

func TestSubagentOutputOverlaySelectionExcludesNarrativeMarkerColumn(t *testing.T) {
	var copied string
	model := NewModel(Config{
		NoAnimation: true,
		WriteClipboardText: func(text string) error {
			copied = text
			return nil
		},
	})
	model.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	model.themeCacheKey = ""
	model.width = 100
	model.height = 28
	view := model.ensureSubagentOutputView("spawn-marker")
	view.block.AppendStreamEvent(
		SEAssistant,
		"先确认未提交改动的范围",
		newNarrativeSourceIdentity("message-marker", "", ""),
		time.Unix(328, 0),
	)
	model.subagentOutputOverlay = &subagentOutputOverlayState{
		callID:      "spawn-marker",
		followTail:  false,
		selectStart: textSelectionPoint{line: -1, col: -1},
		selectEnd:   textSelectionPoint{line: -1, col: -1},
	}

	_ = model.renderSubagentOutputOverlay()
	rowIndex, _ := subagentOutputMarkerPositionForTest(t, model, "先确认未提交改动的范围")
	row := view.renderCache.rows[rowIndex]
	if row.selectionIndent != displayColumns("· ") {
		t.Fatalf("overlay selection indent = %d, want role-marker width", row.selectionIndent)
	}
	geometry := model.subagentOutputOverlay.geometry
	startMouse := tea.Mouse{
		Button: tea.MouseLeft,
		X:      geometry.contentX,
		Y:      geometry.contentY + rowIndex - model.subagentOutputOverlay.offset,
	}
	endMouse := startMouse
	endMouse.X += displayColumns(row.Plain)

	next, _ := model.handleMouse(tea.MouseClickMsg(startMouse))
	model = next.(*Model)
	if got := model.subagentOutputOverlay.selectStart.col; got != row.selectionIndent {
		t.Fatalf("overlay selection start = %d, want content indent %d", got, row.selectionIndent)
	}
	next, _ = model.handleMouse(tea.MouseMotionMsg(endMouse))
	model = next.(*Model)
	highlighted := model.renderSubagentOutputOverlay()
	if marker := model.theme.InputSelectionStyle().Render("· "); strings.Contains(highlighted, marker) {
		t.Fatalf("overlay highlighted decorative marker column: %q", highlighted)
	}

	endMouse.Button = tea.MouseNone
	_, cmd := model.handleMouse(tea.MouseReleaseMsg(endMouse))
	if cmd == nil {
		t.Fatal("overlay marker-column selection did not return a clipboard command")
	}
	if result, ok := cmd().(clipboardCopyResultMsg); !ok {
		t.Fatalf("clipboard command returned %T, want clipboardCopyResultMsg", result)
	} else if result.err != nil {
		t.Fatalf("clipboard command returned error: %v", result.err)
	}
	if copied != "先确认未提交改动的范围" {
		t.Fatalf("clipboard text = %q, want marker-free narrative", copied)
	}
}

func TestSubagentOutputOverlayActiveNarrativeProtectionFitsContentWidth(t *testing.T) {
	model := NewModel(Config{NoAnimation: true})
	model.width = 56
	model.height = 20
	view := model.ensureSubagentOutputView("spawn-active-width")
	view.block.AppendStreamEvent(
		SEAssistant,
		"第一行\n第二行",
		newNarrativeSourceIdentity("message-active-width", "", ""),
		time.Unix(329, 0),
	)
	model.subagentOutputOverlay = &subagentOutputOverlayState{
		callID:      "spawn-active-width",
		followTail:  true,
		selectStart: textSelectionPoint{line: -1, col: -1},
		selectEnd:   textSelectionPoint{line: -1, col: -1},
	}

	_ = model.renderSubagentOutputOverlay()
	width := model.subagentOutputOverlay.geometry.contentWidth
	activeRows := 0
	for i, row := range view.renderCache.rows {
		if !row.activeTail {
			continue
		}
		activeRows++
		if got := displayColumns(row.Styled); got != width {
			t.Fatalf("overlay active row width = %d, want %d: plain=%q styled=%q", got, width, row.Plain, row.Styled)
		}
		if !strings.Contains(row.Styled, wideCellRendererSentinel()) ||
			!strings.Contains(view.renderCache.fixedRows[i], wideCellRendererSentinel()) {
			t.Fatalf("overlay active row lost repaint sentinel: row=%q fixed=%q", row.Styled, view.renderCache.fixedRows[i])
		}
	}
	if activeRows < 2 {
		t.Fatalf("overlay active rows = %#v, want protected multiline tail", view.renderCache.rows)
	}
}

func TestSubagentOutputOverlaySelectionWheelExtendsAcrossRows(t *testing.T) {
	var copied string
	model := NewModel(Config{
		NoColor:     true,
		NoAnimation: true,
		WriteClipboardText: func(text string) error {
			copied = text
			return nil
		},
	})
	model.width = 72
	model.height = 18
	view := model.ensureSubagentOutputView("spawn-1")
	for index := range 40 {
		view.observeChildEvent(TranscriptEvent{
			Kind:       TranscriptEventNotice,
			Scope:      ACPProjectionSubagent,
			ScopeID:    "reviewer",
			Text:       fmt.Sprintf("selectable row %02d", index),
			NoticeKind: "test",
		})
	}
	model.subagentOutputOverlay = &subagentOutputOverlayState{
		callID:      "spawn-1",
		followTail:  false,
		selectStart: textSelectionPoint{line: -1, col: -1},
		selectEnd:   textSelectionPoint{line: -1, col: -1},
	}
	_ = model.renderSubagentOutputOverlay()

	geometry := model.subagentOutputOverlay.geometry
	mouse := tea.Mouse{
		Button: tea.MouseLeft,
		X:      geometry.contentX,
		Y:      geometry.contentY,
	}
	next, _ := model.handleMouse(tea.MouseClickMsg(mouse))
	model = next.(*Model)
	next, _ = model.handleMouse(tea.MouseWheelMsg(tea.Mouse{
		Button: tea.MouseWheelDown,
		X:      mouse.X,
		Y:      mouse.Y,
	}))
	model = next.(*Model)
	if model.subagentOutputOverlay.offset != 1 {
		t.Fatalf("selection wheel offset = %d, want 1", model.subagentOutputOverlay.offset)
	}
	if model.subagentOutputOverlay.selectEnd.line != 1 {
		t.Fatalf("selection end line = %d, want 1 after scrolling", model.subagentOutputOverlay.selectEnd.line)
	}
	if model.subagentOutputOverlay.followTail {
		t.Fatal("selection wheel unexpectedly resumed tail following")
	}

	edgeMouse := tea.Mouse{
		Button: tea.MouseLeft,
		X:      geometry.contentX,
		Y:      geometry.contentY + len(geometry.rowTokens) - 1,
	}
	next, cmd := model.handleMouse(tea.MouseMotionMsg(edgeMouse))
	model = next.(*Model)
	if cmd == nil || !model.selectionAutoScroll.active || !model.selectionAutoScroll.tickScheduled {
		t.Fatalf("edge drag did not schedule overlay selection auto-scroll: %#v", model.selectionAutoScroll)
	}
	token := model.selectionAutoScroll.scheduledToken
	_ = model.advanceSelectionAutoScroll(token)
	if model.subagentOutputOverlay.offset != 2 {
		t.Fatalf("selection auto-scroll offset = %d, want 2", model.subagentOutputOverlay.offset)
	}

	_, cmd = model.handleMouse(tea.MouseReleaseMsg(edgeMouse))
	if cmd == nil {
		t.Fatal("multi-row overlay selection did not return a clipboard command")
	}
	if got, ok := cmd().(clipboardCopyResultMsg); !ok {
		t.Fatalf("clipboard command returned %T, want clipboardCopyResultMsg", got)
	} else if got.err != nil {
		t.Fatalf("clipboard command returned error: %v", got.err)
	}
	if !strings.Contains(copied, "\n") {
		t.Fatalf("wheel-extended clipboard text omitted row boundary: %q", copied)
	}
}

func TestSubagentOutputOverlayRunCommandUsesParticipantPanelDefaults(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 100
	model.height = 28
	view := model.ensureSubagentOutputView("spawn-1")
	view.observeChildEvent(TranscriptEvent{
		Kind:       TranscriptEventTool,
		ToolCallID: "command-date",
		ToolName:   "RunCommand",
		ToolKind:   "execute",
		ToolArgs:   "date",
	})
	view.observeChildEvent(TranscriptEvent{
		Kind:       TranscriptEventTool,
		ToolCallID: "command-date",
		ToolName:   "RunCommand",
		ToolKind:   "execute",
		ToolArgs:   "date",
		ToolOutput: "Thu Jul 30 16:42:00 CST 2026",
		Final:      true,
	})
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1", followTail: true}

	overlay := subagentOutputOverlayPlain(model)
	if !strings.Contains(overlay, "• Ran date") ||
		!strings.Contains(overlay, "Thu Jul 30 16:42:00 CST 2026") {
		t.Fatalf("completed RunCommand did not retain the participant transcript defaults:\n%s", overlay)
	}
	if !view.block.toolPanelExpanded("command-date") {
		t.Fatal("completed RunCommand was collapsed only in the subagent overlay")
	}
	for _, token := range model.subagentOutputOverlay.geometry.rowTokens {
		if token == acpToolPanelClickToken("command-date") {
			t.Fatalf("short RunCommand exposed a click target with no hidden details:\n%s", overlay)
		}
	}
}

func TestSubagentOutputOverlayLongRunCommandMouseTogglesFullAndSummary(t *testing.T) {
	var copied string
	model := NewModel(Config{
		NoColor:     true,
		NoAnimation: true,
		WriteClipboardText: func(text string) error {
			copied = text
			return nil
		},
	})
	model.width = 100
	model.height = 28
	view := model.ensureSubagentOutputView("spawn-1")
	outputLines := make([]string, acpTerminalPanelMaxLines+8)
	for index := range outputLines {
		outputLines[index] = fmt.Sprintf("command output line %02d", index)
	}
	view.observeChildEvent(TranscriptEvent{
		Kind:       TranscriptEventTool,
		ToolCallID: "command-ls",
		ToolName:   "RunCommand",
		ToolKind:   "execute",
		ToolArgs:   "ls -la",
	})
	view.observeChildEvent(TranscriptEvent{
		Kind:       TranscriptEventTool,
		ToolCallID: "command-ls",
		ToolName:   "RunCommand",
		ToolKind:   "execute",
		ToolArgs:   "ls -la",
		ToolOutput: strings.Join(outputLines, "\n"),
		Final:      true,
	})
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1", followTail: true}

	summary := subagentOutputOverlayPlain(model)
	middleLine := outputLines[len(outputLines)/2]
	if !strings.Contains(summary, "... +") || strings.Contains(summary, middleLine) {
		t.Fatalf("long RunCommand did not default to the bounded participant summary:\n%s", summary)
	}

	rowIndex, commandCol := subagentOutputMarkerPositionForTest(t, model, "ls -la")
	geometry := model.subagentOutputOverlay.geometry
	startMouse := tea.Mouse{
		Button: tea.MouseLeft,
		X:      geometry.contentX + commandCol,
		Y:      geometry.contentY + rowIndex - model.subagentOutputOverlay.offset,
	}
	endMouse := startMouse
	endMouse.X += displayColumns("ls")
	next, _ := model.handleMouse(tea.MouseClickMsg(startMouse))
	model = next.(*Model)
	next, _ = model.handleMouse(tea.MouseMotionMsg(endMouse))
	model = next.(*Model)
	next, cmd := model.handleMouse(tea.MouseReleaseMsg(endMouse))
	model = next.(*Model)
	if cmd == nil {
		t.Fatal("dragging a clickable command row did not return a clipboard command")
	}
	if got, ok := cmd().(clipboardCopyResultMsg); !ok {
		t.Fatalf("clipboard command returned %T, want clipboardCopyResultMsg", got)
	} else if got.err != nil {
		t.Fatalf("clipboard command returned error: %v", got.err)
	}
	if copied != "ls" {
		t.Fatalf("clickable command row copied %q, want %q", copied, "ls")
	}
	if view.block.toolPanelFullOutput("command-ls") {
		t.Fatal("drag selection over a clickable command row toggled its panel")
	}

	clickSubagentOutputToolPanelForTest(t, model, "command-ls")
	full := subagentOutputOverlayPlain(model)
	if !view.block.toolPanelFullOutput("command-ls") || !strings.Contains(full, middleLine) {
		t.Fatalf("overlay mouse click did not expand the complete command output:\n%s", full)
	}

	clickSubagentOutputToolPanelForTest(t, model, "command-ls")
	summary = subagentOutputOverlayPlain(model)
	if view.block.toolPanelFullOutput("command-ls") ||
		!view.block.toolPanelExpanded("command-ls") ||
		!strings.Contains(summary, "... +") ||
		strings.Contains(summary, middleLine) {
		t.Fatalf("second overlay mouse click did not return to the bounded summary:\n%s", summary)
	}
}

func subagentOutputMarkerPositionForTest(t *testing.T, model *Model, marker string) (int, int) {
	t.Helper()
	if model == nil || model.subagentOutputOverlay == nil {
		t.Fatal("subagent output overlay is unavailable")
	}
	view := model.subagentOutputViews[model.subagentOutputOverlay.callID]
	if view == nil {
		t.Fatal("subagent output view is unavailable")
	}
	for rowIndex, row := range view.renderCache.rows {
		if byteIndex := strings.Index(row.Plain, marker); byteIndex >= 0 {
			return rowIndex, displayColumns(row.Plain[:byteIndex])
		}
	}
	t.Fatalf("visible overlay rows omitted marker %q", marker)
	return 0, 0
}

func clickSubagentOutputToolPanelForTest(t *testing.T, model *Model, callID string) {
	t.Helper()
	if model == nil || model.subagentOutputOverlay == nil {
		t.Fatal("subagent output overlay is unavailable")
	}
	token := acpToolPanelClickToken(callID)
	geometry := model.subagentOutputOverlay.geometry
	rowIndex := -1
	for index, rowToken := range geometry.rowTokens {
		if rowToken == token {
			rowIndex = index
			break
		}
	}
	if rowIndex < 0 {
		t.Fatalf("visible overlay rows omitted tool panel token %q: %#v", token, geometry.rowTokens)
	}
	mouse := tea.Mouse{
		Button: tea.MouseLeft,
		X:      geometry.x + maxInt(1, geometry.contentWidth/2),
		Y:      geometry.contentY + rowIndex,
	}
	next, _ := model.handleMouse(tea.MouseClickMsg(mouse))
	model = next.(*Model)
	next, _ = model.handleMouse(tea.MouseReleaseMsg(mouse))
	if next.(*Model) != model {
		t.Fatal("overlay mouse release unexpectedly replaced the model")
	}
}

func TestSubagentOutputOverlayTitleUsesOnlySemanticDot(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	view := model.ensureSubagentOutputView("spawn-1")
	view.actor = "reviewer"
	view.block.Actor = "reviewer"
	view.block.Status = eventstream.LifecycleStateCompleted

	title := ansi.Strip(model.renderSubagentOutputTitle(view, 72))
	for _, want := range []string{"•", "Subagent", "reviewer", "×"} {
		if !strings.Contains(title, want) {
			t.Fatalf("overlay title omitted %q: %q", want, title)
		}
	}
	for _, forbidden := range []string{"output", "running", "done", "failed"} {
		if strings.Contains(strings.ToLower(title), forbidden) {
			t.Fatalf("overlay title retained visible status/output label %q: %q", forbidden, title)
		}
	}
}

func TestSpawnToolRowUsesOrdinaryHeaderAndOverlayLink(t *testing.T) {
	model := NewModel(Config{})
	model.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	model.themeCacheKey = ""
	base := SubagentEvent{
		Kind:   SEToolCall,
		CallID: "spawn-1",
		Name:   "Spawn",
		Args:   "reviewer: inspect",
	}

	render := func(event SubagentEvent, frame string, animationsEnabled bool) RenderedRow {
		t.Helper()
		ctx := model.blockRenderContext(96)
		ctx.SpinnerView = frame
		ctx.AnimationsEnabled = animationsEnabled
		rows := renderACPSpawnToolRows("block-1", event, event.CallID, 96, ctx)
		if len(rows) != 1 {
			t.Fatalf("Spawn rows = %#v, want one ordinary tool entry", rows)
		}
		return rows[0]
	}

	runningBright := render(base, runningSpinnerFrames[0], true)
	runningDim := render(base, runningSpinnerFrames[len(runningSpinnerFrames)/2], true)
	if runningBright.Plain != "• Spawned reviewer: inspect" {
		t.Fatalf("running Spawn row = %q", runningBright.Plain)
	}
	if runningBright.ClickToken != subagentOutputOverlayClickToken("spawn-1") {
		t.Fatalf("Spawn click token = %q", runningBright.ClickToken)
	}
	if !runningBright.ACPHeader || runningBright.selectionIndent != 2 {
		t.Fatalf("Spawn row lost ordinary ACP header semantics: %#v", runningBright)
	}
	if runningBright.Styled != runningDim.Styled {
		t.Fatal("ordinary Spawn row still changes with the spinner phase")
	}

	completed := base
	completed.Done = true
	completedRow := render(completed, runningSpinnerFrames[0], true)
	failed := completed
	failed.Err = true
	failedRow := render(failed, runningSpinnerFrames[0], true)
	if completedRow.Plain != runningBright.Plain || completedRow.Styled != runningBright.Styled ||
		failedRow.Plain != runningBright.Plain || failedRow.Styled != runningBright.Styled {
		t.Fatal("Spawn transcript row still exposes child lifecycle state")
	}
}

func TestSpawnToolRowDoesNotScheduleIndependentAnimation(t *testing.T) {
	model := NewModel(Config{})
	block := NewMainACPTurnBlock("turn-1")
	block.Events = append(block.Events, SubagentEvent{
		Kind:   SEToolCall,
		CallID: "spawn-1",
		Name:   "Spawn",
		Args:   "reviewer: inspect",
	})
	model.doc.Append(block)

	if model.runningIndicatorActive() {
		t.Fatal("background subagent changed the main running indicator")
	}
	if model.subagentOutputPulseActive() || model.animationIndicatorActive() {
		t.Fatal("ordinary Spawn row retained an independent status animation")
	}
	if cmd := model.scheduleSpinnerTick(); cmd != nil {
		t.Fatal("ordinary Spawn row scheduled a spinner tick")
	}
}

func TestSubagentOutputPulseSchedulesForOpenOverlay(t *testing.T) {
	model := NewModel(Config{})
	view := model.ensureSubagentOutputView("spawn-1")
	view.block.Status = "running"
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1"}

	if !model.subagentOutputPulseActive() || !model.animationIndicatorActive() {
		t.Fatal("open running subagent overlay did not activate its status animation")
	}
	if cmd := model.scheduleSpinnerTick(); cmd == nil {
		t.Fatal("open running subagent overlay did not schedule a spinner tick")
	}
}

func TestSubagentOutputOverlayIsResponsiveAndPinsScrolledHistory(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 60
	model.height = 18
	model.ready = true
	view := model.ensureSubagentOutputView("spawn-1")
	view.actor = "reviewer"
	view.block.Actor = "reviewer"
	lines := make([]string, 40)
	for index := range lines {
		lines[index] = "output line " + strings.Repeat("x", index%4)
	}
	source := newNarrativeSourceIdentity("message-1", "", "")
	view.block.AppendStreamEvent(SEAssistant, strings.Join(lines, "\n"), source, time.Unix(310, 0))
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1", followTail: true}

	frame := model.renderSubagentOutputOverlay()
	if width := lipgloss.Width(frame); width > model.width {
		t.Fatalf("narrow overlay width = %d, terminal width = %d:\n%s", width, model.width, frame)
	}
	if height := len(strings.Split(frame, "\n")); height > model.height {
		t.Fatalf("narrow overlay height = %d, terminal height = %d:\n%s", height, model.height, frame)
	}
	if model.subagentOutputOverlay.offset == 0 {
		t.Fatal("long running output did not follow its tail")
	}

	model.scrollSubagentOutputOverlay(-1)
	pinnedOffset := model.subagentOutputOverlay.offset
	view.block.AppendStreamEvent(SEAssistant, "\nnew tail", source, time.Unix(311, 0))
	_ = model.renderSubagentOutputOverlay()
	if model.subagentOutputOverlay.followTail || model.subagentOutputOverlay.offset != pinnedOffset {
		t.Fatalf(
			"scrolled output moved after a new chunk: follow=%v offset=%d want=%d",
			model.subagentOutputOverlay.followTail,
			model.subagentOutputOverlay.offset,
			pinnedOffset,
		)
	}

	_, _ = model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	_ = model.renderSubagentOutputOverlay()
	if !model.subagentOutputOverlay.followTail ||
		model.subagentOutputOverlay.offset != model.subagentOutputOverlayMaxOffset() {
		t.Fatalf(
			"End did not resume tail following: follow=%v offset=%d max=%d",
			model.subagentOutputOverlay.followTail,
			model.subagentOutputOverlay.offset,
			model.subagentOutputOverlayMaxOffset(),
		)
	}
}

func TestSubagentOutputOverlayWrapsLongACPHeadersAtNarrowWidth(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 60
	model.height = 18
	model.ready = true
	view := model.ensureSubagentOutputView("spawn-narrow")
	view.actor = "reviewer"
	view.block.Actor = "reviewer"
	view.block.Events = []SubagentEvent{{
		Kind:   SEToolCall,
		CallID: "command-long",
		Name:   "RunCommand",
		Args:   "go test ./surfaces/tui/app -run TestSubagentOutputOverlayWrapsLongACPHeadersAtNarrowWidth -count=1",
		Output: "ok",
		Done:   true,
	}}
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-narrow", followTail: true}

	layout := model.subagentOutputLayout(model.subagentOutputOverlay)
	rows := model.subagentOutputRows(view, layout.innerWidth, layout.contentRows)
	plain := strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(plain, "\n  │ ") {
		t.Fatalf("narrow overlay did not use the ACP header continuation rail:\n%s", plain)
	}
	for _, row := range rows {
		if width := displayColumns(row.Plain); width > layout.innerWidth {
			t.Fatalf("overlay row width = %d, content width = %d: %q", width, layout.innerWidth, row.Plain)
		}
	}
	frame := model.renderSubagentOutputOverlay()
	updates := renderFullscreenFramesForTest(t, model.width, model.height, frame)
	assertPhysicalFullscreenFrame(t, model.width, model.height, frame, updates)
}

func TestSubagentOutputOverlayFixedComposerMatchesResponsiveFrame(t *testing.T) {
	t.Parallel()

	for _, size := range []struct {
		name   string
		width  int
		height int
		color  bool
	}{
		{name: "bordered", width: 100, height: 28},
		{name: "borderless", width: 60, height: 18},
		{name: "bordered-color", width: 100, height: 28, color: true},
		{name: "borderless-color", width: 60, height: 18, color: true},
	} {
		t.Run(size.name, func(t *testing.T) {
			t.Parallel()

			model := NewModel(Config{NoColor: !size.color, NoAnimation: true})
			if size.color {
				model.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
				model.themeCacheKey = ""
			}
			model.width = size.width
			model.height = size.height
			view := model.ensureSubagentOutputView("spawn-1")
			for index := range 80 {
				view.observeChildEvent(TranscriptEvent{
					Kind:       TranscriptEventNotice,
					Scope:      ACPProjectionSubagent,
					ScopeID:    "reviewer",
					Text:       fmt.Sprintf("响应式输出行 %03d", index),
					NoticeKind: "test",
				})
			}
			model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1", followTail: true}

			got := model.renderSubagentOutputOverlay()
			want := legacySubagentOutputOverlayFrameForTest(model)
			want = paintLegacyOverlayBodyBackgroundForTest(want, model.overlayUsesBorder(), model.theme.Tokens().OverlayBg.GetBackground())
			assertStyledOverlayFramesEqual(t, got, want, size.width, size.height)
		})
	}
}

func paintLegacyOverlayBodyBackgroundForTest(frame string, bordered bool, background color.Color) string {
	if background == nil {
		return frame
	}
	width := lipgloss.Width(frame)
	height := len(strings.Split(frame, "\n"))
	screen := uv.NewScreenBuffer(width, height)
	screen.Method = ansi.GraphemeWidth
	uv.NewStyledString(frame).Draw(screen, screen.Bounds())
	startX, endX := 0, width
	startY, endY := 0, height
	if bordered {
		startX, endX = 2, width-2
		startY, endY = 1, height-1
	}
	for y := startY; y < endY; y++ {
		for x := startX; x < endX; x++ {
			cell := screen.CellAt(x, y)
			if cell != nil && cell.Style.Bg == nil {
				cell.Style.Bg = background
			}
		}
	}
	lines := make([]string, height)
	for y := range height {
		lines[y] = screen.Line(y).Render()
	}
	return strings.Join(lines, "\n")
}

func assertStyledOverlayFramesEqual(t *testing.T, got string, want string, width int, height int) {
	t.Helper()
	gotScreen := uv.NewScreenBuffer(width, height)
	gotScreen.Method = ansi.GraphemeWidth
	uv.NewStyledString(got).Draw(gotScreen, gotScreen.Bounds())
	wantScreen := uv.NewScreenBuffer(width, height)
	wantScreen.Method = ansi.GraphemeWidth
	uv.NewStyledString(want).Draw(wantScreen, wantScreen.Bounds())
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			gotCell := gotScreen.CellAt(x, y)
			wantCell := wantScreen.CellAt(x, y)
			if gotCell == nil || !gotCell.Equal(wantCell) {
				t.Fatalf("fixed-width overlay cell (%d,%d) = %#v, want %#v", x, y, gotCell, wantCell)
			}
		}
	}
}

func TestSubagentOutputOverlayLayoutRebuildsOnlyAfterResize(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 100
	model.height = 28
	view := model.ensureSubagentOutputView("spawn-1")
	for index := range 100 {
		view.observeChildEvent(TranscriptEvent{
			Kind:       TranscriptEventNotice,
			Scope:      ACPProjectionSubagent,
			ScopeID:    "reviewer",
			Text:       fmt.Sprintf("output row %03d", index),
			NoticeKind: "test",
		})
	}
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1", followTail: true}
	_ = model.renderSubagentOutputOverlay()
	initialLayout := model.subagentOutputOverlay.layout
	initialRenders := view.renderCache.renders

	for range 20 {
		model.scrollSubagentOutputOverlay(-1)
		_ = model.renderSubagentOutputOverlay()
	}
	if got := model.subagentOutputOverlay.layout; got != initialLayout {
		t.Fatalf("stable terminal size rebuilt overlay layout:\ngot  %#v\nwant %#v", got, initialLayout)
	}
	if got := view.renderCache.renders; got != initialRenders {
		t.Fatalf("stable terminal size rerendered transcript %d times, want %d", got, initialRenders)
	}

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 72, Height: 20})
	model = updated.(*Model)
	frame := model.renderSubagentOutputOverlay()
	resizedLayout := model.subagentOutputOverlay.layout
	if resizedLayout == initialLayout ||
		resizedLayout.termWidth != 72 ||
		resizedLayout.termHeight != 20 ||
		resizedLayout.useBorder {
		t.Fatalf("resize did not rebuild responsive borderless layout: %#v", resizedLayout)
	}
	if width := lipgloss.Width(frame); width > 72 {
		t.Fatalf("resized overlay width = %d, terminal width = 72", width)
	}
	if height := len(strings.Split(frame, "\n")); height > 20 {
		t.Fatalf("resized overlay height = %d, terminal height = 20", height)
	}
	if got := view.renderCache.renders; got != initialRenders+1 {
		t.Fatalf("resize transcript render count = %d, want %d", got, initialRenders+1)
	}

	model.scrollSubagentOutputOverlay(-1)
	_ = model.renderSubagentOutputOverlay()
	if got := model.subagentOutputOverlay.layout; got != resizedLayout {
		t.Fatalf("post-resize scroll rebuilt stable layout:\ngot  %#v\nwant %#v", got, resizedLayout)
	}
}

func legacySubagentOutputOverlayFrameForTest(model *Model) string {
	state := model.subagentOutputOverlay
	view := model.subagentOutputViews[state.callID]
	layout := model.subagentOutputLayout(state)
	rows := model.subagentOutputRows(view, layout.innerWidth, layout.contentRows)
	end := minInt(len(rows), state.offset+layout.contentRows)
	visible := rows[state.offset:end]
	body := make([]string, 0, layout.contentRows+4)
	body = append(body, model.renderSubagentOutputTitle(view, layout.innerWidth))
	body = append(body, model.theme.SeparatorStyle().Render(strings.Repeat("─", layout.innerWidth)))
	for index := 0; index < layout.contentRows; index++ {
		if index < len(visible) {
			body = append(body, visible[index].Styled)
		} else {
			body = append(body, "")
		}
	}
	body = append(body, model.theme.SeparatorStyle().Render(strings.Repeat("─", layout.innerWidth)))
	body = append(body, model.renderSubagentOutputFooter(state.offset, end, len(rows), layout.innerWidth))
	return tuikit.RenderResponsiveOverlayFrame(model.theme, tuikit.ResponsiveOverlayFrameModel{
		Body:      body,
		Width:     layout.frameWidth,
		UseBorder: layout.useBorder,
	})
}

var subagentOutputOverlayBenchmarkSink string

func BenchmarkSubagentOutputOverlayScroll(b *testing.B) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 180
	model.height = 60
	view := model.ensureSubagentOutputView("spawn-1")
	for index := range 800 {
		view.observeChildEvent(TranscriptEvent{
			Kind:       TranscriptEventNotice,
			Scope:      ACPProjectionSubagent,
			ScopeID:    "reviewer",
			Text:       fmt.Sprintf("步骤 %04d：读取并分析当前目录中的 Markdown 与工具输出", index),
			NoticeKind: "benchmark",
		})
	}
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1", followTail: true}
	_ = model.renderSubagentOutputOverlay()
	model.scrollSubagentOutputOverlay(-10)

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; b.Loop(); index++ {
		if index%2 == 0 {
			model.scrollSubagentOutputOverlay(-1)
		} else {
			model.scrollSubagentOutputOverlay(1)
		}
		subagentOutputOverlayBenchmarkSink = model.renderSubagentOutputOverlay()
	}
}

func subagentOutputOverlayPlain(model *Model) string {
	if model == nil {
		return ""
	}
	if model.subagentOutputOverlay != nil {
		if view := model.subagentOutputViews[model.subagentOutputOverlay.callID]; view != nil {
			view.prepareVisibleRender()
		}
	}
	return ansi.Strip(model.renderSubagentOutputOverlay())
}
