package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestRenderSchedulerCoalescesAssistantFramesToOneMutation(t *testing.T) {
	m := NewModel(Config{NoColor: true, StreamTickInterval: 16 * time.Millisecond})
	m.viewport.SetWidth(80)
	m.viewport.SetHeight(20)

	for range 100 {
		updated, _, handled := m.dispatchRenderEvent(schedulerAssistantFrame("x"))
		if !handled {
			t.Fatal("assistant gateway stream frame was not handled")
		}
		m = updated.(*Model)
	}
	if got := m.doc.Len(); got != 0 {
		t.Fatalf("doc mutated before scheduler drain: len=%d, want 0", got)
	}

	updated, _ := m.Update(frameTickMsg{kind: frameTickRenderDrain, at: time.Now()})
	m = updated.(*Model)
	if got := m.doc.Len(); got != 1 {
		t.Fatalf("doc len after drain = %d, want 1", got)
	}
	if got := m.diag.ViewportQueuedSyncs; got > 1 {
		t.Fatalf("queued viewport syncs = %d, want <= 1", got)
	}
}

func schedulerAssistantFrame(text string) kernel.EventEnvelope {
	return kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			HandleID:   "handle-1",
			RunID:      "run-1",
			TurnID:     "turn-1",
			SessionRef: session.SessionRef{SessionID: "session-1"},
			Narrative: &kernel.NarrativePayload{
				Role:       kernel.NarrativeRoleAssistant,
				Text:       text,
				Visibility: string(session.VisibilityUIOnly),
				UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
				Scope:      kernel.EventScopeMain,
			},
		},
	}
}

func TestRenderSchedulerCoalescesLogChunksToOneDrainItem(t *testing.T) {
	m := NewModel(Config{NoColor: true, StreamTickInterval: 16 * time.Millisecond})

	for range 10 {
		updated, _, handled := m.dispatchRenderEvent(LogChunkMsg{Chunk: "x\n"})
		if !handled {
			t.Fatal("LogChunkMsg was not handled")
		}
		m = updated.(*Model)
	}
	if got := len(m.pendingRenderEvents.items); got != 1 {
		t.Fatalf("pending render items = %d, want 1", got)
	}
	logMsg, ok := m.pendingRenderEvents.items[0].msg.(LogChunkMsg)
	if !ok {
		t.Fatalf("pending item = %T, want LogChunkMsg", m.pendingRenderEvents.items[0].msg)
	}
	if got := strings.Count(logMsg.Chunk, "\n"); got != 10 {
		t.Fatalf("coalesced log lines = %d, want 10", got)
	}

	updated, _ := m.Update(frameTickMsg{kind: frameTickRenderDrain, at: time.Now()})
	m = updated.(*Model)
	if got := len(m.pendingRenderEvents.items); got != 0 {
		t.Fatalf("pending render items after drain = %d, want 0", got)
	}
	if got := m.diag.ViewportQueuedSyncs; got > 1 {
		t.Fatalf("queued viewport syncs = %d, want <= 1", got)
	}
}

func TestRenderSchedulerPreservesDeferredCommands(t *testing.T) {
	m := NewModel(Config{NoColor: true, StreamTickInterval: 16 * time.Millisecond})
	if !m.queueLogChunk("hello\n") {
		t.Fatal("queueLogChunk returned false")
	}

	if cmd := m.flushPendingDeferredBatches(); cmd == nil {
		t.Fatal("flushPendingDeferredBatches returned nil command")
	}
}
