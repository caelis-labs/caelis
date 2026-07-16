package controladapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

const acpDiscoveryCacheTTL = 2 * time.Minute

type acpDiscoveryCacheEntry struct {
	request  controlagents.ConnectRequest
	snapshot controlagents.DiscoverySnapshot
	cachedAt time.Time
}

// DiscoverACPConnection probes one temporary empty session for the guided
// /connect flow without adding the endpoint to the user roster.
func (d *Adapter) DiscoverACPConnection(ctx context.Context, req controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error) {
	if d == nil || d.stack == nil || d.stack.Agent.DiscoverConnectionFn == nil {
		return controlagents.DiscoverySnapshot{}, missingRuntimeDependency("ACP agent discovery")
	}
	if strings.TrimSpace(req.CWD) == "" {
		req.CWD = d.WorkspaceDir()
	}
	key := acpDiscoveryRequestKey(req)
	now := time.Now()
	d.mu.Lock()
	for cachedKey, entry := range d.acpDiscoveries {
		if acpDiscoveryCacheExpired(entry, now) {
			delete(d.acpDiscoveries, cachedKey)
		}
	}
	if cached, ok := d.acpDiscoveries[key]; ok {
		d.mu.Unlock()
		return controlagents.NormalizeDiscoverySnapshot(cached.snapshot), nil
	}
	d.mu.Unlock()
	snapshot, err := d.stack.Agent.DiscoverConnectionFn(ctx, req)
	if err != nil {
		return controlagents.DiscoverySnapshot{}, err
	}
	snapshot = controlagents.NormalizeDiscoverySnapshot(snapshot)
	d.mu.Lock()
	if d.acpDiscoveries == nil {
		d.acpDiscoveries = map[string]acpDiscoveryCacheEntry{}
	}
	d.acpDiscoveries[key] = acpDiscoveryCacheEntry{
		request: controlagents.NormalizeConnectRequest(req), snapshot: snapshot, cachedAt: time.Now(),
	}
	d.mu.Unlock()
	return snapshot, nil
}

// ConnectACP validates and persists the explicitly selected remote model as a
// stable user-addressable Agent.
func (d *Adapter) ConnectACP(ctx context.Context, req controlagents.ConnectRequest) (controlagents.ConnectResult, error) {
	if d == nil || d.stack == nil || d.stack.Agent.ConnectFn == nil {
		return controlagents.ConnectResult{}, missingRuntimeDependency("ACP agent connect")
	}
	if strings.TrimSpace(req.CWD) == "" {
		req.CWD = d.WorkspaceDir()
	}
	key := acpDiscoveryRequestKey(req)
	now := time.Now()
	d.mu.Lock()
	if cached, ok := d.acpDiscoveries[key]; ok {
		if acpDiscoveryCacheExpired(cached, now) {
			delete(d.acpDiscoveries, key)
		} else {
			snapshot := controlagents.NormalizeDiscoverySnapshot(cached.snapshot)
			req.Discovery = &snapshot
		}
	}
	d.mu.Unlock()
	result, err := d.stack.Agent.ConnectFn(ctx, req)
	if err != nil {
		return controlagents.ConnectResult{}, err
	}
	if len(result.Agents) == 0 {
		return controlagents.ConnectResult{}, fmt.Errorf("app/gatewayapp/controladapter: ACP connect returned no Agents")
	}
	d.mu.Lock()
	endpointKey := acpDiscoveryEndpointKey(req)
	for cachedKey, entry := range d.acpDiscoveries {
		if acpDiscoveryEndpointKey(entry.request) == endpointKey {
			delete(d.acpDiscoveries, cachedKey)
		}
	}
	d.mu.Unlock()
	return result, nil
}

// DisconnectCandidates lists only user-configured external ACP Agents.
func (d *Adapter) DisconnectCandidates(ctx context.Context) ([]controlagents.DisconnectCandidate, error) {
	if d == nil || d.stack == nil || d.stack.Agent.DisconnectCandidatesFn == nil {
		return nil, missingRuntimeDependency("ACP Agent disconnect candidates")
	}
	return d.stack.Agent.DisconnectCandidatesFn(ctx)
}

// DisconnectACP removes one external roster Agent without uninstalling its ACP
// adapter. If it releases the final Connection reference, endpoint-scoped
// discovery completions are invalidated as well.
func (d *Adapter) DisconnectACP(ctx context.Context, agentID string) (controlagents.DisconnectResult, error) {
	if d == nil || d.stack == nil || d.stack.Agent.DisconnectFn == nil {
		return controlagents.DisconnectResult{}, missingRuntimeDependency("ACP Agent disconnect")
	}
	result, err := d.stack.Agent.DisconnectFn(ctx, agentID)
	if err != nil {
		var inUse *controlagents.AgentInUseError
		if errors.As(err, &inUse) && d.hasSession && strings.TrimSpace(inUse.SessionID) == strings.TrimSpace(d.session.SessionID) {
			return controlagents.DisconnectResult{}, fmt.Errorf(
				"app/gatewayapp/controladapter: Agent %q currently controls this task; run /lead local before disconnecting it: %w",
				strings.TrimSpace(inUse.AgentID),
				err,
			)
		}
		return controlagents.DisconnectResult{}, err
	}
	if result.ConnectionRemoved {
		d.mu.Lock()
		for key, entry := range d.acpDiscoveries {
			if strings.EqualFold(strings.TrimSpace(entry.snapshot.ConnectionID), strings.TrimSpace(result.ConnectionID)) {
				delete(d.acpDiscoveries, key)
			}
		}
		d.mu.Unlock()
	}
	return result, nil
}

func acpDiscoveryEndpointKey(req controlagents.ConnectRequest) string {
	req.ModelID = ""
	return acpDiscoveryRequestKey(req)
}

func acpDiscoveryCacheExpired(entry acpDiscoveryCacheEntry, now time.Time) bool {
	return entry.cachedAt.IsZero() || now.Sub(entry.cachedAt) > acpDiscoveryCacheTTL
}

func acpDiscoveryRequestKey(req controlagents.ConnectRequest) string {
	req = controlagents.NormalizeConnectRequest(req)
	req.ConfigValues = nil
	req.Discovery = nil
	payload, _ := json.Marshal(req)
	return string(payload)
}
