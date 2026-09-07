package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestTUIProgramOptionsOnlyAddsFPSWhenConfigured(t *testing.T) {
	defaultOptions := tuiProgramOptions(strings.NewReader(""), io.Discard, context.Background(), 0)
	if got := len(defaultOptions); got != 3 {
		t.Fatalf("default program options = %d, want 3", got)
	}

	configuredOptions := tuiProgramOptions(strings.NewReader(""), io.Discard, context.Background(), 30)
	if got := len(configuredOptions); got != 4 {
		t.Fatalf("configured program options = %d, want 4", got)
	}
}

type recordingTUISandboxRefresher struct {
	err   error
	calls int
	done  chan struct{}
}

func (r *recordingTUISandboxRefresher) RefreshSandbox(context.Context) error {
	r.calls++
	if r.done != nil {
		close(r.done)
	}
	return r.err
}

func TestRunTUISandboxRefreshInvokesHostAndDiscardsStartupFailure(t *testing.T) {
	refresher := &recordingTUISandboxRefresher{
		err: errors.New("impl/sandbox/windows: refresh sandbox: Access is denied"),
	}
	runTUISandboxRefresh(context.Background(), refresher)
	if refresher.calls != 1 {
		t.Fatalf("RefreshSandbox calls = %d, want 1", refresher.calls)
	}
}

func TestStartTUISandboxRefreshRunsInBackground(t *testing.T) {
	refresher := &recordingTUISandboxRefresher{
		err:  errors.New("impl/sandbox/windows: refresh sandbox: Access is denied"),
		done: make(chan struct{}),
	}
	startTUISandboxRefresh(context.Background(), refresher)
	select {
	case <-refresher.done:
	case <-time.After(2 * time.Second):
		t.Fatal("startup sandbox refresh was not invoked")
	}
	if refresher.calls != 1 {
		t.Fatalf("RefreshSandbox calls = %d, want 1", refresher.calls)
	}
}

func TestRunTUISandboxRefreshIgnoresCanceledAndEmptyService(t *testing.T) {
	runTUISandboxRefresh(context.Background(), nil)

	refresher := &recordingTUISandboxRefresher{err: context.Canceled}
	runTUISandboxRefresh(context.Background(), refresher)
	if refresher.calls != 1 {
		t.Fatalf("RefreshSandbox calls = %d, want 1", refresher.calls)
	}
}
