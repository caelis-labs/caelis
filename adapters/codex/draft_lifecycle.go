package codex

import "context"

// routeClosed handles a failed native projection while the draft Session is
// still owned. Explicit Session close and setup rollback remove ownership first
// and retain their normal RPC responses. Stable ACP does not use this hook.
func (d *draftAgent) routeClosed(route *sessionRoute, cause error) {
	d.core.mu.Lock()
	owned := d.core.sessions[route.state.threadID] == route.state
	d.core.mu.Unlock()
	// alpha.8 has no agent-initiated Session close notification. Its idle(error)
	// means the foreground activity ended, which a lost projection cannot prove.
	// Close this transport promptly: accepted receipts remain facts, but their
	// completion is unknown. The shared backend and other transports stay alive.
	if owned {
		_ = d.connection.Close()
	}
	d.mu.Lock()
	session := d.sessions[route.state.threadID]
	d.mu.Unlock()
	if session != nil {
		session.mu.Lock()
		session.cancelRequested = true
		if pending := session.pending; pending != nil && pending.nativeCancel != nil {
			pending.nativeCancel(cause)
		}
		for _, permission := range session.permissionCancels {
			permission.cancel()
		}
		session.mu.Unlock()
	}
	if !owned {
		return
	}
	// An interrupt acknowledgement is not proof of completion. It only stops
	// further work on the actual known native Turn before ordinary unsubscribe.
	ctx, cancel := context.WithTimeout(context.Background(), codexThreadUnsubscribeTimeout)
	defer cancel()
	_ = d.core.interrupt(ctx, route.state)
}
