package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// startupError identifies the protocol stage that failed. Err is private
// diagnostic material: it may contain command arguments, endpoints or headers.
// A stage identifies where an error was observed, not its underlying cause.
type startupError struct {
	Stage  string
	Err    error
	stdio  bool
	npm    bool
	reason string // fixed diagnostic category, never raw stderr/cause text
}

func (e *startupError) Error() string { return fmt.Sprintf("MCP %s: %v", e.Stage, e.Err) }
func (e *startupError) Unwrap() error { return e.Err }

// startupTransport observes the transport/initialize boundary without owning
// transport lifetime or changing the SDK's cancellation path.
type startupTransport struct {
	mcpsdk.Transport
	connected atomic.Bool
}

func (t *startupTransport) Connect(ctx context.Context) (mcpsdk.Connection, error) {
	conn, err := t.Transport.Connect(ctx)
	if err == nil {
		t.connected.Store(true)
	}
	return conn, err
}

// Summary returns a bounded, secret-free startup phase/category for presentation.
// The error chain remains private; an observed phase does not prove root cause.
func (f ServerFailure) Summary() string { return startupFailureSummary(f.Err) }

func (c *Client) startupFailure(stage string, err error) error {
	return &startupError{Stage: stage, Err: err, reason: c.diagnostics.reason(err)}
}

func startupFailureSummary(err error) string {
	stage := "startup"
	var failure *startupError
	if errors.As(err, &failure) {
		switch failure.Stage {
		case "connection", "transport", "initialize", "tools/list":
			stage = failure.Stage
		}
		if failure.stdio && stage == "initialize" {
			// Starting an npx process does not prove package resolution or the
			// actual MCP peer's handshake has started.
			stage = "startup/initialize"
		}
	}
	reason := "failed"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		reason = "deadline exceeded"
	case errors.Is(err, context.Canceled):
		reason = "canceled"
	}
	if reason != "canceled" && failure != nil {
		switch failure.reason {
		case "DNS lookup failed", "DNS lookup timed out", "connection refused", "network request timed out", "executable not found",
			"HTTP 401 authentication rejected", "HTTP 403 access denied", "npm authentication rejected (E401)", "npm access denied (E403)",
			"npm registry DNS failure (ENOTFOUND)", "npm registry DNS failure (EAI_AGAIN)", "npm registry connection refused (ECONNREFUSED)",
			"npm registry request timed out", "npm package resolution/download failed (E404)":
			reason = failure.reason
		default:
			if failure.npm && failure.stdio && stage == "startup/initialize" && errors.Is(err, context.DeadlineExceeded) {
				reason = "startup incomplete; check npm registry network"
			}
		}
	}
	// Never expose arbitrary cause text through the public MCP status.
	return stage + ": " + reason
}
