package controlplane

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const defaultSlowFencePhase = time.Second

// fencePhaseDiagnostics records only fixed classifications and durations. It
// deliberately excludes Session, workspace, model, prompt, and path values.
type fencePhaseDiagnostics struct {
	logger *slog.Logger
	now    func() time.Time
	slow   time.Duration
}

func newFencePhaseDiagnostics(logger *slog.Logger) *fencePhaseDiagnostics {
	if logger == nil {
		return nil
	}
	return &fencePhaseDiagnostics{logger: logger, now: time.Now, slow: defaultSlowFencePhase}
}

func (d *fencePhaseDiagnostics) started() time.Time {
	if d == nil || d.now == nil {
		return time.Time{}
	}
	return d.now()
}

func (d *fencePhaseDiagnostics) observe(ctx context.Context, phase string, started time.Time, err error) {
	if d == nil || d.logger == nil || d.now == nil || started.IsZero() {
		return
	}
	elapsed := d.now().Sub(started)
	if elapsed < 0 {
		elapsed = 0
	}
	if err == nil && elapsed < d.slow {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	outcome := "slow"
	if err != nil {
		outcome = fencePhaseErrorClass(err)
	}
	d.logger.LogAttrs(ctx, slog.LevelWarn, "session fence phase",
		slog.String("component", "session_fence"),
		slog.String("phase", phase),
		slog.Int64("elapsed_ms", elapsed.Milliseconds()),
		slog.String("outcome", outcome),
	)
}

func fencePhaseErrorClass(err error) string {
	switch {
	case errors.Is(err, session.ErrFenceConflict):
		return "fence_conflict"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "error"
	}
}
