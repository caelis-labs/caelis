package runtime

import (
	"context"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
)

// ValidateRevocation holds a captured RevocationRevision through a short
// checkpoint commit. Contention or changed authority rejects the candidate;
// this does not grant permission, admit a request or replace per-call checks.
// The caller must release on every path, acquire other owner guards in a stable
// order, and not call grant-store methods while holding this guard. Revocation
// and dispatch linearize outside this critical section, without waiting for
// generation or a future compact. A nil store represents no persistent grants.
func (s *MCPGrantStore) ValidateRevocation(ctx context.Context, revision uint64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		if revision != 0 {
			return nil, agent.ErrModelRequestSnapshotStale
		}
		return func() {}, nil
	}
	if !s.mu.TryLock() {
		return nil, agent.ErrModelRequestSnapshotStale
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if revision != s.revocationRevisionLocked() {
		s.mu.Unlock()
		return nil, agent.ErrModelRequestSnapshotStale
	}
	return sync.OnceFunc(s.mu.Unlock), nil
}
