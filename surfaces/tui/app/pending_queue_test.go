package tuiapp

import (
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestContinueRunningDoesNotRenderMidTurnUserLine(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.pendingQueue = append(model.pendingQueue, pendingPrompt{
		execLine:    "steer after this step",
		displayLine: "steer after this step",
		state:       pendingPromptDispatched,
	})

	next, _ := model.handleTaskResultMsg(TaskResultMsg{ContinueRunning: true})
	model = next.(*Model)
	if got := countUserNarrativeBlocksForTest(model, "steer after this step"); got != 0 {
		t.Fatalf("user prompt blocks after ContinueRunning = %d, want 0 until insertion echo", got)
	}
	if len(model.pendingQueue) != 1 || model.pendingQueue[0].state != pendingPromptDispatched {
		t.Fatalf("pendingQueue = %#v, want awaiting insertion echo", model.pendingQueue)
	}
}

func TestPendingImageRendersAsOrdinaryUserMessageOnGatewayEcho(t *testing.T) {
	t.Parallel()

	const display = "inspect this [image #1]"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.pendingQueue = append(model.pendingQueue, pendingPrompt{
		execLine:    "inspect this",
		displayLine: display,
		attachments: []Attachment{{Name: "shot.png", Offset: len([]rune("inspect this"))}},
		state:       pendingPromptDispatched,
	})

	model = model.handleUserMessageMsg(UserMessageMsg{Text: display}).(*Model)
	if got := countUserNarrativeBlocksForTest(model, display); got != 1 {
		t.Fatalf("image user prompt blocks = %d, want one ordinary user message", got)
	}
	if len(model.pendingQueue) != 0 {
		t.Fatalf("pendingQueue after image user echo = %#v, want empty", model.pendingQueue)
	}
	if model.hint != "" {
		t.Fatalf("hint after image user echo = %q, want no scheduling notice", model.hint)
	}
}

func TestPendingQueueAbortDropsDispatched(t *testing.T) {
	t.Parallel()

	queue := pendingPromptQueue{
		{execLine: "queued later", displayLine: "queued later", state: pendingPromptQueued},
		{execLine: "steer now", displayLine: "steer now", state: pendingPromptDispatched},
	}

	next, hasNext := queue.onTurnEnd(false, true)
	if hasNext || next.displayText() != "" {
		t.Fatalf("onTurnEnd(abort) next = %#v/%v, want none", next, hasNext)
	}
	if len(queue) != 0 {
		t.Fatalf("pending queue after abort = %#v, want empty", queue)
	}
}

func TestActiveTurnPromptAbortWithoutEchoClearsPending(t *testing.T) {
	t.Parallel()

	const prompt = "steer after this step"
	cases := []struct {
		name string
		msg  TaskResultMsg
	}{
		{name: "cancelled", msg: TaskResultMsg{Interrupted: true}},
		{name: "failed", msg: TaskResultMsg{Err: errors.New("model step failed")}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := NewModel(Config{NoColor: true, NoAnimation: true})
			model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(130, 0))
			model.pendingQueue = append(model.pendingQueue, pendingPrompt{
				execLine:    prompt,
				displayLine: prompt,
				state:       pendingPromptDispatched,
			})

			next, _ := model.handleTaskResultMsg(TaskResultMsg{ContinueRunning: true})
			model = next.(*Model)
			if model.pendingQueue[0].state != pendingPromptDispatched {
				t.Fatalf("pendingQueue after ContinueRunning = %#v, want awaiting insertion echo", model.pendingQueue)
			}

			next, _ = model.handleTaskResultMsg(tc.msg)
			model = next.(*Model)
			if got := countUserNarrativeBlocksForTest(model, prompt); got != 0 {
				t.Fatalf("user prompt blocks after %s = %d, want 0 without insertion echo", tc.name, got)
			}
			if len(model.pendingQueue) != 0 {
				t.Fatalf("pendingQueue after %s = %#v, want abort to drop unconsumed mid-turn prompt", tc.name, model.pendingQueue)
			}
			if model.pendingQueue.visibleCount() != 0 {
				t.Fatalf("visible pending after %s = %d, want 0", tc.name, model.pendingQueue.visibleCount())
			}

			model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
				Kind:      eventstream.KindSessionUpdate,
				SessionID: "session-1",
				ScopeID:   "session-1",
				TurnID:    "turn-1",
				Update: eventstream.ContentChunk{
					SessionUpdate: eventstream.UpdateUserMessage,
					Content:       eventstream.TextContent{Type: "text", Text: prompt},
				},
			})
			if got := countUserNarrativeBlocksForTest(model, prompt); got != 1 {
				t.Fatalf("user prompt blocks after late echo = %d, want ordinary durable user line", got)
			}
		})
	}
}

func TestEscCancelledLifecycleClearsPendingWithoutEcho(t *testing.T) {
	t.Parallel()

	const prompt = "steer after this step"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(130, 0))
	model.pendingQueue = append(model.pendingQueue, pendingPrompt{
		execLine:    prompt,
		displayLine: prompt,
		state:       pendingPromptDispatched,
	})

	next, _ := model.handleTaskResultMsg(TaskResultMsg{ContinueRunning: true})
	model = next.(*Model)
	model = applyACPEnvelopeForTest(t, model, eventstream.TurnCancelled("handle-1", "run-1", "turn-1", "interrupted", time.Unix(131, 0)))
	if model.turnRunning() {
		t.Fatal("turn still running after cancelled lifecycle")
	}
	if got := countUserNarrativeBlocksForTest(model, prompt); got != 0 {
		t.Fatalf("user prompt blocks after Esc cancel = %d, want 0", got)
	}
	if len(model.pendingQueue) != 0 || model.pendingQueue.visibleCount() != 0 {
		t.Fatalf("pendingQueue after Esc cancel = %#v, want empty", model.pendingQueue)
	}
}

func TestCanonicalUserEventConsumesOnlyDispatchedPrompt(t *testing.T) {
	t.Parallel()
	const text = "same prompt"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.pendingQueue = pendingPromptQueue{
		{localID: 1, execLine: text, state: pendingPromptQueued},
		{localID: 2, execLine: text, state: pendingPromptDispatchScheduled},
		{localID: 3, execLine: text, state: pendingPromptDispatched},
		{localID: 4, execLine: text, state: pendingPromptDispatched},
	}
	event := TranscriptEvent{Kind: TranscriptEventNarrative, Scope: ACPProjectionMain,
		ScopeID: "session-1", TurnID: "turn-1", SourceEventID: "user-1",
		NarrativeKind: TranscriptNarrativeUser, Text: text, Final: true}
	model.Update(TranscriptEventsMsg{Events: []TranscriptEvent{event}})
	if len(model.pendingQueue) != 3 || model.pendingQueue[0].localID != 1 || model.pendingQueue[1].localID != 2 || model.pendingQueue[2].localID != 4 {
		t.Fatalf("pending after event = %#v, want only first dispatched entry removed", model.pendingQueue)
	}
	model.Update(TranscriptEventsMsg{Events: []TranscriptEvent{event}})
	if len(model.pendingQueue) != 3 {
		t.Fatalf("pending after retransmission = %#v, want no further consumption", model.pendingQueue)
	}
	if got := countUserNarrativeBlocksForTest(model, text); got != 1 {
		t.Fatalf("user blocks after retransmission = %d, want one", got)
	}
	event.SourceEventID = "user-2"
	model.Update(TranscriptEventsMsg{Events: []TranscriptEvent{event}})
	if len(model.pendingQueue) != 2 || model.pendingQueue[0].localID != 1 || model.pendingQueue[1].localID != 2 {
		t.Fatalf("pending after distinct event = %#v, want undispatched entries preserved", model.pendingQueue)
	}
	if got := countUserNarrativeBlocksForTest(model, text); got != 2 {
		t.Fatalf("user blocks after distinct event = %d, want two", got)
	}
}

func TestRejectedEchoRacePreservesUnsentIdenticalInputs(t *testing.T) {
	for _, outcome := range []appserver.Outcome{appserver.OutcomeRejected, appserver.OutcomeConflicted} {
		t.Run(string(outcome), func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			const text = "same input"
			m.pendingQueue = pendingPromptQueue{
				{localID: 1, execLine: text, state: pendingPromptQueued},
				{localID: 2, execLine: text, state: pendingPromptDispatchScheduled},
				{localID: 3, execLine: text, state: pendingPromptDispatched},
			}
			m.Update(UserMessageMsg{Text: text})
			m.handleFailedActiveSubmission(Submission{localID: 3, Text: text}, outcome)
			if len(m.pendingQueue) != 2 || m.pendingQueue[0].localID != 1 || m.pendingQueue[1].localID != 2 {
				t.Fatalf("failed submission consumed unsent input: %#v", m.pendingQueue)
			}
			if m.textarea.Value() != text {
				t.Fatalf("failed draft = %q, want %q", m.textarea.Value(), text)
			}
			if got := countUserNarrativeBlocksForTest(m, text); got != 1 {
				t.Fatalf("observed user messages = %d, want one", got)
			}
		})
	}
}
