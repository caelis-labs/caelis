// Package acpcleanup provides cancellation-detached ACP cleanup with bounded
// Session RPCs and process grace periods followed by proof-bearing tree joins.
package acpcleanup

import (
	"context"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

const (
	// DefaultSessionCloseTimeout bounds one temporary ACP session/close RPC.
	DefaultSessionCloseTimeout = 10 * time.Second
	// DefaultRuntimeExitGrace bounds latency-sensitive runtime teardown before
	// it escalates to proof-bearing forced process cleanup. Preparation passes
	// its longer process grace explicitly.
	DefaultRuntimeExitGrace = 3 * time.Second
)

type sessionCloser interface {
	CloseSession(context.Context, string) error
}

type clientCloser interface {
	Close(context.Context) error
}

// CloseSession closes one ACP session using the standard RPC timeout.
func CloseSession(parent context.Context, closer sessionCloser, sessionID string) error {
	return CloseSessionWithin(parent, closer, sessionID, DefaultSessionCloseTimeout)
}

// CloseSessionWithin closes one ACP session independently of parent
// cancellation, while still bounding an unresponsive session/close RPC.
func CloseSessionWithin(parent context.Context, closer sessionCloser, sessionID string, timeout time.Duration) error {
	if closer == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	ctx, cancel := cleanupContext(parent, timeout, DefaultSessionCloseTimeout)
	defer cancel()
	return closer.CloseSession(ctx, strings.TrimSpace(sessionID))
}

// CloseClient closes one ACP client process using the standard graceful-exit
// window.
func CloseClient(parent context.Context, closer clientCloser) error {
	return CloseClientWithGrace(parent, closer, DefaultRuntimeExitGrace)
}

// CloseClientWithGrace closes one ACP client process independently of parent
// cancellation. The context deadline bounds connection join and graceful
// process exit; an owning closer may outlive it while forcefully terminating
// and joining the process tree so its cleanup result remains provable.
func CloseClientWithGrace(parent context.Context, closer clientCloser, grace time.Duration) error {
	if closer == nil {
		return nil
	}
	ctx, cancel := cleanupContext(parent, grace, client.DefaultProcessExitGrace)
	defer cancel()
	return closer.Close(ctx)
}

func cleanupContext(parent context.Context, timeout time.Duration, fallback time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		timeout = fallback
	}
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}
