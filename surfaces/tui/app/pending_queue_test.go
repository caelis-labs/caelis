package tuiapp

import (
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestContinueRunningDoesNotRenderMidTurnUserLine(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.pendingQueue = append(model.pendingQueue, pendingPrompt{
		execLine:    "steer after this step",
		displayLine: "steer after this step",
		state:       pendingPromptAwaitingActiveDisplay,
	})

	next, _ := model.handleTaskResultMsg(TaskResultMsg{ContinueRunning: true})
	model = next.(*Model)
	if got := countUserNarrativeBlocksForTest(model, "steer after this step"); got != 0 {
		t.Fatalf("user prompt blocks after ContinueRunning = %d, want 0 until insertion echo", got)
	}
	if len(model.pendingQueue) != 1 || !model.pendingQueue[0].awaitsAcceptedActiveDisplay() {
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
		state:       pendingPromptAwaitingActiveDisplay,
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

func TestPendingQueueAbortDropsAwaitingActiveDisplay(t *testing.T) {
	t.Parallel()

	queue := pendingPromptQueue{
		{execLine: "queued later", displayLine: "queued later", state: pendingPromptQueued},
		{execLine: "steer now", displayLine: "steer now", state: pendingPromptAwaitingActiveDisplay},
		{execLine: "already shown", displayLine: "already shown", state: pendingPromptRendered},
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
				state:       pendingPromptAwaitingActiveDisplay,
			})

			next, _ := model.handleTaskResultMsg(TaskResultMsg{ContinueRunning: true})
			model = next.(*Model)
			if !model.pendingQueue[0].awaitsAcceptedActiveDisplay() {
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
		state:       pendingPromptAwaitingActiveDisplay,
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
