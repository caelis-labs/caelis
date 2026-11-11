package sessionprofile

import (
	"context"
	"sync"
	"time"
)

// ValidateConfiguration holds the exact active profile revision through a short
// checkpoint append. Callers must not reenter Store methods until release. It
// neither admits a model request nor writes LastRequest. Contention is stale.
func (s *Store) ValidateConfiguration(ctx context.Context, scope Scope, session string, revision uint64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.mu.TryLock() {
		return nil, ErrConfigurationStale
	}
	return s.validateLockedConfiguration(ctx, scope, session, revision)
}

// GuardConfiguration waits for the same configuration guard used by
// ValidateConfiguration. Host dispatch cannot resolve and replay a stale model
// request after an effect, so ordinary local Store contention must not be
// mistaken for a changed revision. Waiting is cancellable and grants no lease.
func (s *Store) GuardConfiguration(ctx context.Context, scope Scope, session string, revision uint64) (func(), error) {
	for !s.mu.TryLock() {
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return s.validateLockedConfiguration(ctx, scope, session, revision)
}

func (s *Store) validateLockedConfiguration(ctx context.Context, scope Scope, session string, revision uint64) (func(), error) {
	release := sync.OnceFunc(s.mu.Unlock)
	fail := func(err error) (func(), error) { release(); return nil, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := s.checkScope(ctx, scope); err != nil {
		return fail(err)
	}
	binding, err := s.binding(ctx, scope, session)
	if err != nil {
		return fail(err)
	}
	if binding.Archived {
		return fail(ErrRevoked)
	}
	current, err := s.configuration(ctx, session, 0)
	if err != nil {
		return fail(err)
	}
	if current.Revision != revision {
		return fail(ErrConfigurationStale)
	}
	return release, nil
}
