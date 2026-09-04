package acpbridge

import (
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestSourceEventFromAgentAdaptsNativeACPEnvelope(t *testing.T) {
	t.Parallel()

	envelope := &eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.RawUpdate{
			SessionUpdate: "vendor/custom",
			Raw:           []byte(`{"sessionUpdate":"vendor/custom"}`),
		},
	}
	canonical := &session.Event{ID: "e1", Type: session.EventTypeAssistant}
	adapted := SourceEventFromAgent(agent.SourceEvent{
		Canonical:                        canonical,
		Native:                           envelope,
		CanonicalContentAlreadyPublished: agent.PublishedAssistantMessage,
	})
	if adapted.Canonical == nil || adapted.Canonical.ID != "e1" {
		t.Fatalf("adapted canonical = %#v, want cloned assistant event", adapted.Canonical)
	}
	if adapted.Canonical == canonical {
		t.Fatal("adapted canonical should be cloned")
	}
	if adapted.ACP == nil || adapted.ACP == envelope {
		t.Fatalf("adapted ACP = %#v, want cloned envelope", adapted.ACP)
	}
	if !adapted.CanonicalContentAlreadyPublished.Has(agent.PublishedAssistantMessage) {
		t.Fatal("adapted source event lost live content ownership")
	}
	if update, ok := adapted.ACP.Update.(eventstream.RawUpdate); !ok || update.SessionUpdate != "vendor/custom" {
		t.Fatalf("adapted ACP update = %#v, want vendor/custom passthrough", adapted.ACP.Update)
	}
}
