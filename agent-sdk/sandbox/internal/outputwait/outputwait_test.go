package outputwait

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestAwaitReturnsPublishedProgressMonotonically(t *testing.T) {
	observation, err := Await(context.Background(), sandbox.OutputCursor{Stdout: 3}, func() Snapshot[string] {
		return Snapshot[string]{
			Published: sandbox.OutputCursor{Stdout: 1, Stderr: 2},
			Available: sandbox.OutputCursor{Stdout: 3, Stderr: 2},
			Status:    "running",
		}
	})
	if err != nil {
		t.Fatalf("Await() error = %v", err)
	}
	if observation.Cursor != (sandbox.OutputCursor{Stdout: 3, Stderr: 2}) {
		t.Fatalf("Await().Cursor = %+v, want monotonic stdout 3/stderr 2", observation.Cursor)
	}
}

func TestAwaitRetriesRacingSnapshot(t *testing.T) {
	attempts := 0
	observation, err := Await(context.Background(), sandbox.OutputCursor{}, func() Snapshot[string] {
		attempts++
		if attempts == 1 {
			return Snapshot[string]{Retry: true}
		}
		return Snapshot[string]{
			Published: sandbox.OutputCursor{Stdout: 4},
			Available: sandbox.OutputCursor{Stdout: 4},
			Terminal:  true,
			Status:    "completed",
		}
	})
	if err != nil {
		t.Fatalf("Await() error = %v", err)
	}
	if attempts != 2 || observation.Status != "completed" {
		t.Fatalf("Await() attempts/status = %d/%q, want 2/completed", attempts, observation.Status)
	}
}

func TestAwaitCancellationDoesNotRequireSignal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := Await(ctx, sandbox.OutputCursor{}, func() Snapshot[struct{}] {
		return Snapshot[struct{}]{
			Signal:    make(chan struct{}),
			Published: sandbox.OutputCursor{},
			Available: sandbox.OutputCursor{},
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Await() error = %v, want deadline exceeded", err)
	}
}
