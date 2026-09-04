package kernel

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

type testTurnEventRecorder struct {
	mu     sync.Mutex
	events []eventstream.Envelope
	next   chan eventstream.Envelope
}

var testTurnRecorders sync.Map

func newTestTurnEventRecorder() *testTurnEventRecorder {
	return &testTurnEventRecorder{next: make(chan eventstream.Envelope, 16384)}
}

func (r *testTurnEventRecorder) ObserveTurnEvent(_ context.Context, envelope eventstream.Envelope) error {
	if r == nil {
		return nil
	}
	envelope = eventstream.CloneEnvelope(envelope)
	r.mu.Lock()
	r.events = append(r.events, envelope)
	r.mu.Unlock()
	r.next <- envelope
	return nil
}

func (r *testTurnEventRecorder) snapshot() []eventstream.Envelope {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]eventstream.Envelope, len(r.events))
	for i := range r.events {
		out[i] = eventstream.CloneEnvelope(r.events[i])
	}
	return out
}

func (r *testTurnEventRecorder) waitNext(t *testing.T) eventstream.Envelope {
	t.Helper()
	select {
	case envelope := <-r.next:
		return envelope
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for observed Turn event")
		return eventstream.Envelope{}
	}
}

func registerTestTurnRecorder(handle *turnHandle, recorder *testTurnEventRecorder) {
	if handle != nil && recorder != nil {
		testTurnRecorders.Store(handle, recorder)
	}
}

func testObservedTurnEvents(handle *turnHandle) []eventstream.Envelope {
	if handle == nil {
		return nil
	}
	value, ok := testTurnRecorders.Load(handle)
	if !ok {
		return nil
	}
	return value.(*testTurnEventRecorder).snapshot()
}
