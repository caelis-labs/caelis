package kernel

import (
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestForwardSourceEventsKeepsObservationGapTransientAndContinues(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	firstMessage := model.NewTextMessage(model.RoleAssistant, "before gap")
	lastMessage := model.NewTextMessage(model.RoleAssistant, "after gap")
	source := acpbridge.SourceStream{Events: func(yield func(acpbridge.SourceEvent, error) bool) {
		if !yield(acpbridge.SourceEvent{Canonical: &session.Event{
			ID: "event-1", Type: session.EventTypeAssistant, Visibility: session.VisibilityUIOnly, Message: &firstMessage,
		}}, nil) {
			return
		}
		if !yield(acpbridge.SourceEvent{}, &agent.EventStreamGapError{Dropped: 7}) {
			return
		}
		yield(acpbridge.SourceEvent{Canonical: &session.Event{
			ID: "event-9", Type: session.EventTypeAssistant, Visibility: session.VisibilityUIOnly, Message: &lastMessage,
		}}, nil)
	}}
	(&Gateway{}).forwardSourceEvents(session.Session{SessionRef: handle.sessionRef}, handle, source)

	got, _, err := handle.eventsAfter("")
	if err != nil {
		t.Fatalf("eventsAfter() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("eventsAfter() = %#v, want canonical, transient gap notice, canonical", got)
	}
	if got[0].EventID != "event-1" || got[2].EventID != "event-9" {
		t.Fatalf("forwarded event ids = %q, %q, want event-1 and event-9", got[0].EventID, got[2].EventID)
	}
	if got[1].Kind != eventstream.KindNotice || got[1].Delivery == nil || got[1].Delivery.Mode != eventstream.DeliveryTransient {
		t.Fatalf("gap envelope = %#v, want transient notice", got[1])
	}
	if got[1].Notice != acpbridge.RuntimeObservationGapNotice {
		t.Fatalf("gap Notice = %q, want stable presentation text", got[1].Notice)
	}
	observation := metautil.RuntimeSection(got[1].Meta, metautil.RuntimeObservation)
	if observation[metautil.RuntimeObservationCode] != metautil.RuntimeObservationGap {
		t.Fatalf("gap observation code = %#v, want %q", observation[metautil.RuntimeObservationCode], metautil.RuntimeObservationGap)
	}
	if observation[metautil.RuntimeObservationDropped] != uint64(7) {
		t.Fatalf("gap dropped = %#v, want 7", observation[metautil.RuntimeObservationDropped])
	}
	if handle.failed {
		t.Fatal("observation gap marked the Runtime turn failed")
	}
}

func TestForwardSourceEventsPublishesFinalUsageWithoutRepeatedAssistantContent(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	delta := model.NewTextMessage(model.RoleAssistant, "done")
	final := model.NewTextMessage(model.RoleAssistant, "done")
	source := acpbridge.SourceStream{Events: func(yield func(acpbridge.SourceEvent, error) bool) {
		if !yield(acpbridge.SourceEvent{Canonical: session.MarkUIOnly(&session.Event{
			Type: session.EventTypeAssistant, Message: &delta,
		})}, nil) {
			return
		}
		yield(acpbridge.SourceEvent{
			Canonical: &session.Event{
				ID: "assistant-final", Seq: 2, Type: session.EventTypeAssistant,
				Visibility: session.VisibilityCanonical, Message: &final,
				Meta: map[string]any{"usage": map[string]any{
					"prompt_tokens": 8, "completion_tokens": 1, "total_tokens": 9,
				}},
			},
			CanonicalContentAlreadyPublished: agent.PublishedAssistantMessage,
		}, nil)
	}}
	(&Gateway{}).forwardSourceEvents(session.Session{SessionRef: handle.sessionRef}, handle, source)

	got, _, err := handle.eventsAfter("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || eventstream.UpdateType(got[0].Update) != "agent_message_chunk" ||
		eventstream.UpdateType(got[1].Update) != "usage_update" {
		t.Fatalf("live source projection = %#v, want delta once followed by final usage", got)
	}
}

func TestForwardSourceEventsPublishesTaskOwnedRunCommandFinalWithoutTerminalBytes(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID: "task-1", metautil.RuntimeOutputDelta: "ok\n",
		"kind": "command", "state": "completed", "running": false,
	})
	source := acpbridge.SourceStream{Events: func(yield func(acpbridge.SourceEvent, error) bool) {
		yield(acpbridge.SourceEvent{
			Canonical: &session.Event{
				ID: "command-final", Seq: 2, Type: session.EventTypeToolResult,
				Visibility: session.VisibilityCanonical, Meta: meta,
				Tool: &session.EventTool{
					ID: "call-1", Name: "RunCommand", Status: "completed",
					Output:  map[string]any{"stdout": "ok\n", "exit_code": 0},
					Content: []session.EventToolContent{{Type: "terminal", TerminalID: "terminal-1", Text: "ok\n"}},
				},
			},
			CanonicalContentAlreadyPublished: agent.PublishedTerminal,
		}, nil)
	}}
	(&Gateway{}).forwardSourceEvents(session.Session{SessionRef: handle.sessionRef}, handle, source)

	got, _, err := handle.eventsAfter("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("live source projection = %#v, want one final state update", got)
	}
	update, ok := got[0].Update.(schema.ToolCallUpdate)
	if !ok || stringPtrValue(update.Status) != schema.ToolStatusCompleted {
		t.Fatalf("live final update = %#v, want completed ToolCallUpdate", got[0].Update)
	}
	if _, ok := metautil.TerminalOutput(update.Meta); ok {
		t.Fatalf("live final update meta = %#v, want no terminal bytes", update.Meta)
	}
	if delta := metautil.RuntimeSection(got[0].Meta, metautil.RuntimeTask)[metautil.RuntimeOutputDelta]; delta != nil {
		t.Fatalf("live final envelope meta output_delta = %#v, want Task stream ownership", delta)
	}
}

func TestForwardSourceEventsKeepsAnswerWhenOnlyThoughtWasPublished(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	thought := model.NewReasoningMessage(model.RoleAssistant, "thinking", model.ReasoningVisibilityVisible)
	final := model.NewMessage(
		model.RoleAssistant,
		model.NewReasoningPart("thinking", model.ReasoningVisibilityVisible),
		model.NewTextPart("answer"),
	)
	source := acpbridge.SourceStream{Events: func(yield func(acpbridge.SourceEvent, error) bool) {
		if !yield(acpbridge.SourceEvent{Canonical: session.MarkUIOnly(&session.Event{
			Type: session.EventTypeAssistant, MessageID: "message-1", Message: &thought,
			Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentThought), MessageID: "message-1",
			}},
		})}, nil) {
			return
		}
		yield(acpbridge.SourceEvent{
			Canonical: &session.Event{
				ID: "assistant-final", Seq: 2, Type: session.EventTypeAssistant,
				Visibility: session.VisibilityCanonical, MessageID: "message-1", Message: &final,
			},
			CanonicalContentAlreadyPublished: agent.PublishedAssistantThought,
		}, nil)
	}}
	(&Gateway{}).forwardSourceEvents(session.Session{SessionRef: handle.sessionRef}, handle, source)

	got, _, err := handle.eventsAfter("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || eventstream.UpdateType(got[0].Update) != schema.UpdateAgentThought ||
		eventstream.UpdateType(got[1].Update) != schema.UpdateAgentMessage {
		t.Fatalf("live source projection = %#v, want thought delta followed by the unpublished final answer", got)
	}
}

func TestForwardSourceEventsPublishesPairedNativeContentBeforeCanonicalUsage(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	message := model.NewTextMessage(model.RoleAssistant, "complete")
	native := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			MessageID:     "message-1",
			Content:       schema.TextContent{Type: "text", Text: "delta"},
		},
	}
	source := acpbridge.SourceStream{Events: func(yield func(acpbridge.SourceEvent, error) bool) {
		yield(acpbridge.SourceEvent{
			Canonical: &session.Event{
				ID: "assistant-final", Seq: 2, Type: session.EventTypeAssistant,
				Visibility: session.VisibilityCanonical, Message: &message,
				Meta: map[string]any{"usage": map[string]any{
					"prompt_tokens": 8, "completion_tokens": 1, "total_tokens": 9,
				}},
			},
			ACP: &native,
		}, nil)
	}}
	(&Gateway{}).forwardSourceEvents(session.Session{SessionRef: handle.sessionRef}, handle, source)

	got, _, err := handle.eventsAfter("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || eventstream.UpdateType(got[0].Update) != schema.UpdateAgentMessage ||
		eventstream.UpdateType(got[1].Update) != schema.UpdateUsage {
		t.Fatalf("paired source projection = %#v, want native content followed by canonical usage", got)
	}
	if got[1].ProjectionID != "acp-projection:YXNzaXN0YW50LWZpbmFs:1" {
		t.Fatalf("usage projection ID = %q, want canonical sibling index 1", got[1].ProjectionID)
	}
}

func TestForwardSourceEventsKeepsPairedNativeTerminalAsSingleLiveAuthority(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	meta := metautil.WithTerminalOutput(nil, "command-1", "exact terminal delta\n")
	status := schema.ToolStatusInProgress
	native := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "command-1",
			Status:        &status,
			Meta:          meta,
		},
	}
	source := acpbridge.SourceStream{Events: func(yield func(acpbridge.SourceEvent, error) bool) {
		yield(acpbridge.SourceEvent{
			Canonical: session.MarkUIOnly(&session.Event{
				ID:   "terminal-delta",
				Type: session.EventTypeToolCall,
				Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
					SessionUpdate: schema.UpdateToolCallInfo,
					ToolCallID:    "command-1",
					Status:        status,
					Meta:          meta,
				}},
			}),
			ACP: &native,
		}, nil)
	}}
	(&Gateway{}).forwardSourceEvents(session.Session{SessionRef: handle.sessionRef}, handle, source)

	got, _, err := handle.eventsAfter("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("paired terminal projection = %#v, want one native terminal delta", got)
	}
	update, ok := got[0].Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("terminal update = %T, want ToolCallUpdate", got[0].Update)
	}
	output, ok := metautil.TerminalOutput(update.Meta)
	if !ok || output.Data != "exact terminal delta\n" {
		t.Fatalf("terminal output = %#v, %v; want exact native delta", output, ok)
	}
}

func TestForwardSourceEventsDoesNotDuplicateNativeUsage(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	native := eventstream.Envelope{
		Kind:   eventstream.KindSessionUpdate,
		Update: schema.UsageUpdate{SessionUpdate: schema.UpdateUsage, Used: 9, Size: 9},
	}
	source := acpbridge.SourceStream{Events: func(yield func(acpbridge.SourceEvent, error) bool) {
		yield(acpbridge.SourceEvent{
			Canonical: &session.Event{
				ID: "assistant-final", Seq: 2, Type: session.EventTypeAssistant,
				Visibility: session.VisibilityCanonical,
				Meta: map[string]any{"usage": map[string]any{
					"prompt_tokens": 8, "completion_tokens": 1, "total_tokens": 9,
				}},
			},
			ACP: &native,
		}, nil)
	}}
	(&Gateway{}).forwardSourceEvents(session.Session{SessionRef: handle.sessionRef}, handle, source)

	got, _, err := handle.eventsAfter("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || eventstream.UpdateType(got[0].Update) != schema.UpdateUsage {
		t.Fatalf("paired native usage = %#v, want exactly one usage update", got)
	}
}
