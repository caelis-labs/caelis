package tuiapp

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/charmbracelet/x/ansi"
)

func TestRunningHintShowsStableActivityElapsedAndPending(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	m.width = 100
	m.liveTurn.Active = true
	m.runningActivity = runningActivityState{
		Phase:     runningPhaseWait,
		Target:    runningTargetSubagent,
		Key:       "task:wait:orbit",
		StartedAt: time.Unix(100, 0),
	}
	m.pendingQueue = append(m.pendingQueue,
		pendingPrompt{state: pendingPromptQueued},
		pendingPrompt{state: pendingPromptDispatched},
	)

	got := ansi.Strip(m.buildRunningHintTextAt(time.Unix(112, 0)))
	if !strings.Contains(got, "Wait subagent · 12s · 2 pending") {
		t.Fatalf("running hint = %q, want stable activity, elapsed time, and pending suffix", got)
	}
	if strings.Contains(got, "Esc") {
		t.Fatalf("running hint = %q, want no interrupt affordance", got)
	}
}

func TestNoAnimationUsesStaticMarkerAndDoesNotScheduleSpinner(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	m.runningActivity = runningActivityState{
		Phase: runningPhaseThinking,
	}

	got := ansi.Strip(m.buildRunningHintTextAt(time.Unix(103, 0)))
	if got != "• Thinking" {
		t.Fatalf("running hint = %q, want reduced-motion marker", got)
	}
	if cmd := m.scheduleSpinnerTick(); cmd != nil {
		t.Fatal("scheduleSpinnerTick() returned a command with animation disabled")
	}
	updated, cmd := m.Update(m.spinner.Tick())
	if cmd != nil {
		t.Fatal("spinner tick rescheduled with animation disabled")
	}
	if next := updated.(*Model); next.spinnerTickScheduled {
		t.Fatal("spinner tick remained scheduled with animation disabled")
	}
}

func TestPendingPromptIsOnlySummarizedInRunningHint(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(*Model)
	m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	fixedHeight := m.preComposerFixedHeight()
	m.pendingQueue = append(m.pendingQueue, pendingPrompt{
		execLine:    "do not preview this queued prompt",
		displayLine: "do not preview this queued prompt",
		state:       pendingPromptQueued,
	})

	frame := ansi.Strip(m.View().Content)
	if strings.Contains(frame, "do not preview this queued prompt") {
		t.Fatalf("frame exposed the pending prompt body:\n%s", frame)
	}
	if !strings.Contains(frame, "Thinking · 1 pending") {
		t.Fatalf("frame = %q, want pending count appended to the running hint", frame)
	}
	if got := m.preComposerFixedHeight(); got != fixedHeight {
		t.Fatalf("preComposerFixedHeight() = %d, want %d without a pending drawer reservation", got, fixedHeight)
	}
}

func TestThinkingFallbackDoesNotResetAVisibleClock(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	m.runningActivity = runningActivityState{
		Phase:     runningPhaseWait,
		Target:    runningTargetShell,
		Key:       "tool:command-1",
		StartedAt: time.Unix(100, 0),
	}
	if got := ansi.Strip(m.buildRunningHintTextAt(time.Unix(112, 0))); got != "• Wait shell · 12s" {
		t.Fatalf("wait hint = %q", got)
	}

	m.completeRunningActivity("tool:command-1")
	if got := ansi.Strip(m.buildRunningHintTextAt(time.Unix(113, 0))); got != "• Thinking" {
		t.Fatalf("thinking fallback = %q, want no reset elapsed clock", got)
	}
}

func TestTaskActivityUsesGenericFallbackAndStableFinalKey(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	start := TranscriptEvent{
		Kind:           TranscriptEventTool,
		Scope:          ACPProjectionMain,
		ToolCallID:     "task-wait-1",
		ToolName:       "TASK",
		ToolTaskAction: "wait",
		ToolTaskHandle: "command-48",
	}
	m.applyTranscriptRunningActivity(start)
	if m.runningActivity.Phase != runningPhaseWait || m.runningActivity.Target != runningTargetTask {
		t.Fatalf("runningActivity = %#v, want generic Wait task without typed target or parent", m.runningActivity)
	}
	if m.runningActivity.Key != "tool:g0:task-wait-1" {
		t.Fatalf("activity key = %q, want call-stable Task key", m.runningActivity.Key)
	}

	final := start
	final.Final = true
	final.ToolTaskTargetKind = "subagent"
	m.applyTranscriptRunningActivity(final)
	if m.runningActivity.Phase != runningPhaseThinking {
		t.Fatalf("runningActivity = %#v, want final update with refined target to complete the same activity", m.runningActivity)
	}
}

func TestNonControlTaskActivityHasMatchingFinal(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	start := TranscriptEvent{
		Kind:           TranscriptEventTool,
		Scope:          ACPProjectionMain,
		ToolCallID:     "task-write-1",
		ToolName:       "TASK",
		ToolTaskAction: "write",
	}
	m.applyTranscriptRunningActivity(start)
	if m.runningActivity.Phase != runningPhaseThinking || m.runningActivity.Key != "tool:g0:task-write-1" {
		t.Fatalf("runningActivity = %#v, want keyed thinking activity", m.runningActivity)
	}

	start.Final = true
	m.applyTranscriptRunningActivity(start)
	if m.runningActivity.Phase != runningPhaseThinking || m.runningActivity.Key != "" {
		t.Fatalf("runningActivity = %#v, want Task final to clear its keyed activity", m.runningActivity)
	}
}

func TestTaskReadAndCommandWriteUseSemanticShellActivity(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		action string
		phase  runningActivityPhase
		label  string
	}{
		{action: "read", phase: runningPhaseRead, label: "Read shell"},
		{action: "write", phase: runningPhaseWait, label: "Wait shell"},
	} {
		t.Run(test.action, func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.liveTurn.Active = true
			m.applyTranscriptRunningActivity(TranscriptEvent{
				Kind:               TranscriptEventTool,
				Scope:              ACPProjectionMain,
				ToolCallID:         "task-" + test.action,
				ToolName:           "TASK",
				ToolTaskAction:     test.action,
				ToolTaskTargetKind: "command",
				ToolTaskHandle:     "command-48",
			})
			if m.runningActivity.Phase != test.phase ||
				m.runningActivity.Target != runningTargetShell {
				t.Fatalf("runningActivity = %#v, want %s for Task %s", m.runningActivity, test.label, test.action)
			}
			if got := m.runningActivity.label(); got != test.label {
				t.Fatalf("runningActivity label = %q, want %q", got, test.label)
			}
		})
	}
}

func TestTaskWriteToSubagentRemainsWorkActivity(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:               TranscriptEventTool,
		Scope:              ACPProjectionMain,
		ToolCallID:         "task-write",
		ToolName:           "TASK",
		ToolTaskAction:     "write",
		ToolTaskTargetKind: "subagent",
		ToolTaskHandle:     "orbit",
	})
	if m.runningActivity.Phase != runningPhaseThinking || m.runningActivity.Target != "" {
		t.Fatalf("runningActivity = %#v, want Thinking for subagent continuation", m.runningActivity)
	}
}

func TestParallelToolCompletionRestoresRemainingActivity(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "command-1",
		ToolName:   "RUN_COMMAND",
	})
	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "spawn-1",
		ToolName:   "SPAWN",
	})
	if m.runningActivity.Phase != runningPhaseWait || m.runningActivity.Target != runningTargetSubagent {
		t.Fatalf("runningActivity = %#v, want latest parallel Spawn activity", m.runningActivity)
	}

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "spawn-1",
		ToolName:   "SPAWN",
		Final:      true,
	})
	if m.runningActivity.Phase != runningPhaseWait || m.runningActivity.Target != runningTargetShell ||
		m.runningActivity.Key != "tool:g0:command-1" {
		t.Fatalf("runningActivity = %#v, want remaining command activity after Spawn completes", m.runningActivity)
	}

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "command-1",
		ToolName:   "RUN_COMMAND",
		Final:      true,
	})
	if m.runningActivity.Phase != runningPhaseThinking || m.runningActivity.Key != "" {
		t.Fatalf("runningActivity = %#v, want thinking after all parallel tools complete", m.runningActivity)
	}
}

func TestRunningActivityAllowsReusedCallIDInANewTurnWithoutRevivingOldOwner(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
	first := TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		TurnID:     "turn-1",
		OccurredAt: time.Unix(101, 0),
		ToolCallID: "command-1",
		ToolName:   "RUN_COMMAND",
	}
	m.applyTranscriptRunningActivity(first)
	first.Final = true
	first.OccurredAt = time.Unix(102, 0)
	m.applyTranscriptRunningActivity(first)
	m.stopLiveTurn()

	m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(200, 0))
	second := first
	second.TurnID = "turn-2"
	second.OccurredAt = time.Unix(201, 0)
	second.Final = false
	m.applyTranscriptRunningActivity(second)
	if m.runningActivity.Key != "tool:turn-2:command-1" {
		t.Fatalf("runningActivity = %#v, want reused call ID owned by turn 2", m.runningActivity)
	}

	first.Final = false
	first.OccurredAt = time.Unix(103, 0)
	m.applyTranscriptRunningActivity(first)
	if m.runningActivity.Key != "tool:turn-2:command-1" {
		t.Fatalf("runningActivity = %#v, want late turn-1 update unable to revive completed owner", m.runningActivity)
	}
}

func TestRunningActivityMissingTurnIDUsesTurnGenerationAndRejectsOlderEvent(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
	event := TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		OccurredAt: time.Unix(101, 0),
		ToolCallID: "command-1",
		ToolName:   "RUN_COMMAND",
	}
	m.applyTranscriptRunningActivity(event)
	event.Final = true
	m.applyTranscriptRunningActivity(event)
	m.stopLiveTurn()

	m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(200, 0))
	event.Final = false
	event.OccurredAt = time.Unix(201, 0)
	m.applyTranscriptRunningActivity(event)
	if m.runningActivity.Key != "tool:g2:command-1" {
		t.Fatalf("runningActivity = %#v, want generation-scoped compatibility key", m.runningActivity)
	}
	event.OccurredAt = time.Unix(150, 0)
	m.applyTranscriptRunningActivity(event)
	if m.runningActivity.Key != "tool:g2:command-1" {
		t.Fatalf("runningActivity = %#v, want old compatibility event ignored", m.runningActivity)
	}
}

func TestRunningHintPlainRowDoesNotExposeANSI(t *testing.T) {
	m := NewModel(Config{})
	m.beginLiveTurn(SubmissionModeDefault, false, time.Now())

	plain := m.hintRowText()
	if plain != ansi.Strip(plain) || strings.Contains(plain, "[38;") {
		t.Fatalf("hintRowText() = %q, want plain clipboard-safe text", plain)
	}
}

func TestSpinnerTickReschedulesWhileRunning(t *testing.T) {
	m := NewModel(Config{})
	m.liveTurn.Active = true
	m.spinnerTickScheduled = true

	updated, cmd := m.Update(m.spinner.Tick())
	next := updated.(*Model)
	if cmd == nil {
		t.Fatal("running spinner tick should keep scheduling future ticks")
	}
	if !next.spinnerTickScheduled {
		t.Fatal("spinnerTickScheduled = false, want true after running tick")
	}
}

func TestRunningSpinnerContinuesWhenViewportPinned(t *testing.T) {
	m := NewModel(Config{Workspace: "/tmp/storage"})
	m.liveTurn.Active = true
	m.viewportFollowState = viewportPinnedHistory
	m.spinnerTickScheduled = true

	before := m.windowTitle()
	updated, cmd := m.Update(m.spinner.Tick())
	next := updated.(*Model)
	after := next.windowTitle()

	if cmd == nil {
		t.Fatal("pinned viewport running tick should schedule the next tick")
	}
	if !next.spinnerTickScheduled {
		t.Fatal("spinnerTickScheduled = false, want true after pinned viewport tick")
	}
	if before == after {
		t.Fatalf("windowTitle did not advance while viewport was pinned: %q", before)
	}
	if !strings.Contains(after, "storage") {
		t.Fatalf("windowTitle() = %q, want workspace title", after)
	}
}

func TestResumeRunningAnimationIgnoresViewportPin(t *testing.T) {
	m := NewModel(Config{})
	m.liveTurn.Active = true
	m.viewportFollowState = viewportPinnedHistory

	if cmd := m.resumeRunningAnimationIfNeeded(); cmd == nil {
		t.Fatal("resumeRunningAnimationIfNeeded() = nil, want tick command while viewport is pinned")
	}
}

func TestInterruptReplacesApprovalReviewActivity(t *testing.T) {
	cancelled := false
	m := NewModel(Config{
		CancelRunning: func() bool {
			cancelled = true
			return true
		},
	})
	m.liveTurn.Active = true
	m.runningActivity = runningActivityState{
		Phase:     runningPhaseReview,
		Key:       "approval:call-1",
		StartedAt: time.Now(),
	}

	updated, cmd := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	next := updated.(*Model)
	if cmd == nil {
		t.Fatal("interrupt should return a cancel command")
	}
	if cancelled {
		t.Fatal("cancel command should not run synchronously")
	}
	if next.runningActivity.Phase != runningPhaseInterrupt {
		t.Fatalf("runningActivity = %#v, want interrupting", next.runningActivity)
	}
	activity, _ := next.runningActivityText()
	if activity != "Interrupting" {
		t.Fatalf("running activity = %q, want interrupting to replace approval review", activity)
	}
}

func TestRejectedInterruptRestoresActivityAdvancedWhilePending(t *testing.T) {
	m := NewModel(Config{
		NoColor:     true,
		NoAnimation: true,
		CancelRunning: func() bool {
			return false
		},
	})
	m.liveTurn.Active = true
	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "command-1",
		ToolName:   "RUN_COMMAND",
	})

	updated, _ := m.requestRunningInterrupt()
	m = updated.(*Model)
	m.applyACPRunningActivity(eventstream.Envelope{}, []TranscriptEvent{{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "command-1",
		ToolName:   "RUN_COMMAND",
		Final:      true,
	}})
	m.applyACPRunningActivity(eventstream.Envelope{}, []TranscriptEvent{{
		Kind:          TranscriptEventNarrative,
		NarrativeKind: TranscriptNarrativeAssistant,
		Scope:         ACPProjectionMain,
		MessageID:     "response-1",
	}})
	if m.runningActivity.Phase != runningPhaseInterrupt {
		t.Fatalf("runningActivity = %#v, want interrupt overlay while request is pending", m.runningActivity)
	}

	updated, _ = m.handleRunningInterruptResultMsg(RunningInterruptResultMsg{Accepted: false})
	next := updated.(*Model)
	if next.runningActivity.Phase != runningPhaseResponding || next.runningActivity.Key != "response:response-1" {
		t.Fatalf("runningActivity = %#v, want latest response activity after rejected interrupt", next.runningActivity)
	}
	if len(next.runningActivityTracker.active) != 0 {
		t.Fatalf("active activities = %#v, want completed command removed while interrupt was pending", next.runningActivityTracker.active)
	}
}
