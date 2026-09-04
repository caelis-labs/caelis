package wirev1

import (
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestEnvelopeRoundTripPreservesAgentCommunicationSource(t *testing.T) {
	t.Parallel()

	want := eventstream.ActorIdentity{Kind: "participant", ID: "reviewer-1", Role: "delegated", Name: "reviewer"}
	envelope := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, AgentCommunicationSource: &want,
		Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateUserMessage,
			Content: eventstream.TextContent{Type: "text", Text: "review complete"},
		},
	}
	cloned := eventstream.CloneEnvelope(envelope)
	cloned.AgentCommunicationSource.ID = "another-actor"
	if envelope.AgentCommunicationSource.ID != "reviewer-1" {
		t.Fatal("clone shares sender identity")
	}
	raw, err := MarshalEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := UnmarshalEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.AgentCommunicationSource == nil || *decoded.AgentCommunicationSource != want {
		t.Fatalf("source = %#v, want %#v", decoded.AgentCommunicationSource, want)
	}
}
