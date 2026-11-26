package executil

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestAwaitOutputReturnsPublishedProgressMonotonically(t *testing.T) {
	t.Parallel()
	observation, err := AwaitOutput(context.Background(), sandbox.OutputCursor{Stdout: 3}, func() OutputSnapshot[string] {
		return OutputSnapshot[string]{
			Published: sandbox.OutputCursor{Stdout: 1, Stderr: 2},
			Available: sandbox.OutputCursor{Stdout: 3, Stderr: 2},
			Status:    "running",
		}
	})
	if err != nil {
		t.Fatalf("AwaitOutput() error = %v", err)
	}
	if observation.Cursor != (sandbox.OutputCursor{Stdout: 3, Stderr: 2}) {
		t.Fatalf("AwaitOutput().Cursor = %+v, want monotonic stdout 3/stderr 2", observation.Cursor)
	}
}

func TestAwaitOutputRetriesRacingSnapshot(t *testing.T) {
	t.Parallel()
	attempts := 0
	observation, err := AwaitOutput(context.Background(), sandbox.OutputCursor{}, func() OutputSnapshot[string] {
		attempts++
		if attempts == 1 {
			return OutputSnapshot[string]{Retry: true}
		}
		return OutputSnapshot[string]{
			Published: sandbox.OutputCursor{Stdout: 4},
			Available: sandbox.OutputCursor{Stdout: 4},
			Terminal:  true,
			Status:    "completed",
		}
	})
	if err != nil {
		t.Fatalf("AwaitOutput() error = %v", err)
	}
	if attempts != 2 || observation.Status != "completed" {
		t.Fatalf("AwaitOutput() attempts/status = %d/%q, want 2/completed", attempts, observation.Status)
	}
}

func TestAwaitOutputCancellationDoesNotRequireSignal(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := AwaitOutput(ctx, sandbox.OutputCursor{}, func() OutputSnapshot[struct{}] {
		return OutputSnapshot[struct{}]{
			Signal:    make(chan struct{}),
			Published: sandbox.OutputCursor{},
			Available: sandbox.OutputCursor{},
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AwaitOutput() error = %v, want deadline exceeded", err)
	}
}
