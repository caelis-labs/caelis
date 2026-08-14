package acpcleanup

import (
	"context"
	"errors"
	"testing"
	"time"
)

type blockingCleanupClient struct {
	entered chan context.Context
}

func (c blockingCleanupClient) CloseSession(ctx context.Context, _ string) error {
	c.entered <- ctx
	<-ctx.Done()
	return ctx.Err()
}

func (c blockingCleanupClient) Close(ctx context.Context) error {
	c.entered <- ctx
	<-ctx.Done()
	return ctx.Err()
}

func TestCleanupDetachesCancellationAndBoundsOperations(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name string
		run  func(blockingCleanupClient) error
	}{
		{name: "session", run: func(client blockingCleanupClient) error {
			return CloseSessionWithin(parent, client, "temporary", 20*time.Millisecond)
		}},
		{name: "client", run: func(client blockingCleanupClient) error {
			return CloseClientWithin(parent, client, 20*time.Millisecond)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entered := make(chan context.Context, 1)
			started := time.Now()
			err := test.run(blockingCleanupClient{entered: entered})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cleanup error = %v, want deadline exceeded", err)
			}
			cleanupCtx := <-entered
			if cause := context.Cause(cleanupCtx); !errors.Is(cause, context.DeadlineExceeded) {
				t.Fatalf("cleanup context cause = %v, want its own deadline", cause)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("cleanup elapsed = %v, want bounded operation", elapsed)
			}
		})
	}
}
