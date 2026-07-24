//go:build !windows

package cmdsession

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestAwaitOutputPublishesAfterOutputCallback(t *testing.T) {
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	session := startObservationSession(t, "printf x; sleep 0.2", func(chunk AsyncOutputChunk) {
		if chunk.Final || len(chunk.Data) == 0 {
			return
		}
		close(callbackStarted)
		<-releaseCallback
	})

	<-callbackStarted
	_, _, readCursor, _ := session.ReadOutput(0, 0)
	publishedCtx, cancelPublished := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelPublished()
	if _, err := session.AwaitOutput(
		publishedCtx,
		sandbox.OutputCursor{Stdout: readCursor},
	); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AwaitOutput(ReadOutput cursor during callback) error = %v, want deadline exceeded", err)
	}

	result := make(chan OutputObservation, 1)
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

func TestAwaitOutputBroadcastsAndCancellationDoesNotTerminate(t *testing.T) {
	session := startObservationSession(t, "sleep 0.1; printf shared; sleep 0.1", nil)

	cancelCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := session.AwaitOutput(cancelCtx, sandbox.OutputCursor{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AwaitOutput(cancelled) error = %v, want deadline exceeded", err)
	}
	if session.HasExited() {
		t.Fatal("cancelled AwaitOutput terminated the process")
	}

	results := make(chan OutputObservation, 2)
	for range 2 {
		go func() {
			observation, _ := session.AwaitOutput(context.Background(), sandbox.OutputCursor{})
			results <- observation
		}()
	}
	for range 2 {
		select {
		case observation := <-results:
			if observation.Cursor.Stdout != int64(len("shared")) {
				t.Fatalf("broadcast cursor = %+v, want stdout %d", observation.Cursor, len("shared"))
			}
		case <-time.After(time.Second):
			t.Fatal("one output observer did not receive the broadcast")
		}
	}
}

func TestAwaitOutputPublishesTerminalAfterDescriptorFinalCallback(t *testing.T) {
	finalStarted := make(chan struct{})
	releaseFinal := make(chan struct{})
	var finalOnce sync.Once
	session := startObservationSession(t, "printf x", func(chunk AsyncOutputChunk) {
		if chunk.Stream == "stdout" && chunk.Final {
			finalOnce.Do(func() { close(finalStarted) })
			<-releaseFinal
		}
	})

	first, err := session.AwaitOutput(context.Background(), sandbox.OutputCursor{})
	if err != nil {
		t.Fatalf("AwaitOutput(initial) error = %v", err)
	}
	if first.Cursor.Stdout != 1 || first.Status.State != SessionStateRunning {
		t.Fatalf("initial observation = %+v, want stdout progress while running", first)
	}
	<-finalStarted

	terminal := make(chan OutputObservation, 1)
	go func() {
		observation, _ := session.AwaitOutput(context.Background(), first.Cursor)
		terminal <- observation
	}()
	select {
	case observation := <-terminal:
		t.Fatalf("terminal published before final callback completed: %+v", observation)
	case <-time.After(30 * time.Millisecond):
	}

	close(releaseFinal)
	select {
	case observation := <-terminal:
		if observation.Status.State == SessionStateRunning {
			t.Fatalf("terminal observation = %+v, want non-running status", observation)
		}
		if observation.Cursor != first.Cursor {
			t.Fatalf("terminal cursor = %+v, want %+v", observation.Cursor, first.Cursor)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal observation was not published")
	}
}

func TestAwaitOutputRejectsCursorAhead(t *testing.T) {
	session := NewAsyncSession(AsyncSessionConfig{})
	_, err := session.AwaitOutput(context.Background(), sandbox.OutputCursor{Stdout: 1})
	var ahead *sandbox.OutputCursorAheadError
	if !errors.As(err, &ahead) {
		t.Fatalf("AwaitOutput() error = %v, want OutputCursorAheadError", err)
	}
	if ahead.Stream != "stdout" || ahead.Requested != 1 || ahead.Available != 0 {
		t.Fatalf("cursor error = %+v", ahead)
	}
}

func TestAwaitOutputDoesNotRegressBlockedSiblingCursor(t *testing.T) {
	stdoutCallback := make(chan struct{})
	releaseStdout := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseStdout) })
	session := NewAsyncSession(AsyncSessionConfig{
		OnOutput: func(chunk AsyncOutputChunk) {
			if chunk.Stream == "stdout" && len(chunk.Data) > 0 {
				close(stdoutCallback)
				<-releaseStdout
			}
		},
	})
	session.readersWg.Add(2)
	go session.readOutput(bytes.NewReader([]byte("x")), "stdout", session.stdoutBuffer)
	<-stdoutCallback
	go session.readOutput(bytes.NewReader([]byte("e")), "stderr", session.stderrBuffer)

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
	session.readersWg.Wait()
}

func startObservationSession(t *testing.T, command string, onOutput func(AsyncOutputChunk)) *AsyncSession {
	t.Helper()
	session := NewAsyncSession(AsyncSessionConfig{
		Command:  command,
		OnOutput: onOutput,
		BuildCommand: func(ctx context.Context, cfg AsyncSessionConfig) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "sh", "-c", cfg.Command), nil
		},
	})
	if err := session.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
