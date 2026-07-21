package kernel

import (
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
)

func TestForwardSourceEventsKeepsObservationGapTransientAndContinues(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	firstMessage := model.NewTextMessage(model.RoleAssistant, "before gap")
	lastMessage := model.NewTextMessage(model.RoleAssistant, "after gap")
	source := acpbridge.SourceStream{Events: func(yield func(acpbridge.SourceEvent, error) bool) {
		if !yield(acpbridge.SourceEvent{Canonical: &session.Event{
			ID: "event-1", Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Message: &firstMessage,
		}}, nil) {
			return
		}
		if !yield(acpbridge.SourceEvent{}, &agent.EventStreamGapError{Dropped: 7}) {
			return
		}
		yield(acpbridge.SourceEvent{Canonical: &session.Event{
			ID: "event-9", Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Message: &lastMessage,
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
	if got[1].Notice != acpprojector.RuntimeObservationGapNotice {
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

func TestACPFinalAssistantMaterializationIgnoresAuditSource(t *testing.T) {
	t.Parallel()

	message := model.NewTextMessage(model.RoleAssistant, "done")
	for _, source := range []string{"acp", "slash", "renamed-product-source"} {
		event := &session.Event{
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    &message,
			Scope:      &session.EventScope{Source: source},
		}
		if !isACPFinalAssistantMaterialization(event) {
			t.Fatalf("source %q changed final materialization classification", source)
		}
	}
}
