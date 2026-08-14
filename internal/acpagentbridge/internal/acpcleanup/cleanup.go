// Package acpcleanup provides bounded, cancellation-detached cleanup for ACP
// sessions and their backing client processes.
package acpcleanup

import (
	"context"
	"strings"
	"time"
)

// DefaultTimeout bounds one best-effort ACP cleanup operation.
const DefaultTimeout = 3 * time.Second

type sessionCloser interface {
	CloseSession(context.Context, string) error
}

type clientCloser interface {
	Close(context.Context) error
}

// CloseSession closes one ACP session using the standard cleanup timeout.
func CloseSession(parent context.Context, closer sessionCloser, sessionID string) error {
	return CloseSessionWithin(parent, closer, sessionID, DefaultTimeout)
}

// CloseSessionWithin closes one ACP session independently of parent
// cancellation, while still bounding an unresponsive session/close RPC.
func CloseSessionWithin(parent context.Context, closer sessionCloser, sessionID string, timeout time.Duration) error {
	if closer == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	ctx, cancel := cleanupContext(parent, timeout)
	defer cancel()
	return closer.CloseSession(ctx, strings.TrimSpace(sessionID))
}

// CloseClient closes one ACP client process using the standard cleanup timeout.
func CloseClient(parent context.Context, closer clientCloser) error {
	return CloseClientWithin(parent, closer, DefaultTimeout)
}

// CloseClientWithin closes one ACP client process independently of parent
// cancellation, while still bounding an unresponsive process shutdown.
func CloseClientWithin(parent context.Context, closer clientCloser, timeout time.Duration) error {
	if closer == nil {
		return nil
	}
	ctx, cancel := cleanupContext(parent, timeout)
	defer cancel()
	return closer.Close(ctx)
}

func cleanupContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}
