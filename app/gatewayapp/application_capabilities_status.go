package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// applicationMCPStatus observes an already resident activation. Status reads
// must never activate a Session, start a service, or borrow another Session's
// manager. The Application Store checks connection ownership before this call.
func (s *Stack) applicationMCPStatus(_ context.Context, sessionID string, revision uint64) sessionprofile.MCPStatus {
	if s == nil || s.sessionRuntimes == nil {
		return sessionprofile.MCPStatus{}
	}
	registry := s.sessionRuntimes
	registry.mu.RLock()
	active := registry.sessions[sessionID]
	if active == nil || active.releasing || active.instance == nil {
		registry.mu.RUnlock()
		return sessionprofile.MCPStatus{}
	}
	composition := &active.instance.runtimeComposition
	registry.mu.RUnlock()
	composition.mu.RLock()
	read := composition.capabilityStatus
	composition.mu.RUnlock()
	if read == nil {
		return sessionprofile.MCPStatus{}
	}
	return read(revision)
}

func (s *Stack) applicationConfigurationCommitted(_ context.Context, sessionID string, configuration sessionprofile.Configuration) {
	if s == nil || s.sessionRuntimes == nil {
		return
	}
	registry := s.sessionRuntimes
	registry.mu.RLock()
	active := registry.sessions[sessionID]
	if active == nil || active.releasing || active.instance == nil {
		registry.mu.RUnlock()
		return
	}
	composition := &active.instance.runtimeComposition
	registry.mu.RUnlock()
	composition.mu.RLock()
	update := composition.capabilityUpdated
	composition.mu.RUnlock()
	if update != nil {
		update(configuration)
	}
}
