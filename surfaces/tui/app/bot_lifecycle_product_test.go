package tuiapp

import (
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestProductBotLiveAndReattachedTerminalStatus(t *testing.T) {
	h := newBotCommandProductHarness(t)
	value := createProductBot(t, h.ctx, h.clients.Bots, "Ada")
	h.attach(t, value)
	assertBotLifecycleStatus(t, h.model, "idle")

	cmd := h.model.executeLineCmd(Submission{Text: "hello"})
	if cmd == nil {
		t.Fatal("Bot prompt scheduled no dispatch")
	}
	if msg := cmd(); msg != nil {
		h.apply(t, msg)
	}
	for {
		select {
		case msg := <-h.messages:
			env, ok := h.apply(t, msg).(eventstream.Envelope)
			if !ok || !eventstream.IsTurnTerminalLifecycle(env) {
				continue
			}
			if env.SessionID != value.SessionID || env.Lifecycle.State != eventstream.LifecycleStateCompleted {
				t.Fatalf("unexpected Bot terminal: %+v", env)
			}
		case <-h.ctx.Done():
			t.Fatalf("waiting for the canonical Bot terminal: %v", h.ctx.Err())
		}
		break
	}
	if h.model.turnRunning() {
		t.Fatal("canonical Bot completion left the reply running")
	}
	assertBotLifecycleStatus(t, h.model, "last reply completed")

	// Reattaching consumes the real Host's recovered RunState and canonical
	// history; it must not change /status or submit the previous input again.
	h.attach(t, value)
	assertBotLifecycleStatus(t, h.model, "last reply completed")
	if calls := h.providerCalls.Load(); calls != 1 {
		t.Fatalf("provider calls after reattach = %d, want 1", calls)
	}
}
