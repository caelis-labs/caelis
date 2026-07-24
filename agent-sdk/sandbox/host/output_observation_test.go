//go:build !windows

package host

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestSessionAwaitOutputPublishesAfterCallback(t *testing.T) {
	rt, err := New(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	var callbackCursor int64
	session, err := rt.Start(context.Background(), sandbox.CommandRequest{
		Command: "printf x; sleep 0.2",
		OnOutput: func(chunk sandbox.OutputChunk) {
			if chunk.Stream == "stdout" && chunk.Text == "x" {
				callbackCursor = chunk.Cursor
				close(callbackStarted)
				<-releaseCallback
			}
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Terminate(context.Background()) })

	<-callbackStarted
	if callbackCursor != 1 {
		t.Fatalf("callback cursor = %d, want stdout marker 1", callbackCursor)
	}
	result := make(chan sandbox.OutputObservation, 1)
	go func() {
		observation, _ := session.AwaitOutput(context.Background(), sandbox.OutputCursor{})
		result <- observation
	}()
	select {
	case observation := <-result:
		t.Fatalf("AwaitOutput() returned before callback completed: %+v", observation)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseCallback)
	select {
	case observation := <-result:
		if observation.Cursor.Stdout != 1 {
			t.Fatalf("AwaitOutput().Cursor = %+v, want stdout 1", observation.Cursor)
		}
	case <-time.After(time.Second):
		t.Fatal("AwaitOutput() did not return after callback completed")
	}
}

func TestSessionAwaitOutputTerminalWaitsForDecoderTailCallback(t *testing.T) {
	rt, err := New(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	tailStarted := make(chan struct{})
	releaseTail := make(chan struct{})
	var tailOnce sync.Once
	session, err := rt.Start(context.Background(), sandbox.CommandRequest{
		Command: "printf '\\344\\270'",
		OnOutput: func(chunk sandbox.OutputChunk) {
			if chunk.Stream == "stdout" && strings.Contains(chunk.Text, "\uFFFD") {
				tailOnce.Do(func() { close(tailStarted) })
				<-releaseTail
			}
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Terminate(context.Background()) })

	progress, err := session.AwaitOutput(context.Background(), sandbox.OutputCursor{})
	if err != nil {
		t.Fatalf("AwaitOutput(progress) error = %v", err)
	}
	if progress.Cursor.Stdout != 2 || !progress.Status.Running {
		t.Fatalf("progress observation = %+v, want two raw bytes while running", progress)
	}
	<-tailStarted

	terminal := make(chan sandbox.OutputObservation, 1)
	go func() {
		observation, _ := session.AwaitOutput(context.Background(), progress.Cursor)
		terminal <- observation
	}()
	select {
	case observation := <-terminal:
		t.Fatalf("terminal published before decoder tail callback completed: %+v", observation)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseTail)
	select {
	case observation := <-terminal:
		if observation.Status.Running || observation.Cursor != progress.Cursor {
			t.Fatalf("terminal observation = %+v, want final raw cursor", observation)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal observation was not published")
	}
}

func TestSessionAwaitOutputCancellationIsObserverLocal(t *testing.T) {
	rt, err := New(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	session, err := rt.Start(context.Background(), commandRequest("sleep 0.1; printf done"))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Terminate(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := session.AwaitOutput(ctx, sandbox.OutputCursor{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AwaitOutput(cancelled) error = %v, want deadline exceeded", err)
	}
	status, err := session.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Running {
		t.Fatal("cancelling AwaitOutput terminated the host process")
	}

	observation, err := session.AwaitOutput(context.Background(), sandbox.OutputCursor{})
	if err != nil {
		t.Fatalf("AwaitOutput(after cancellation) error = %v", err)
	}
	if observation.Cursor.Stdout != int64(len("done")) {
		t.Fatalf("observation.Cursor = %+v, want stdout %d", observation.Cursor, len("done"))
	}
}

func TestSessionAwaitOutputDoesNotRegressBlockedSiblingCursor(t *testing.T) {
	stdoutCallback := make(chan struct{})
	releaseStdout := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseStdout) })
	session := &hostSession{
		running:      true,
		outputSignal: make(chan struct{}),
		onOutput: func(chunk sandbox.OutputChunk) {
			if chunk.Stream == "stdout" && chunk.Text != "" {
				close(stdoutCallback)
				<-releaseStdout
			}
		},
	}
	session.wg.Add(2)
	go session.readStream(bytes.NewReader([]byte("x")), "stdout")
	<-stdoutCallback
	go session.readStream(bytes.NewReader([]byte("e")), "stderr")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observation, err := session.AwaitOutput(ctx, sandbox.OutputCursor{Stdout: 1})
	if err != nil {
		t.Fatalf("AwaitOutput() error = %v", err)
	}
	if observation.Cursor != (sandbox.OutputCursor{Stdout: 1, Stderr: 1}) {
		t.Fatalf("AwaitOutput().Cursor = %+v, want monotonic stdout 1/stderr 1", observation.Cursor)
	}

	releaseOnce.Do(func() { close(releaseStdout) })
	session.wg.Wait()
}
