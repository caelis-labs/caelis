//go:build !windows

package cmdsession

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStatusReportsRunningUntilOutputCompletesAfterTerminate pins the exit
// observation window: Terminate stores SessionStateTerminated while the output
// reader is still delivering, so Status must keep reporting running until the
// output drained and the exit code settled.
func TestStatusReportsRunningUntilOutputCompletesAfterTerminate(t *testing.T) {
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCallback) }) }
	defer release()
	session := startObservationSession(t, "printf tail-output; sleep 30", func(chunk AsyncOutputChunk) {
		if chunk.Final || len(chunk.Data) == 0 {
			return
		}
		startOnce.Do(func() { close(callbackStarted) })
		<-releaseCallback
	})

	<-callbackStarted
	if err := session.Terminate(); err != nil {
		t.Fatalf("Terminate() error = %v", err)
	}
	if session.HasExited() {
		t.Fatal("HasExited() = true before the stalled output callback completed")
	}
	if status := session.Status(); status.State != SessionStateRunning {
		t.Fatalf("Status().State = %q after Terminate, want %q until output completes", status.State, SessionStateRunning)
	}

	release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := session.Wait(ctx); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	status := session.Status()
	if status.State != SessionStateTerminated {
		t.Fatalf("Status().State = %q after output completion, want %q", status.State, SessionStateTerminated)
	}
	if status.ExitCode != -1 {
		t.Fatalf("Status().ExitCode = %d, want settled signal exit code -1", status.ExitCode)
	}
	stdout, _ := session.ReadAllOutput()
	if !strings.Contains(stdout, "tail-output") {
		t.Fatalf("ReadAllOutput() stdout = %q, want tail-output", stdout)
	}
}
