package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/internal/runtimeinput"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestRunnerPublicSubmitRejectsRuntimeModelContext(t *testing.T) {
	t.Parallel()

	runner := newRunner(t.Context(), "run-1", func() {}, nil)
	submission := agent.Submission{
		Kind: runtimeinput.ModelContext, Text: "hidden context",
		Actor: session.ActorRef{Kind: session.ActorKindParticipant, ID: "participant-1"},
	}
	if err := runner.Submit(submission); err == nil {
		t.Fatal("Runner.Submit(runtime model context) error = nil, want unsupported kind")
	}
	if err := runner.SubmitContext(context.Background(), submission); err == nil {
		t.Fatal("Runner.SubmitContext(runtime model context) error = nil, want unsupported kind")
	}
	if drained := runner.drainSubmissions(); len(drained) != 0 {
		t.Fatalf("public runtime model context reached queue: %#v", drained)
	}
}

func TestRunnerSubmissionDispatcherPreservesFIFOAndPerItemResults(t *testing.T) {
	t.Parallel()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstErr := errors.New("first result")
	var mu sync.Mutex
	var calls []string
	dispatcher := newRunnerSubmissionDispatcher(context.Background(), func(_ context.Context, submission agent.Submission) error {
		mu.Lock()
		calls = append(calls, submission.Text)
		mu.Unlock()
		if submission.Text == "first" {
			close(firstStarted)
			<-releaseFirst
			return firstErr
		}
		return nil
	})
	defer dispatcher.close(errors.New("test complete"))

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- dispatcher.submit(context.Background(), agent.Submission{
			Kind: agent.SubmissionKindConversation,
			Text: "first",
		})
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first submission did not start")
	}
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- dispatcher.submit(context.Background(), agent.Submission{
			Kind: agent.SubmissionKindConversation,
			Text: "second",
		})
	}()
	select {
	case err := <-secondResult:
		t.Fatalf("second submission completed before first: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstResult; !errors.Is(err, firstErr) {
		t.Fatalf("first result = %v, want item-specific error", err)
	}
	if err := <-secondResult; err != nil {
		t.Fatalf("second result = %v, want nil", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0] != "first" || calls[1] != "second" {
		t.Fatalf("submission calls = %#v, want FIFO", calls)
	}
}

func TestRunnerSubmissionDispatcherCloseCancelsActiveAndRejectsQueued(t *testing.T) {
	t.Parallel()

	activeStarted := make(chan struct{})
	dispatcher := newRunnerSubmissionDispatcher(context.Background(), func(ctx context.Context, submission agent.Submission) error {
		if submission.Text == "active" {
			close(activeStarted)
			<-ctx.Done()
			return ctx.Err()
		}
		return errors.New("queued submission reached handler")
	})
	activeResult := make(chan error, 1)
	go func() {
		activeResult <- dispatcher.submit(context.Background(), agent.Submission{
			Kind: agent.SubmissionKindConversation,
			Text: "active",
		})
	}()
	select {
	case <-activeStarted:
	case <-time.After(time.Second):
		t.Fatal("active submission did not start")
	}
	queuedResult := make(chan error, 1)
	go func() {
		queuedResult <- dispatcher.submit(context.Background(), agent.Submission{
			Kind: agent.SubmissionKindConversation,
			Text: "queued",
		})
	}()
	wantErr := errors.New("participant Turn finished")
	dispatcher.close(wantErr)
	if err := <-activeResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("active result = %v, want cancellation", err)
	}
	if err := <-queuedResult; !errors.Is(err, wantErr) {
		t.Fatalf("queued result = %v, want close error", err)
	}
}

func TestRunnerSubmissionDispatcherSkipsCanceledQueuedRequest(t *testing.T) {
	t.Parallel()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	called := make(chan string, 2)
	dispatcher := newRunnerSubmissionDispatcher(context.Background(), func(_ context.Context, submission agent.Submission) error {
		called <- submission.Text
		if submission.Text == "first" {
			close(firstStarted)
			<-releaseFirst
		}
		return nil
	})
	defer dispatcher.close(errors.New("test complete"))
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- dispatcher.submit(context.Background(), agent.Submission{Kind: agent.SubmissionKindConversation, Text: "first"})
	}()
	<-firstStarted
	ctx, cancel := context.WithCancel(context.Background())
	queuedResult := make(chan error, 1)
	go func() {
		queuedResult <- dispatcher.submit(ctx, agent.Submission{Kind: agent.SubmissionKindConversation, Text: "canceled"})
	}()
	deadline := time.Now().Add(time.Second)
	for len(dispatcher.queue) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("canceled request was not queued behind active submission")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-queuedResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued result = %v, want caller cancellation", err)
	}
	close(releaseFirst)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	if first := <-called; first != "first" {
		t.Fatalf("first handler call = %q, want first", first)
	}
	select {
	case next := <-called:
		t.Fatalf("canceled queued request reached handler as %q", next)
	case <-time.After(25 * time.Millisecond):
	}
}
