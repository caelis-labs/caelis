package projection

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestSteeringAppliedCorrelationUsesCommittedInputInLiveAndRecovery(t *testing.T) {
	t.Parallel()
	message := model.NewTextMessage(model.RoleUser, "guide")
	committed := &session.Event{
		ID: "canonical-event-one", Seq: 4, SessionID: "session-one",
		Type: session.EventTypeUser, Visibility: session.VisibilityCanonical,
		Message: &message, Text: "guide",
		Meta:  map[string]any{"operation_id": "operation-one", "steering_operation_id": "operation-one"},
		Scope: &session.EventScope{TurnID: "turn-one", Source: "user_side_agent", Participant: session.ParticipantRef{ID: "helper", Kind: session.ParticipantKindACP}},
	}
	for _, transport := range []SessionEventTransport{{HandleID: "live-handle", RunID: "live-run"}, {}} {
		base := EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session-one"}, session.CloneEvent(committed), transport)
		if base.InputOperationID != "operation-one" || base.InputStatus != "applied" || base.EventID != "canonical-event-one" || base.ParticipantID != "helper" || base.Scope != eventstream.ScopeParticipant {
			t.Fatalf("live/recovery correlation = %#v", base)
		}
	}
	for _, change := range []func(*session.Event){
		func(event *session.Event) { event.Seq = 0 },
		func(event *session.Event) { event.Visibility = session.VisibilityUIOnly },
		func(event *session.Event) { event.Type = session.EventTypeContext },
		func(event *session.Event) { event.Meta["steering_operation_id"] = 42 },
	} {
		uncommitted := session.CloneEvent(committed)
		change(uncommitted)
		base := EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session-one"}, uncommitted, SessionEventTransport{})
		if base.InputOperationID != "" || base.InputStatus != "" {
			t.Fatalf("noncanonical or non-user input claimed applied: %#v", base)
		}
	}
}
