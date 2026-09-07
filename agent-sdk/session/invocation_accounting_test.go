package session

import (
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"testing"
)

func TestInvocationAccountingRequiresMatchingDurableReceipt(t *testing.T) {
	receipt := NewModelInvocationReceipt(model.Invocation{ID: "attempt", Provider: "p", Model: "m", Usage: model.Usage{TotalTokens: 12}, Outcome: "completed"}, "chat")
	receipt.SessionID = "s"
	receipt.Scope = &EventScope{TurnID: "turn"}
	response := &Event{SessionID: "s", Type: EventTypeAssistant, Visibility: VisibilityCanonical, Scope: &EventScope{TurnID: "turn"}, Invocation: &EventInvocation{Provider: "p", Model: "m"}, Meta: map[string]any{"caelis": map[string]any{"sdk": map[string]any{"usage_receipt_id": "attempt", "usage": map[string]any{"total_tokens": 12}}}}}
	if got := InvocationAccountingEvents([]*Event{receipt, response}); len(got) != 1 || got[0] != receipt {
		t.Fatalf("matching receipt not selected: %#v", got)
	}
	for _, mutation := range []struct {
		name  string
		apply func(*Event)
	}{
		{"session", func(e *Event) { e.SessionID = "other" }},
		{"turn", func(e *Event) { e.Scope.TurnID = "other" }},
		{"provider", func(e *Event) { e.Invocation.Provider = "other" }},
		{"model", func(e *Event) { e.Invocation.Model = "other" }},
		{"tool_claim", func(e *Event) { e.Type = EventTypeToolResult; e.Visibility = VisibilityCanonical }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			other := CloneEvent(receipt)
			mutation.apply(other)
			if got := InvocationAccountingEvents([]*Event{other, response}); len(got) != 2 {
				t.Fatalf("invalid receipt suppressed usage: %#v", got)
			}
		})
	}
	if got := InvocationAccountingEvents([]*Event{response}); len(got) != 1 {
		t.Fatal("dangling reference dropped legacy measurement")
	}
	if got := InvocationAccountingEvents([]*Event{receipt, CloneEvent(receipt), response}); len(got) != 1 {
		t.Fatal("duplicate receipt counted")
	}
	if IsMainInvocationVisibleEvent(receipt) || IsClientReplayEvent(receipt) {
		t.Fatal("receipt entered context/replay")
	}
}
