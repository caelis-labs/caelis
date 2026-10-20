package runtime

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type mcpRevocationEpoch struct {
	alwaysKey, sessionKey string
	always, session       uint64
}

func (s *MCPGrantStore) revocationEpoch(g MCPGrant, sessionID string) mcpRevocationEpoch {
	epoch := mcpRevocationEpoch{}
	if s == nil {
		return epoch
	}
	epoch.alwaysKey, _ = g.key("always", "")
	epoch.sessionKey, _ = g.key("session", sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	epoch.always = s.revocations[epoch.alwaysKey]
	epoch.session = s.revocations[epoch.sessionKey]
	return epoch
}
func (s *MCPGrantStore) epochCurrentLocked(epoch mcpRevocationEpoch) bool {
	return s.revocations[epoch.alwaysKey] == epoch.always && s.revocations[epoch.sessionKey] == epoch.session
}
func (s *MCPGrantStore) checkEpoch(epoch mcpRevocationEpoch) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.epochCurrentLocked(epoch) {
		return errors.New("MCP grant revoked while call was pending")
	}
	return nil
}
func (s *MCPGrantStore) grantAtEpoch(g MCPGrant, scope, sessionID string, epoch mcpRevocationEpoch) error {
	if s == nil {
		return errors.New("MCP grant store is unavailable")
	}
	key, err := g.key(scope, sessionID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.epochCurrentLocked(epoch) {
		return errors.New("MCP approval superseded by revocation")
	}
	return s.change(key, true)
}

// The last check runs after all source and per-call approvals. It orders
// revocation against dispatch, without claiming to undo an in-flight effect.
type mcpRevocationTool struct {
	tool.Tool
	store *MCPGrantStore
	epoch mcpRevocationEpoch
}

func (t mcpRevocationTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if err := t.store.checkEpoch(t.epoch); err != nil {
		return tool.Result{}, err
	}
	return t.Tool.Call(ctx, call)
}
