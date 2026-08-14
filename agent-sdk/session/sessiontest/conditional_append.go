package sessiontest

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ConditionalAppendStore is the cross-Session revision-fenced append surface
// exercised by ConditionalAppendConformance.
type ConditionalAppendStore interface {
	session.Service
	session.ConditionalEventAppenderWithOutcome
}

// ConditionalAppendConformance verifies that a stale related Session blocks
// the target append without changing its events or revision.
func ConditionalAppendConformance(t *testing.T, factory func(*testing.T) ConditionalAppendStore) {
	t.Helper()
	store := factory(t)
	ctx := context.Background()
	target, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "sessiontest", UserID: "target"})
	if err != nil {
		t.Fatal(err)
	}
	related, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "sessiontest", UserID: "related"})
	if err != nil {
		t.Fatal(err)
	}
	staleRelatedRevision := related.Revision
	related, err = store.BindController(ctx, session.BindControllerRequest{
		SessionRef: related.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "controller-1", EpochID: "epoch-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	message := model.NewTextMessage(model.RoleUser, "conditionally accepted")
	targetRevision := target.Revision
	request := session.ConditionalAppendEventRequest{
		AppendEventRequest: session.AppendEventRequest{
			SessionRef: target.SessionRef, ExpectedRevision: &targetRevision,
			Event: &session.Event{
				ID: "conditional-event", IdempotencyKey: "conditional:event",
				Type: session.EventTypeContext, Visibility: session.VisibilityCanonical,
				Actor:   session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
				Message: &message,
			},
		},
		RelatedRevisions: []session.SessionRevisionPrecondition{{
			SessionRef: related.SessionRef, ExpectedRevision: staleRelatedRevision,
		}},
	}
	_, err = store.AppendEventWithOutcomeConditional(ctx, request)
	var conflict *session.RevisionConflictError
	if !errors.As(err, &conflict) || conflict.SessionID != related.SessionID {
		t.Fatalf("stale related revision error = %v, want related RevisionConflictError", err)
	}
	unchanged, err := store.Session(ctx, target.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(ctx, session.EventsRequest{SessionRef: target.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != target.Revision || len(events) != 0 {
		t.Fatalf("failed conditional append changed target to revision %d with events %#v", unchanged.Revision, events)
	}

	request.RelatedRevisions[0].ExpectedRevision = related.Revision
	result, err := store.AppendEventWithOutcomeConditional(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Appended || result.Event == nil || result.Event.ID != "conditional-event" {
		t.Fatalf("conditional append result = %#v, want newly appended event", result)
	}
}
