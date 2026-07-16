package modelconfig

import (
	"context"
	"strings"
)

// AuthProgressPhase identifies a user-visible phase of an interactive model
// provider authentication flow.
type AuthProgressPhase string

const (
	// AuthProgressOpeningBrowser indicates that Control is starting a browser
	// authorization flow.
	AuthProgressOpeningBrowser AuthProgressPhase = "opening_browser"
	// AuthProgressWaitingForBrowser indicates that the browser flow is waiting
	// for the localhost OAuth callback.
	AuthProgressWaitingForBrowser AuthProgressPhase = "waiting_for_browser"
	// AuthProgressRequestingDeviceCode indicates that Control is requesting a
	// one-time code suitable for a remote or headless environment.
	AuthProgressRequestingDeviceCode AuthProgressPhase = "requesting_device_code"
	// AuthProgressWaitingForDevice indicates that Control is waiting for the
	// user to approve the displayed one-time device code.
	AuthProgressWaitingForDevice AuthProgressPhase = "waiting_for_device"
	// AuthProgressAuthenticated indicates that authentication completed.
	AuthProgressAuthenticated AuthProgressPhase = "authenticated"
)

// AuthProgress carries presentation-neutral status for an interactive model
// provider login. VerificationURL and UserCode are intended for display to the
// user who initiated the flow; token material must never be placed here.
type AuthProgress struct {
	Provider        string
	Phase           AuthProgressPhase
	VerificationURL string
	UserCode        string
	Detail          string
}

type authProgressReporter func(AuthProgress)
type authProgressContextKey struct{}

// WithAuthProgress installs a synchronous progress reporter on ctx. Reporters
// should return quickly; presentation surfaces normally enqueue the update on
// their own event loop.
func WithAuthProgress(ctx context.Context, report func(AuthProgress)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if report == nil {
		return ctx
	}
	return context.WithValue(ctx, authProgressContextKey{}, authProgressReporter(report))
}

// ReportAuthProgress publishes one normalized authentication progress update
// when ctx carries a reporter.
func ReportAuthProgress(ctx context.Context, progress AuthProgress) {
	if ctx == nil {
		return
	}
	report, _ := ctx.Value(authProgressContextKey{}).(authProgressReporter)
	if report == nil {
		return
	}
	progress.Provider = strings.TrimSpace(progress.Provider)
	progress.VerificationURL = strings.TrimSpace(progress.VerificationURL)
	progress.UserCode = strings.TrimSpace(progress.UserCode)
	progress.Detail = strings.TrimSpace(progress.Detail)
	report(progress)
}
