package stdio

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

const stdioProcessHelperEnvironment = "CAELIS_STDIO_PROCESS_HELPER"

func TestProcessCloseIsIdempotent(t *testing.T) {
	process := startStdioProcessHelper(t)
	if err := process.Close(context.Background()); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := process.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestProcessCloseSharesWaitAcrossConcurrentCallers(t *testing.T) {
	process := startStdioProcessHelper(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- process.Close(context.Background())
		}()
	}
	close(start)
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("concurrent Close() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent Close() did not return")
		}
	}
}

func TestProcessCloseSharesWaitAcrossCancellation(t *testing.T) {
	process := startStdioProcessHelper(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := process.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close(cancelled) error = %v, want context.Canceled", err)
	}
	if err := process.Close(context.Background()); err != nil {
		t.Fatalf("Close(after cancellation) error = %v", err)
	}
}

func startStdioProcessHelper(t *testing.T) *Process {
	t.Helper()
	process, err := Start(context.Background(), Config{
		Command:         os.Args[0],
		Args:            []string{"-test.run=^TestStdioProcessHelper$"},
		Env:             map[string]string{stdioProcessHelperEnvironment: "1"},
		ShutdownTimeout: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return process
}

func TestStdioProcessHelper(t *testing.T) {
	if os.Getenv(stdioProcessHelperEnvironment) != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}
