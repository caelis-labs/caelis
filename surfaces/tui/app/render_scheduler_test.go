package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestRenderSchedulerCoalescesACPAssistantEnvelopesToOneMutation(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true, StreamTickInterval: 16 * time.Millisecond})
	model.viewport.SetWidth(80)
	model.viewport.SetHeight(20)

	for range 100 {
		updated, _, handled := model.dispatchRenderEvent(schedulerACPAssistantEnvelope("x"))
		if !handled {
			t.Fatal("ACP assistant envelope was not handled")
		}
		model = updated.(*Model)
	}
	if got := model.doc.Len(); got != 0 {
		t.Fatalf("doc mutated before scheduler drain: len=%d, want 0", got)
	}
	if got := len(model.pendingRenderEvents.items); got != 1 {
		t.Fatalf("pending render items = %d, want 1", got)
	}
	pending, ok := model.pendingRenderEvents.items[0].msg.(eventstream.Envelope)
	if !ok {
		t.Fatalf("pending item = %T, want eventstream.Envelope", model.pendingRenderEvents.items[0].msg)
	}
	update, ok := pending.Update.(schema.ContentChunk)
	if !ok {
		t.Fatalf("pending update = %T, want ContentChunk", pending.Update)
	}
	if got := strings.Count(update.Content.(schema.TextContent).Text, "x"); got != 100 {
		t.Fatalf("coalesced assistant chunks = %d, want 100", got)
	}

	updated, _ := model.Update(frameTickMsg{kind: frameTickRenderDrain, at: time.Now()})
	model = updated.(*Model)
	if got := len(model.pendingRenderEvents.items); got != 0 {
		t.Fatalf("pending render items after drain = %d, want 0", got)
	}
	if got := model.doc.Len(); got != 1 {
		t.Fatalf("doc len after drain = %d, want 1", got)
	}
	if got := model.diag.ViewportQueuedSyncs; got > 1 {
		t.Fatalf("queued viewport syncs = %d, want <= 1", got)
	}
	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 || block.Events[0].Kind != SEAssistant || len(block.Events[0].Text) != 100 {
		t.Fatalf("main ACP events = %#v, want one coalesced assistant event", block.Events)
	}
}

func TestEventStreamNarrativeBatchKeyPreservesMessageIDBoundary(t *testing.T) {
	t.Parallel()

	first := schedulerACPAssistantEnvelope("first")
	firstUpdate := first.Update.(schema.ContentChunk)
	firstUpdate.MessageID = "message-1"
	first.Update = firstUpdate
	second := schedulerACPAssistantEnvelope("second")
	secondUpdate := second.Update.(schema.ContentChunk)
	secondUpdate.MessageID = "message-2"
	second.Update = secondUpdate

	firstKey, firstOK := eventStreamNarrativeBatchKey(first)
	secondKey, secondOK := eventStreamNarrativeBatchKey(second)
	if !firstOK || !secondOK {
		t.Fatalf("narrative batch keys unavailable: first=%t second=%t", firstOK, secondOK)
	}
	if firstKey == secondKey {
		t.Fatalf("different ACP message IDs shared one narrative batch key: %q", firstKey)
	}
}

func schedulerACPAssistantEnvelope(text string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: text},
		},
	}
}

func TestRenderSchedulerCoalescesLogChunksToOneDrainItem(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true, StreamTickInterval: 16 * time.Millisecond})

	for range 10 {
		updated, _, handled := model.dispatchRenderEvent(LogChunkMsg{Chunk: "x\n"})
		if !handled {
			t.Fatal("LogChunkMsg was not handled")
		}
		model = updated.(*Model)
	}
	if got := len(model.pendingRenderEvents.items); got != 1 {
		t.Fatalf("pending render items = %d, want 1", got)
	}
	logMsg, ok := model.pendingRenderEvents.items[0].msg.(LogChunkMsg)
	if !ok {
		t.Fatalf("pending item = %T, want LogChunkMsg", model.pendingRenderEvents.items[0].msg)
	}
	if got := strings.Count(logMsg.Chunk, "\n"); got != 10 {
		t.Fatalf("coalesced log lines = %d, want 10", got)
	}

	updated, _ := model.Update(frameTickMsg{kind: frameTickRenderDrain, at: time.Now()})
	model = updated.(*Model)
	if got := len(model.pendingRenderEvents.items); got != 0 {
		t.Fatalf("pending render items after drain = %d, want 0", got)
	}
	if got := model.diag.ViewportQueuedSyncs; got > 1 {
		t.Fatalf("queued viewport syncs = %d, want <= 1", got)
	}
}

func TestRenderSchedulerPreservesDeferredCommands(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true, StreamTickInterval: 16 * time.Millisecond})
	if !model.queueLogChunk("hello\n") {
		t.Fatal("queueLogChunk returned false")
	}
	if cmd := model.flushPendingDeferredBatches(); cmd == nil {
		t.Fatal("flushPendingDeferredBatches returned nil command")
	}
}
