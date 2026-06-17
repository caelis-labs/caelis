package local

import (
	"context"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/session"
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
