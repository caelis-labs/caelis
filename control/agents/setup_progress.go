package agents

import (
	"context"
	"strings"
	"time"
)

// SetupPhase identifies one user-visible phase of preparing a local ACP
// connection. It is progress metadata only; it is never persisted as Agent
// configuration or Session state.
type SetupPhase string

const (
	SetupPhaseChecking    SetupPhase = "checking"
	SetupPhaseWaiting     SetupPhase = "waiting"
	SetupPhaseInstalling  SetupPhase = "installing"
	SetupPhaseDownloading SetupPhase = "downloading"
	SetupPhaseVerifying   SetupPhase = "verifying"
	SetupPhaseReady       SetupPhase = "ready"
	SetupPhaseDiscovering SetupPhase = "discovering"
)

// SetupProgress describes the latest best-effort ACP setup activity. Bytes is
// the amount currently written into an isolated installation workspace, not a
// percentage or a promised download total.
type SetupProgress struct {
	AdapterID string
	Phase     SetupPhase
	Detail    string
	Bytes     int64
	Elapsed   time.Duration
}

// SetupProgressObserver receives transient local ACP setup activity.
type SetupProgressObserver func(SetupProgress)

type setupProgressContextKey struct{}

// WithSetupProgress returns a context that reports local ACP setup activity to
// observer. A nil observer leaves the context unchanged.
func WithSetupProgress(ctx context.Context, observer SetupProgressObserver) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, setupProgressContextKey{}, observer)
}

// ReportSetupProgress emits one normalized best-effort progress update.
// Observers must return promptly because setup work may report from a process
// output or heartbeat goroutine.
func ReportSetupProgress(ctx context.Context, progress SetupProgress) {
	if ctx == nil {
		return
	}
	observer, _ := ctx.Value(setupProgressContextKey{}).(SetupProgressObserver)
	if observer == nil {
		return
	}
	progress.AdapterID = strings.ToLower(strings.TrimSpace(progress.AdapterID))
	progress.Detail = strings.TrimSpace(progress.Detail)
	if progress.Bytes < 0 {
		progress.Bytes = 0
	}
	if progress.Elapsed < 0 {
		progress.Elapsed = 0
	}
	observer(progress)
}
