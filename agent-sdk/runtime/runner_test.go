package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestRunnerSourceEventsDoesNotBlockOnUndrainedLegacyEvents(t *testing.T) {
	t.Parallel()

	runner := newRunner("run-1", func() {})
	done := make(chan int, 1)
	go func() {
		count := 0
		for event, err := range runner.SourceEvents() {
			if err != nil {
				continue
			}
			if event.Canonical != nil {
				count++
			}
		}
		done <- count
	}()

	published := make(chan struct{})
	go func() {
		defer close(published)
		for i := 0; i < 128; i++ {
			runner.publishEvent(&session.Event{ID: "event", Type: session.EventTypeAssistant})
		}
		runner.finish()
	}()

	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("publishEvent blocked while only SourceEvents was drained")
	}
	select {
	case count := <-done:
		if count != 128 {
			t.Fatalf("SourceEvents received %d events, want 128", count)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SourceEvents to close")
	}
}

func TestRunnerRejectsCompetingEventConsumers(t *testing.T) {
	t.Parallel()

	runner := newRunner("run-1", func() {})
	runner.publishEvent(&session.Event{ID: "event-1", Type: session.EventTypeAssistant})
	runner.finish()
	var sourceCount int
	for event, err := range runner.SourceEvents() {
		if err != nil {
			t.Fatalf("SourceEvents() error = %v", err)
		}
		if event.Canonical != nil {
			sourceCount++
		}
	}
	if sourceCount != 1 {
		t.Fatalf("SourceEvents() count = %d, want 1", sourceCount)
	}
	var competingErr error
	for _, err := range runner.Events() {
		competingErr = err
	}
	if !errors.Is(competingErr, ErrEventStreamConsumed) {
		t.Fatalf("Events() error = %v, want ErrEventStreamConsumed", competingErr)
	}
}

func TestRunnerCloseCancelsAndDiscardsUndrainedEvents(t *testing.T) {
	t.Parallel()

	cancelled := make(chan struct{}, 1)
	runner := newRunner("run-1", func() { cancelled <- struct{}{} })
	runner.publishEvent(&session.Event{ID: "event-1", Type: session.EventTypeAssistant})
	if err := runner.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("Close() did not cancel the active execution")
	}
	var count int
	for range runner.Events() {
		count++
	}
	if count != 0 {
		t.Fatalf("Events() count after Close = %d, want discarded queue", count)
	}
	if err := runner.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestRunnerEventsDoesNotBlockOnUndrainedSourceEvents(t *testing.T) {
	t.Parallel()

	runner := newRunner("run-1", func() {})
	done := make(chan int, 1)
	go func() {
		count := 0
		for event, err := range runner.Events() {
			if err != nil {
				continue
			}
			if event != nil {
				count++
			}
		}
		done <- count
	}()

	published := make(chan struct{})
	go func() {
		defer close(published)
		for i := 0; i < 128; i++ {
			runner.publishEvent(&session.Event{ID: "event", Type: session.EventTypeAssistant})
		}
		runner.finish()
	}()

	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("publishEvent blocked while only Events was drained")
	}
	select {
	case count := <-done:
		if count != 128 {
			t.Fatalf("Events received %d events, want 128", count)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Events to close")
	}
}

func TestRunnerPublishDoesNotBlockBeforeAnyStreamIsDrained(t *testing.T) {
	t.Parallel()

	runner := newRunner("run-1", func() {})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 128; i++ {
			runner.publishEvent(&session.Event{ID: "event", Type: session.EventTypeAssistant})
		}
		runner.publishError(context.Canceled)
		runner.finish()
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked before a stream was selected")
	}
}
