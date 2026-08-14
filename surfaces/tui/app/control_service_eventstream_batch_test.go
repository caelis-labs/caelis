package tuiapp

import (
	"context"
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestForwardTurnEventStreamCoalescesNarrativeWithoutCorruptingEnvelopeIdentity(t *testing.T) {
	t.Parallel()

	events := make(chan eventstream.Envelope, 8)
	first := narrativeBatchEnvelope("event-1", "cursor-1", 1, "message-1", "first ")
	second := narrativeBatchEnvelope("event-2", "cursor-2", 2, "message-1", "second")
	repeatedBoundary := narrativeBatchEnvelope("event-3", "cursor-3", 3, "message-1", "d answer")
	nextMessage := narrativeBatchEnvelope("event-4", "cursor-4", 4, "message-2", "next")
	barrier := eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: "session-1", HandleID: "handle-1",
		RunID: "run-1", TurnID: "turn-1", Notice: "semantic barrier",
	}
	beforeTerminal := narrativeBatchEnvelope("event-5", "cursor-5", 5, "message-3", "final narrative")
	terminal := eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(300, 0))
	for _, envelope := range []eventstream.Envelope{first, second, repeatedBoundary, nextMessage, barrier, beforeTerminal, terminal} {
		events <- envelope
	}
	close(events)

	var sent []tea.Msg
	result := forwardTurnEventStream(context.Background(), &eventstreamIntegrationTurn{events: events}, &ProgramSender{
		Send: func(message tea.Msg) { sent = append(sent, message) },
	})
	if !result.queued {
		t.Fatalf("forward result = %#v, want queued", result)
	}
	if len(sent) != 5 {
		t.Fatalf("sent messages = %#v, want merged m1, m2, barrier, m3, terminal", sent)
	}

	merged := requireNarrativeBatchEnvelope(t, sent[0])
	if got := narrativeBatchText(t, merged); got != "first secondd answer" {
		t.Fatalf("merged narrative = %q, want exact ACP delta concatenation", got)
	}
	if merged.EventID != repeatedBoundary.EventID || merged.ProjectionID != repeatedBoundary.ProjectionID || merged.Cursor != repeatedBoundary.Cursor || !reflect.DeepEqual(merged.Position, repeatedBoundary.Position) || !reflect.DeepEqual(merged.Delivery, repeatedBoundary.Delivery) {
		t.Fatalf("merged identity = %#v, want complete latest-frame identity %#v", merged, repeatedBoundary)
	}
	if got := narrativeBatchText(t, requireNarrativeBatchEnvelope(t, sent[1])); got != "next" {
		t.Fatalf("different-message narrative = %q, want independent flush", got)
	}
	if got, ok := sent[2].(eventstream.Envelope); !ok || got.Kind != eventstream.KindNotice || got.Notice != "semantic barrier" {
		t.Fatalf("barrier message = %#v", sent[2])
	}
	if got := narrativeBatchText(t, requireNarrativeBatchEnvelope(t, sent[3])); got != "final narrative" {
		t.Fatalf("pre-terminal narrative = %q, want flushed before terminal", got)
	}
	last, ok := sent[4].(eventstream.Envelope)
	if !ok || !eventstream.IsTerminalLifecycle(last) {
		t.Fatalf("last message = %#v, want terminal", sent[4])
	}
}

func TestNarrativeBatcherCoalescesTinySubagentChunksIntoOneVisualUpdate(t *testing.T) {
	t.Parallel()

	first := narrativeBatchEnvelope("event-1", "cursor-1", 1, "message-1", "一")
	first.Scope = eventstream.ScopeSubagent
	first.ScopeID = "task-1"
	second := narrativeBatchEnvelope("event-2", "cursor-2", 2, "message-1", "行")
	second.Scope = eventstream.ScopeSubagent
	second.ScopeID = "task-1"

	var sent []tea.Msg
	send := func(message tea.Msg) { sent = append(sent, message) }
	var batcher eventStreamNarrativeBatcher
	if !batcher.enqueue(first, send) {
		t.Fatal("first subagent chunk was not accepted by the narrative batcher")
	}
	started := time.Unix(400, 0)
	batcher.pendingSince = started
	batcher.flushReady(started.Add(eventStreamBatchInterval), send)
	if len(sent) != 0 {
		t.Fatalf("tiny subagent chunk flushed as a one-character frame: %#v", sent)
	}
	if !batcher.enqueue(second, send) {
		t.Fatal("second subagent chunk was not accepted by the narrative batcher")
	}
	batcher.flushReady(started.Add(eventStreamSubagentBatchMaxDelay), send)
	if len(sent) != 1 {
		t.Fatalf("sent messages = %#v, want one coalesced subagent update", sent)
	}
	if got := narrativeBatchText(t, requireNarrativeBatchEnvelope(t, sent[0])); got != "一行" {
		t.Fatalf("coalesced subagent narrative = %q, want %q", got, "一行")
	}
}

func narrativeBatchEnvelope(eventID string, cursor string, sequence uint64, messageID string, text string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", HandleID: "handle-1",
		RunID: "run-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		EventID: eventID, ProjectionID: eventstream.FormatProjectionID(eventID, 0), Cursor: cursor,
		Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Generation: "generation-1", Sequence: sequence,
		}},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			MessageID:     messageID,
			Content:       schema.TextContent{Type: "text", Text: text},
		},
	}
}

func requireNarrativeBatchEnvelope(t *testing.T, message tea.Msg) eventstream.Envelope {
	t.Helper()
	envelope, ok := message.(eventstream.Envelope)
	if !ok {
		t.Fatalf("message = %T, want eventstream.Envelope", message)
	}
	return envelope
}

func narrativeBatchText(t *testing.T, envelope eventstream.Envelope) string {
	t.Helper()
	update, ok := envelope.Update.(schema.ContentChunk)
	if !ok {
		t.Fatalf("update = %T, want schema.ContentChunk", envelope.Update)
	}
	content, ok := update.Content.(schema.TextContent)
	if !ok {
		t.Fatalf("content = %T, want schema.TextContent", update.Content)
	}
	return content.Text
}
