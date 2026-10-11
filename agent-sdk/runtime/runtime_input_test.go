package runtime

import (
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestSteeringInputCorrelationDoesNotAdoptSubmissionIdentity(t *testing.T) {
	t.Parallel()
	metadata := map[string]any{
		"operation_id": "operation-one", "steering_operation_id": "operation-one",
		"actor": "forged-actor", "message_id": "forged-message", "participant_id": "forged-participant",
		"caelis": map[string]any{"agent_communication": true},
	}
	events, err := buildSteeringInputEvents(session.Session{}, "active-turn", agent.Submission{
		Kind: agent.SubmissionKindConversation, Text: "guide", DisplayInput: "display guide", Metadata: metadata,
	})
	if err != nil || len(events) != 1 {
		t.Fatalf("buildSteeringInputEvents = %#v, %v", events, err)
	}
	event := events[0]
	metadata["operation_id"] = "changed-after-admission"
	if event.Meta["operation_id"] != "operation-one" || event.Meta["steering_operation_id"] != "operation-one" || event.Meta["display_input"] != "display guide" {
		t.Fatalf("canonical steering metadata = %#v", event.Meta)
	}
	if len(event.Meta) != 3 || event.Actor.Kind != session.ActorKindUser || event.MessageID != "" || event.Scope.TurnID != "active-turn" || event.IdempotencyKey != "" {
		t.Fatalf("submission metadata changed canonical ownership: %#v", event)
	}
	for _, invalid := range []any{nil, "", "  ", 42, map[string]any{"id": "operation-one"}} {
		unrelated := &session.Event{Meta: map[string]any{"display_input": "display guide"}}
		preserveSteeringCorrelation(unrelated, map[string]any{"operation_id": invalid, "steering_operation_id": invalid})
		if len(unrelated.Meta) != 1 {
			t.Fatalf("invalid correlation %v was preserved: %#v", invalid, unrelated.Meta)
		}
	}
}

func assertCommittedSteeringCorrelation(t *testing.T, event *session.Event, operation string) {
	t.Helper()
	if event.ID == "" || event.Seq <= 0 || event.Visibility != session.VisibilityCanonical ||
		event.Meta["operation_id"] != operation || event.Meta["steering_operation_id"] != operation {
		t.Fatalf("committed steering lost correlation or persistence evidence: %#v", event)
	}
	if event.ID == operation || session.EventMessageID(event) == operation || session.EventMessageID(event) == "forged-message" {
		t.Fatalf("operation or metadata replaced canonical message/event identity: %#v", event)
	}
	for _, key := range []string{"actor", "message_id", "participant_id", "caelis"} {
		if _, present := event.Meta[key]; present {
			t.Fatalf("canonical steering adopted submission metadata %q: %#v", key, event.Meta)
		}
	}
}
