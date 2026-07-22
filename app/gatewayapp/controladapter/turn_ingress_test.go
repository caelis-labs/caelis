package controladapter

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestMainTurnIngressDoesNotFanInTaskOutput(t *testing.T) {
	mainEvents := make(chan eventstream.Envelope, 4)
	handle := newBrokerTestHandle(mainEvents)
	turn := newGatewayTurn(handle)

	mainEvents <- eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		Scope:     eventstream.ScopeMain,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "spawn-call-1",
			Title:         stringPointer("SPAWN helper"),
		},
	}
	mainEvents <- eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "task-1",
		SessionID: "session-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "must stay on task stream"},
		},
	}
	mainEvents <- eventstream.Envelope{
		Kind:      eventstream.KindNotice,
		Scope:     eventstream.ScopeMain,
		SessionID: "session-1",
		Notice:    "main continues",
	}
	close(mainEvents)

	events := collectAdapterTurnEvents(turn.Events())
	if len(events) != 3 {
		t.Fatalf("main events = %#v, want tool state, main notice, and terminal", events)
	}
	for _, event := range events {
		if event.Scope == eventstream.ScopeSubagent {
			t.Fatalf("Session feed contains child live output: %#v", event)
		}
	}
	if events[1].Notice != "main continues" || !eventstream.IsTurnTerminalLifecycle(events[2]) {
		t.Fatalf("main events = %#v, want main notice followed by terminal", events)
	}
}

func stringPointer(value string) *string { return &value }

func TestMainTurnIngressTerminalDoesNotWaitForTaskSource(t *testing.T) {
	mainEvents := make(chan eventstream.Envelope, 1)
	handle := newBrokerTestHandle(mainEvents)
	turn := newGatewayTurn(handle)
	mainEvents <- eventstream.TurnCompleted(handle.HandleID(), handle.RunID(), handle.TurnID(), time.Now())
	close(mainEvents)

	select {
	case terminal := <-turn.Events():
		if !eventstream.IsTurnTerminalLifecycle(terminal) {
			t.Fatalf("event = %#v, want terminal", terminal)
		}
	case <-time.After(time.Second):
		t.Fatal("parent terminal waited for a detached Task source")
	}
}

func TestMainTurnIngressDropsKnownForeignMainEvent(t *testing.T) {
	mainEvents := make(chan eventstream.Envelope, 2)
	handle := newBrokerTestHandle(mainEvents)
	mainEvents <- eventstream.Envelope{
		Kind:      eventstream.KindNotice,
		Scope:     eventstream.ScopeMain,
		SessionID: "session-1",
		HandleID:  "foreign",
		RunID:     handle.RunID(),
		TurnID:    handle.TurnID(),
		Notice:    "foreign",
	}
	close(mainEvents)

	events := collectAdapterTurnEvents(newGatewayTurn(handle).Events())
	if len(events) != 1 || !eventstream.IsTurnTerminalLifecycle(events[0]) {
		t.Fatalf("events = %#v, want only current Turn terminal", events)
	}
}

type brokerTestHandle struct {
	events        <-chan eventstream.Envelope
	handleID      string
	runID         string
	turnID        string
	eventsStarted chan struct{}
	eventsOnce    sync.Once
	cancelCalls   atomic.Int32
	closeCalls    atomic.Int32
	cancelFn      func()
	closeFn       func() error
}

func newBrokerTestHandle(events <-chan eventstream.Envelope) *brokerTestHandle {
	return &brokerTestHandle{events: events, handleID: "handle-1", runID: "run-1", turnID: "turn-1"}
}

func (h *brokerTestHandle) HandleID() string { return h.handleID }
func (h *brokerTestHandle) RunID() string    { return h.runID }
func (h *brokerTestHandle) TurnID() string   { return h.turnID }
func (*brokerTestHandle) SessionRef() session.SessionRef {
	return session.SessionRef{SessionID: "session-1"}
}
func (*brokerTestHandle) CreatedAt() time.Time { return time.Time{} }
func (h *brokerTestHandle) ACPEvents() <-chan eventstream.Envelope {
	if h.eventsStarted != nil {
		h.eventsOnce.Do(func() { close(h.eventsStarted) })
	}
	return h.events
}
func (*brokerTestHandle) Submit(context.Context, kernel.SubmitRequest) error { return nil }
func (h *brokerTestHandle) Cancel() agent.CancelResult {
	h.cancelCalls.Add(1)
	if h.cancelFn != nil {
		h.cancelFn()
	}
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (h *brokerTestHandle) Close() error {
	h.closeCalls.Add(1)
	if h.closeFn != nil {
		return h.closeFn()
	}
	return nil
}
