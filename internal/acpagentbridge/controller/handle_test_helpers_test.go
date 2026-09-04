package controller

import (
	"context"
	"iter"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpbridge"
)

// Tests that exercise controller projection install the same synchronous
// observer production callers use. The buffered channel belongs only to the
// test harness; turnHandle itself retains no replay queue.
var testTurnCaptures sync.Map

type testTurnCapture struct {
	events chan agent.SourceEvent
}

func newTestTurnHandle(cancel context.CancelFunc) *turnHandle {
	capture := &testTurnCapture{events: make(chan agent.SourceEvent, 1024)}
	handle := newTurnHandle(context.Background(), cancel, agent.SourceEventObserverFunc(func(_ context.Context, event agent.SourceEvent) error {
		capture.events <- agent.CloneSourceEvent(event)
		return nil
	}))
	testTurnCaptures.Store(handle, capture)
	return handle
}

func (h *turnHandle) SourceEvents() iter.Seq2[acpbridge.SourceEvent, error] {
	return func(yield func(acpbridge.SourceEvent, error) bool) {
		loaded, ok := testTurnCaptures.Load(h)
		if !ok {
			return
		}
		capture := loaded.(*testTurnCapture)
		defer testTurnCaptures.Delete(h)
		for {
			select {
			case source := <-capture.events:
				if !yield(testACPSourceEvent(source), source.Err) {
					return
				}
			case <-h.done:
				for {
					select {
					case source := <-capture.events:
						if !yield(testACPSourceEvent(source), source.Err) {
							return
						}
					default:
						return
					}
				}
			}
		}
	}
}

func testACPSourceEvent(source agent.SourceEvent) acpbridge.SourceEvent {
	var native *eventstream.Envelope
	if envelope, ok := source.Native.(*eventstream.Envelope); ok {
		native = acpbridge.CloneEnvelopePtr(envelope)
	}
	return acpbridge.SourceEvent{
		Canonical:                        source.Canonical,
		ACP:                              native,
		CanonicalContentAlreadyPublished: source.CanonicalContentAlreadyPublished,
	}
}
