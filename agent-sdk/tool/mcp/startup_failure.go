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
	Stage string
	Err   error
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

func startupFailureSummary(err error) string {
	stage := "startup"
	var failure *startupError
	if errors.As(err, &failure) {
		switch failure.Stage {
		case "transport", "initialize", "tools/list":
			stage = failure.Stage
		}
	}
	reason := "failed"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		reason = "deadline exceeded"
	case errors.Is(err, context.Canceled):
		reason = "canceled"
	}
	// Never expose arbitrary cause text through the public MCP status.
	return stage + ": " + reason
}
