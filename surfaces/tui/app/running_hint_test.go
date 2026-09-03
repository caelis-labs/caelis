package tuiapp

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/charmbracelet/x/ansi"
)

func TestRunningHintShowsStableActivityElapsedAndPending(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	m.width = 100
	m.liveTurn.Active = true
	m.runningActivity = runningActivityState{
		Phase:     runningPhaseToolWait,
		Target:    runningTargetSubagent,
		Key:       "task:wait:orbit",
		StartedAt: time.Unix(100, 0),
	}
	m.pendingQueue = append(m.pendingQueue,
		pendingPrompt{state: pendingPromptQueued},
		pendingPrompt{state: pendingPromptDispatched},
	)

	got := ansi.Strip(m.buildRunningHintTextAt(time.Unix(112, 0)))
	if !strings.Contains(got, "Waiting on subagent · 12s · 2 pending") {
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
		Phase:     runningPhaseThinking,
		StartedAt: time.Unix(103, 0),
	}

	got := ansi.Strip(m.buildRunningHintTextAt(time.Unix(103, 0)))
	if got != "• Thinking · 0.1s" {
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

func TestRunningActivityElapsedUsesTenthsBeforeTenSeconds(t *testing.T) {
	t.Parallel()

	startedAt := time.Unix(100, 0)
	for _, test := range []struct {
		name    string
		elapsed time.Duration
		want    string
	}{
		{name: "starts moving", elapsed: 0, want: "0.1s"},
		{name: "sub tenth", elapsed: 99 * time.Millisecond, want: "0.1s"},
		{name: "tenths", elapsed: 1900 * time.Millisecond, want: "1.9s"},
		{name: "last tenth", elapsed: 9999 * time.Millisecond, want: "9.9s"},
		{name: "whole seconds", elapsed: 10999 * time.Millisecond, want: "10s"},
		{name: "minutes", elapsed: 61*time.Second + 900*time.Millisecond, want: "1m01s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := formatRunningActivityElapsed(startedAt.Add(test.elapsed), startedAt); got != test.want {
				t.Fatalf("formatRunningActivityElapsed(%s) = %q, want %q", test.elapsed, got, test.want)
			}
		})
	}
}

func TestFirstSessionEnvelopePreservesResponseClock(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	startedAt := time.Unix(100, 0)
	m.beginLiveTurn(SubmissionModeDefault, false, startedAt)

	m.observeTaskStreamSession(eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
	})

	if !m.runningActivity.StartedAt.Equal(startedAt) {
		t.Fatalf("first Session response clock = %v, want %v", m.runningActivity.StartedAt, startedAt)
	}
	if got := ansi.Strip(m.buildRunningHintTextAt(startedAt.Add(12 * time.Second))); got != "• Waiting for response · 12s" {
		t.Fatalf("first Session hint = %q, want advancing response clock", got)
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
	if !strings.Contains(frame, "Waiting for response · 0.1s · 1 pending") {
		t.Fatalf("frame = %q, want pending count appended to the running hint", frame)
	}
	if got := m.preComposerFixedHeight(); got != fixedHeight {
		t.Fatalf("preComposerFixedHeight() = %d, want %d without a pending drawer reservation", got, fixedHeight)
	}
}

func TestToolCompletionStartsFreshModelWaitClock(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	key := "tool:command-1"
	m.runningHintTracker.beginTurn(time.Unix(90, 0))
	m.runningHintTracker.start(key, runningPhaseToolWait, runningTargetShell, time.Unix(100, 0), "command-1")
	m.refreshRunningActivity()
	if got := ansi.Strip(m.buildRunningHintTextAt(time.Unix(112, 0))); got != "• Waiting on shell · 12s" {
		t.Fatalf("wait hint = %q", got)
	}

	completedAt := time.Unix(113, 0)
	m.runningHintTracker.complete(key, completedAt)
	m.refreshRunningActivity()
	if !m.runningActivity.StartedAt.Equal(completedAt) {
		t.Fatalf("model wait started at %v, want completion time %v", m.runningActivity.StartedAt, completedAt)
	}
	if got := ansi.Strip(m.buildRunningHintTextAt(completedAt.Add(900 * time.Millisecond))); got != "• Waiting for response · 0.9s" {
		t.Fatalf("waiting fallback = %q, want a fresh model-wait clock", got)
	}
}

func TestTaskWaitUsesGenericFallbackAndStableFinalKey(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	start := TranscriptEvent{
		Kind:           TranscriptEventTool,
		Scope:          ACPProjectionMain,
		ToolCallID:     "task-wait-1",
		ToolName:       "Task",
		ToolTaskAction: "wait",
		ToolTaskHandle: "command-48",
	}
	m.applyTranscriptRunningActivity(start)
	if m.runningActivity.Phase != runningPhaseToolWait || m.runningActivity.Target != runningTargetTask {
		t.Fatalf("runningActivity = %#v, want generic Wait task without typed target or parent", m.runningActivity)
	}
	if m.runningActivity.Key != "tool:g0:task-wait-1" {
		t.Fatalf("activity key = %q, want call-stable Task key", m.runningActivity.Key)
	}

	final := start
	final.Final = true
	final.ToolTaskTargetKind = "subagent"
	m.applyTranscriptRunningActivity(final)
	if m.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("runningActivity = %#v, want final update with refined target to complete the same activity", m.runningActivity)
	}
}

func TestSparseTaskWaitFinalClosesInvocation(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:               TranscriptEventTool,
		Scope:              ACPProjectionMain,
		TurnID:             "turn-1",
		ToolCallID:         "task-wait-1",
		ToolName:           "Task",
		ToolTaskAction:     "wait",
		ToolTaskTargetKind: "command",
	})
	if m.runningActivity.Phase != runningPhaseToolWait || m.runningActivity.Target != runningTargetShell {
		t.Fatalf("runningActivity = %#v, want active Task wait", m.runningActivity)
	}

	// Standard ACP permits a result update to omit the repeated tool name,
	// action, input, and metadata. The call identity still closes this wait.
	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		TurnID:     "turn-1",
		ToolCallID: "task-wait-1",
		Final:      true,
	})
	if m.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("runningActivity = %#v, want sparse Task final to close this wait invocation", m.runningActivity)
	}
}

func TestParticipantACPEventsDriveForegroundActivity(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
	apply := func(update eventstream.Update) {
		t.Helper()
		m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{
			Kind:          eventstream.KindSessionUpdate,
			SessionID:     "session-1",
			TurnID:        "participant-turn-1",
			Scope:         eventstream.ScopeParticipant,
			ScopeID:       "participant-turn-1",
			ParticipantID: "reviewer",
			Actor:         "@reviewer",
			Update:        update,
		})
	}

	apply(eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateAgentThought,
		Content:       eventstream.TextContent{Type: "text", Text: "inspecting the change"},
	})
	if m.runningActivity.Phase != runningPhaseThinking {
		t.Fatalf("participant thought activity = %#v, want Thinking", m.runningActivity)
	}

	apply(eventstream.ToolCall{
		SessionUpdate: eventstream.UpdateToolCall,
		ToolCallID:    "review-command-1",
		Title:         "go test ./...",
		Kind:          eventstream.ToolKindExecute,
		Status:        eventstream.ToolStatusInProgress,
		RawInput:      map[string]any{"command": "go test ./..."},
	})
	if m.runningActivity.Phase != runningPhaseToolWait || m.runningActivity.Target != runningTargetShell {
		t.Fatalf("participant tool activity = %#v, want Waiting on shell", m.runningActivity)
	}

	completed := eventstream.ToolStatusCompleted
	apply(eventstream.ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo,
		ToolCallID:    "review-command-1",
		Status:        &completed,
	})
	if m.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("participant tool final activity = %#v, want completed invocation closed", m.runningActivity)
	}

	apply(eventstream.ContentChunk{
		SessionUpdate: eventstream.UpdateAgentMessage,
		Content:       eventstream.TextContent{Type: "text", Text: "review complete"},
	})
	if m.runningActivity.Phase != runningPhaseResponding {
		t.Fatalf("participant response activity = %#v, want Responding", m.runningActivity)
	}
}

func TestStandardACPKindPrecedesTerminalMetadataInHint(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	m.runningHintTracker.beginTurn(time.Unix(100, 0))
	m.refreshRunningActivity()
	before := m.runningActivity

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind: TranscriptEventTool, Scope: ACPProjectionParticipant,
		ToolCallID: "read-with-terminal-meta", ToolKind: eventstream.ToolKindRead, ToolTerminal: true,
	})
	if m.runningActivity != before {
		t.Fatalf("read activity = %#v, want standard kind to preserve %#v", m.runningActivity, before)
	}

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind: TranscriptEventTool, Scope: ACPProjectionParticipant,
		ToolCallID: "search-1", ToolKind: eventstream.ToolKindSearch,
	})
	if m.runningActivity.Phase != runningPhaseSearch {
		t.Fatalf("search activity = %#v, want standard search phase", m.runningActivity)
	}
	if got := m.runningActivity.label(); got != "Searching" {
		t.Fatalf("search activity label = %q, want generic search without a web claim", got)
	}
	search := m.runningActivity
	search.StartedAt = time.Unix(101, 0)
	m.runningHintTracker.active[search.Key] = search
	m.refreshRunningActivity()
	if got := ansi.Strip(m.buildRunningHintTextAt(time.Unix(110, 0))); got != "• Searching · 9.0s" {
		t.Fatalf("running hint = %q, want generic search action without a web claim", got)
	}
}

func TestStandardACPWaitUsesTaskWaitHintSemantics(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	m.runningHintTracker.beginTurn(time.Unix(100, 0))
	m.refreshRunningActivity()
	wait := TranscriptEvent{
		Kind: TranscriptEventTool, Scope: ACPProjectionParticipant,
		ToolCallID: "codex-wait-1", ToolKind: eventstream.ToolKindOther, ToolTitle: "wait",
		ToolTaskAction: "wait", ToolTaskTargetKind: "subagent",
	}
	m.applyTranscriptRunningActivity(wait)
	if m.runningActivity.Phase != runningPhaseToolWait || m.runningActivity.Target != runningTargetSubagent {
		t.Fatalf("wait activity = %#v, want Task-wait subagent semantics", m.runningActivity)
	}
	wait.Final = true
	m.applyTranscriptRunningActivity(wait)
	if m.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("completed wait activity = %#v, want model waiting", m.runningActivity)
	}
}

func TestCodexWaitSourcesRemainDistinct(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		event      TranscriptEvent
		wantPhase  runningActivityPhase
		wantTarget runningActivityTarget
		wantHidden bool
	}{
		{
			name: "asynchronous shell wait",
			event: TranscriptEvent{
				Kind: TranscriptEventTool, Scope: ACPProjectionParticipant,
				ToolCallID: "shell-wait-1", ToolKind: eventstream.ToolKindExecute,
				ToolTitle: "wait", ToolTerminal: true,
			},
			wantPhase: runningPhaseToolWait, wantTarget: runningTargetShell,
		},
		{
			name: "collaboration wait",
			event: TranscriptEvent{
				Kind: TranscriptEventTool, Scope: ACPProjectionParticipant,
				ToolCallID: "collab-wait-1", ToolKind: eventstream.ToolKindOther,
				ToolTitle: "wait", ToolTaskAction: "wait", ToolTaskTargetKind: "subagent",
			},
			wantPhase: runningPhaseToolWait, wantTarget: runningTargetSubagent, wantHidden: true,
		},
		{
			name: "provider sleep",
			event: TranscriptEvent{
				Kind: TranscriptEventTool, Scope: ACPProjectionParticipant,
				ToolCallID: "sleep-1", ToolKind: eventstream.ToolKindOther, ToolTitle: "Wait",
			},
			wantPhase: runningPhaseModelWait,
		},
		{
			name: "untyped dynamic wait",
			event: TranscriptEvent{
				Kind: TranscriptEventTool, Scope: ACPProjectionParticipant,
				ToolCallID: "dynamic-wait-1", ToolKind: eventstream.ToolKindOther,
				ToolTitle: "wait", ToolTaskAction: "wait",
			},
			wantPhase: runningPhaseModelWait,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.liveTurn.Active = true
			m.runningHintTracker.beginTurn(time.Unix(100, 0))
			m.refreshRunningActivity()
			m.applyTranscriptRunningActivity(test.event)
			if m.runningActivity.Phase != test.wantPhase || m.runningActivity.Target != test.wantTarget {
				t.Fatalf("running activity = %#v, want phase %q target %q", m.runningActivity, test.wantPhase, test.wantTarget)
			}
			_, hidden := hiddenTaskControlAction(test.event)
			if hidden != test.wantHidden {
				t.Fatalf("hiddenTaskControlAction() hidden = %v, want %v", hidden, test.wantHidden)
			}
		})
	}
}

func TestTaskWriteDoesNotReplaceModelWaitingActivity(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	m.runningHintTracker.beginTurn(time.Unix(100, 0))
	m.refreshRunningActivity()
	start := TranscriptEvent{
		Kind:           TranscriptEventTool,
		Scope:          ACPProjectionMain,
		ToolCallID:     "task-write-1",
		ToolName:       "Task",
		ToolTaskAction: "write",
	}
	m.applyTranscriptRunningActivity(start)
	if m.runningActivity.Phase != runningPhaseModelWait || m.runningActivity.Key != "model" {
		t.Fatalf("runningActivity = %#v, want Task write to preserve model waiting", m.runningActivity)
	}

	start.Final = true
	m.applyTranscriptRunningActivity(start)
	if m.runningActivity.Phase != runningPhaseModelWait || m.runningActivity.Key != "model" {
		t.Fatalf("runningActivity = %#v, want Task final to preserve model waiting", m.runningActivity)
	}
}

func TestTaskReadAndWriteDoNotReplaceModelWaitingActivity(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"read", "write"} {
		t.Run(action, func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.liveTurn.Active = true
			m.runningHintTracker.beginTurn(time.Unix(100, 0))
			m.refreshRunningActivity()
			m.applyTranscriptRunningActivity(TranscriptEvent{
				Kind:               TranscriptEventTool,
				Scope:              ACPProjectionMain,
				ToolCallID:         "task-" + action,
				ToolName:           "Task",
				ToolTaskAction:     action,
				ToolTaskTargetKind: "command",
				ToolTaskHandle:     "command-48",
			})
			if m.runningActivity.Phase != runningPhaseModelWait || m.runningActivity.Key != "model" {
				t.Fatalf("runningActivity = %#v, want Task %s to preserve model waiting", m.runningActivity, action)
			}
		})
	}
}

func TestShortFileToolsDoNotReplaceCurrentACPActivity(t *testing.T) {
	t.Parallel()

	for _, toolName := range []string{"Read", "Write", "Patch", "Glob", "GREP"} {
		t.Run(toolName, func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.liveTurn.Active = true
			m.runningHintTracker.beginTurn(time.Unix(100, 0))
			m.refreshRunningActivity()
			before := m.runningActivity

			m.applyTranscriptRunningActivity(TranscriptEvent{
				Kind:       TranscriptEventTool,
				Scope:      ACPProjectionMain,
				ToolCallID: "short-tool-1",
				ToolName:   toolName,
			})
			if m.runningActivity != before {
				t.Fatalf("runningActivity = %#v, want %s to preserve %#v", m.runningActivity, toolName, before)
			}
		})
	}
}

func TestLongRunningWebToolsUseDistinctWebActivity(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		toolName string
		phase    runningActivityPhase
		label    string
	}{
		{toolName: "WebSearch", phase: runningPhaseWebSearch, label: "Searching web"},
		{toolName: "WebFetch", phase: runningPhaseFetch, label: "Fetching web"},
	} {
		t.Run(test.toolName, func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.liveTurn.Active = true
			m.applyTranscriptRunningActivity(TranscriptEvent{
				Kind:       TranscriptEventTool,
				Scope:      ACPProjectionMain,
				ToolCallID: "web-tool-1",
				ToolName:   test.toolName,
			})
			if m.runningActivity.Phase != test.phase || m.runningActivity.StartedAt.IsZero() {
				t.Fatalf("runningActivity = %#v, want timed web activity for %s", m.runningActivity, test.toolName)
			}
			if got := m.runningActivity.label(); got != test.label {
				t.Fatalf("runningActivity label = %q, want %q", got, test.label)
			}
		})
	}
}

func TestAttemptResetShowsRetryingWithoutDroppingActiveToolOwner(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	m.runningHintTracker.beginTurn(time.Unix(100, 0))
	commandKey := "tool:turn-1:command-1"
	m.runningHintTracker.start(commandKey, runningPhaseToolWait, runningTargetShell, time.Unix(101, 0), "command-1")
	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:  TranscriptEventLifecycle,
		Scope: ACPProjectionMain,
		State: "attempt_reset",
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"attempt_reset": map[string]any{"retrying": true},
				},
			},
		},
	})
	if m.runningActivity.Phase != runningPhaseRetrying || m.runningActivity.StartedAt.IsZero() {
		t.Fatalf("runningActivity = %#v, want timed retry activity", m.runningActivity)
	}
	if _, active := m.runningHintTracker.active[commandKey]; !active {
		t.Fatalf("active activities = %#v, want real shell owner preserved across model retry", m.runningHintTracker.active)
	}

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:          TranscriptEventNarrative,
		NarrativeKind: TranscriptNarrativeReasoning,
		Scope:         ACPProjectionMain,
		MessageID:     "reasoning-after-retry",
	})
	if m.runningActivity.Phase != runningPhaseThinking {
		t.Fatalf("runningActivity = %#v, want reasoning to replace retry activity", m.runningActivity)
	}
	if _, active := m.runningHintTracker.active[commandKey]; !active {
		t.Fatalf("active activities = %#v, want background shell owner to remain observable", m.runningHintTracker.active)
	}
}

func TestTerminalLifecycleClearsRetryingHint(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	m.runningHintTracker.beginTurn(time.Unix(100, 0))
	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:  TranscriptEventLifecycle,
		Scope: ACPProjectionMain,
		State: "attempt_reset",
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"attempt_reset": map[string]any{"retrying": true},
				},
			},
		},
	})
	if m.runningActivity.Phase != runningPhaseRetrying {
		t.Fatalf("runningActivity = %#v, want retrying before terminal lifecycle", m.runningActivity)
	}

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:  TranscriptEventLifecycle,
		Scope: ACPProjectionMain,
		State: "failed",
	})
	if m.runningActivity.Phase == runningPhaseRetrying {
		t.Fatalf("runningActivity = %#v, want terminal lifecycle to clear retrying hint", m.runningActivity)
	}
}

func TestAttemptResetWithoutRetryingMetaKeepsCurrentActivity(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	m.runningHintTracker.beginTurn(time.Unix(100, 0))
	m.refreshRunningActivity()
	before := m.runningActivity
	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:  TranscriptEventLifecycle,
		Scope: ACPProjectionMain,
		State: "attempt_reset",
	})
	if m.runningActivity != before {
		t.Fatalf("runningActivity = %#v, want non-retrying reset to preserve %#v", m.runningActivity, before)
	}
}

func TestParallelToolCompletionRestoresRemainingActivity(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "command-1",
		ToolName:   "RunCommand",
	})
	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "spawn-1",
		ToolName:   "Spawn",
	})
	if m.runningActivity.Phase != runningPhaseToolWait || m.runningActivity.Target != runningTargetSubagent {
		t.Fatalf("runningActivity = %#v, want latest parallel Spawn activity", m.runningActivity)
	}

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "spawn-1",
		ToolName:   "Spawn",
		Final:      true,
	})
	if m.runningActivity.Phase != runningPhaseToolWait || m.runningActivity.Target != runningTargetShell ||
		m.runningActivity.Key != "tool:g0:command-1" {
		t.Fatalf("runningActivity = %#v, want remaining command activity after Spawn completes", m.runningActivity)
	}

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "command-1",
		ToolName:   "RunCommand",
		Final:      true,
	})
	if m.runningActivity.Phase != runningPhaseModelWait || m.runningActivity.Key != "model" {
		t.Fatalf("runningActivity = %#v, want model waiting after all parallel tools complete", m.runningActivity)
	}
}

func TestParallelWebSearchShowsElapsedAndRestoresRemainingSearch(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	firstStartedAt := time.Unix(100, 0)
	secondStartedAt := time.Unix(103, 0)

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		TurnID:     "turn-search",
		OccurredAt: firstStartedAt,
		ToolCallID: "search-1",
		ToolName:   "WebSearch",
	})
	// Production timestamps each independently keyed activity on receipt.
	// Pin both clocks here so the rendered elapsed values are deterministic.
	firstKey := "tool:turn-search:search-1"
	first := m.runningHintTracker.active[firstKey]
	first.StartedAt = firstStartedAt
	m.runningHintTracker.active[firstKey] = first

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		TurnID:     "turn-search",
		OccurredAt: secondStartedAt,
		ToolCallID: "search-2",
		ToolName:   "WebSearch",
	})
	secondKey := "tool:turn-search:search-2"
	second := m.runningHintTracker.active[secondKey]
	second.StartedAt = secondStartedAt
	m.runningHintTracker.active[secondKey] = second
	m.refreshRunningActivity()

	if got := ansi.Strip(m.buildRunningHintTextAt(time.Unix(112, 0))); got != "• Searching web · 9.0s" {
		t.Fatalf("running hint = %q, want latest search with its own elapsed time", got)
	}

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		TurnID:     "turn-search",
		OccurredAt: time.Unix(113, 0),
		ToolCallID: "search-2",
		ToolName:   "WebSearch",
		Final:      true,
	})
	if got := ansi.Strip(m.buildRunningHintTextAt(time.Unix(114, 0))); got != "• Searching web · 14s" {
		t.Fatalf("running hint = %q, want first search restored after parallel completion", got)
	}

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		TurnID:     "turn-search",
		OccurredAt: time.Unix(115, 0),
		ToolCallID: "search-1",
		ToolName:   "WebSearch",
		Final:      true,
	})
	if m.runningActivity.Phase != runningPhaseModelWait || m.runningActivity.Key != "model" {
		t.Fatalf("runningActivity = %#v, want model waiting after all searches complete", m.runningActivity)
	}
}

func TestNarrativeForegroundOverridesRunningBackgroundTool(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	command := TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "command-1",
		ToolName:   "RunCommand",
	}
	m.applyTranscriptRunningActivity(command)
	if m.runningActivity.Phase != runningPhaseToolWait || m.runningActivity.Target != runningTargetShell {
		t.Fatalf("runningActivity = %#v, want foreground command", m.runningActivity)
	}

	m.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:          TranscriptEventNarrative,
		NarrativeKind: TranscriptNarrativeAssistant,
		Scope:         ACPProjectionMain,
		MessageID:     "response-1",
	})
	if m.runningActivity.Phase != runningPhaseResponding {
		t.Fatalf("runningActivity = %#v, want assistant narrative foreground", m.runningActivity)
	}

	// A late progress update refreshes the existing owner but must not make a
	// background command replace an answer already being produced.
	m.applyTranscriptRunningActivity(command)
	if m.runningActivity.Phase != runningPhaseResponding {
		t.Fatalf("runningActivity = %#v, want late command update to remain background", m.runningActivity)
	}

	spawn := TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "spawn-1",
		ToolName:   "Spawn",
	}
	m.applyTranscriptRunningActivity(spawn)
	if m.runningActivity.Phase != runningPhaseToolWait || m.runningActivity.Target != runningTargetSubagent {
		t.Fatalf("runningActivity = %#v, want newly started Spawn foreground", m.runningActivity)
	}
	spawn.Final = true
	m.applyTranscriptRunningActivity(spawn)
	if m.runningActivity.Phase != runningPhaseToolWait || m.runningActivity.Target != runningTargetShell {
		t.Fatalf("runningActivity = %#v, want active background command after foreground Spawn completes", m.runningActivity)
	}
}

func TestObservedTerminalCommandClosesExactRunningActivityOwner(t *testing.T) {
	t.Parallel()

	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true
	key := "tool:turn-1:command-call"
	m.runningHintTracker.start(key, runningPhaseToolWait, runningTargetShell, time.Unix(1, 0), "command-call")
	m.runningHintTracker.observeOwner("command-3", runningActivityOwner{
		Key:     key,
		CallID:  "command-call",
		BlockID: "block-1",
		Target:  runningTargetShell,
	})
	m.refreshRunningActivity()

	m.applyObservedCommandResults([]taskstream.CommandTaskResult{{
		ParentCallID: "different-command",
		Handle:       "command-3",
	}})
	if _, active := m.runningHintTracker.active[key]; !active {
		t.Fatal("conflicting command observation closed the owner")
	}

	m.applyObservedCommandResults([]taskstream.CommandTaskResult{{
		ParentCallID: "command-call",
		Handle:       "command-3",
	}})
	if _, active := m.runningHintTracker.active[key]; active {
		t.Fatalf("active activities = %#v, want exact terminal command owner closed", m.runningHintTracker.active)
	}
	if m.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("runningActivity = %#v, want model waiting after terminal command", m.runningActivity)
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
		ToolName:   "RunCommand",
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
		ToolName:   "RunCommand",
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
		ToolName:   "RunCommand",
	})

	updated, _ := m.requestRunningInterrupt()
	m = updated.(*Model)
	m.applyACPRunningActivity(eventstream.Envelope{}, []TranscriptEvent{{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "command-1",
		ToolName:   "RunCommand",
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
	if len(next.runningHintTracker.active) != 0 {
		t.Fatalf("active activities = %#v, want completed command removed while interrupt was pending", next.runningHintTracker.active)
	}
}

func TestAcceptedInterruptClearsAfterCancelledLifecycle(t *testing.T) {
	m := NewModel(Config{
		NoColor:     true,
		NoAnimation: true,
		CancelRunning: func() bool {
			return true
		},
	})
	m.liveTurn.Active = true
	m.runningHintTracker.beginTurn(time.Now())
	m.refreshRunningActivity()

	updated, cmd := m.requestRunningInterrupt()
	next := updated.(*Model)
	if cmd == nil {
		t.Fatal("requestRunningInterrupt() cmd = nil")
	}
	updated, _ = next.handleRunningInterruptResultMsg(RunningInterruptResultMsg{Accepted: true})
	next = updated.(*Model)
	if next.runningActivity.Phase != runningPhaseInterrupt || !next.runningInterruptRequested {
		t.Fatalf("after accepted interrupt: activity=%#v requested=%v", next.runningActivity, next.runningInterruptRequested)
	}

	_, ok := next.finishLiveTurnFromEnvelope(eventstream.TurnCancelled("h", "r", "t", "tui interrupt", time.Now()))
	if !ok {
		t.Fatal("finishLiveTurnFromEnvelope() = false, want cancelled lifecycle to end the live turn")
	}
	if next.turnRunning() {
		t.Fatal("live turn still active after cancelled lifecycle")
	}
	if next.runningInterruptRequested {
		t.Fatal("runningInterruptRequested still set after cancelled lifecycle")
	}
	if activity, _ := next.runningActivityText(); activity == "Interrupting" {
		t.Fatalf("running activity = %q after terminal, want interrupt overlay cleared", activity)
	}
}
