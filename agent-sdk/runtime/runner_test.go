package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestRunnerPublishesSynchronouslyToInstalledObserver(t *testing.T) {
	var got []agent.SourceEvent
	observer := agent.SourceEventObserverFunc(func(_ context.Context, event agent.SourceEvent) error {
		got = append(got, agent.CloneSourceEvent(event))
		return nil
	})
	runner := newRunner(t.Context(), "run-1", func() {}, observer)
	runner.publishEvent(&session.Event{ID: "event-1", Type: session.EventTypeAssistant})
	runner.publishError(errors.New("producer failed"))
	runner.finish()

	if len(got) != 2 || got[0].Canonical == nil || got[0].Canonical.ID != "event-1" || got[1].Err == nil {
		t.Fatalf("observed SourceEvents = %#v", got)
	}
	if err := runner.WaitCompletion(t.Context()); err == nil || err.Error() != "producer failed" {
		t.Fatalf("WaitCompletion() error = %v", err)
	}
}

func TestRunnerWithoutObserverRetainsNoPayloadQueue(t *testing.T) {
	runner := newRunner(t.Context(), "run-1", func() {}, nil)
	for range 1024 {
		runner.publishEvent(&session.Event{ID: "event", Type: session.EventTypeAssistant})
	}
	runner.finish()
	if err := runner.WaitCompletion(t.Context()); err != nil {
		t.Fatalf("WaitCompletion() error = %v", err)
	}
}

func TestRunnerObserverFailureDoesNotCancelProducer(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	runner := newRunner(t.Context(), "run-1", func() { cancelled <- struct{}{} }, agent.SourceEventObserverFunc(
		func(context.Context, agent.SourceEvent) error { return errors.New("spool unavailable") },
	))
	runner.publishEvent(&session.Event{ID: "event-1", Type: session.EventTypeAssistant})
	runner.finish()
	select {
	case <-cancelled:
		t.Fatal("observer failure cancelled the producer")
	case <-time.After(20 * time.Millisecond):
	}
	if err := runner.WaitCompletion(t.Context()); err != nil {
		t.Fatalf("WaitCompletion() error = %v", err)
	}
}

func TestRunnerSourceOwnershipUsesStableMessageIdentity(t *testing.T) {
	var got []agent.SourceEvent
	runner := newRunner(t.Context(), "run-1", func() {}, agent.SourceEventObserverFunc(
		func(_ context.Context, event agent.SourceEvent) error {
			got = append(got, agent.CloneSourceEvent(event))
			return nil
		},
	))
	delta := model.NewTextMessage(model.RoleAssistant, "prefix")
	runner.publishEvent(session.MarkUIOnly(&session.Event{
		Type: session.EventTypeAssistant, MessageID: "message-1", Message: &delta,
	}))
	final := model.NewTextMessage(model.RoleAssistant, "complete")
	runner.publishEvent(&session.Event{
		Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical,
		MessageID: "message-1", Message: &final,
	})
	if len(got) != 2 || !got[1].CanonicalContentAlreadyPublished.Has(agent.PublishedAssistantMessage) {
		t.Fatalf("observed ownership = %#v", got)
	}
}
