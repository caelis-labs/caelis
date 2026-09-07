package evalharness

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestVisibleTraceExcludesJournalWithoutRemovingRawEvidence(t *testing.T) {
	receipt := session.NewModelInvocationReceipt(model.Invocation{ID: "attempt", Usage: model.Usage{TotalTokens: 12}}, "chat")
	answer := &session.Event{Type: session.EventTypeAssistant, Text: "answer"}
	raw := []*session.Event{receipt, answer}
	if got := CanonicalEvents(raw); len(got) != 1 || got[0].Type != session.EventTypeAssistant {
		t.Fatalf("canonical trace = %#v", got)
	}
	if got := EventTrace(raw); len(got) != 1 || got[0].Text != "answer" {
		t.Fatalf("visible trace = %#v", got)
	}
	if len(raw) != 2 || !session.IsModelInvocationReceipt(raw[0]) || session.UsageSnapshotFromSessionEvent(raw[0]).TotalTokens != 12 {
		t.Fatalf("raw evidence changed: %#v", raw)
	}
}
