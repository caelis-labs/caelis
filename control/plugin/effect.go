package plugin

import "context"

// EffectReport records whether an external install/update effect started.
// Started is monotonic for one operation: once true, callers must not treat
// later configuration CAS failures as proof that no effect occurred.
type EffectReport struct {
	Started bool
}

type effectReportKey struct{}

// withEffectReport binds a mutable effect report into ctx so deep install paths
// can mark durable external work without changing every intermediate signature.
func withEffectReport(ctx context.Context) (context.Context, *EffectReport) {
	if ctx == nil {
		ctx = context.Background()
	}
	report := &EffectReport{}
	return context.WithValue(ctx, effectReportKey{}, report), report
}

// markEffectStarted records that a durable external cache/install effect began.
func markEffectStarted(ctx context.Context) {
	if ctx == nil {
		return
	}
	if report, ok := ctx.Value(effectReportKey{}).(*EffectReport); ok && report != nil {
		report.Started = true
	}
}

func effectReportFrom(report *EffectReport) EffectReport {
	if report == nil {
		return EffectReport{}
	}
	return *report
}
