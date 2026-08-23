package appserver

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const defaultSlowApprovalRecoveryPhase = time.Second

type approvalRecoveryDiagnostics struct {
	logger *slog.Logger
	now    func() time.Time
	slow   time.Duration
}

func newApprovalRecoveryDiagnostics(logger *slog.Logger) *approvalRecoveryDiagnostics {
	if logger == nil {
		return nil
	}
	return &approvalRecoveryDiagnostics{logger: logger, now: time.Now, slow: defaultSlowApprovalRecoveryPhase}
}

func (d *approvalRecoveryDiagnostics) started() time.Time {
	if d == nil || d.now == nil {
		return time.Time{}
	}
	return d.now()
}

func (d *approvalRecoveryDiagnostics) observe(ctx context.Context, phase string, started time.Time, err error) {
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
	switch {
	case errors.Is(err, session.ErrFenceConflict):
		outcome = "fence_conflict"
	case errors.Is(err, context.DeadlineExceeded):
		outcome = "deadline"
	case errors.Is(err, context.Canceled):
		outcome = "canceled"
	case err != nil:
		outcome = "error"
	}
	d.logger.LogAttrs(ctx, slog.LevelWarn, "approval recovery phase",
		slog.String("component", "session_fence"),
		slog.String("phase", phase),
		slog.Int64("elapsed_ms", elapsed.Milliseconds()),
		slog.String("outcome", outcome),
	)
}
