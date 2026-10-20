package sessionprofile

import (
	"context"
	"sync"
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
	release := sync.OnceFunc(s.mu.Unlock)
	fail := func(err error) (func(), error) { release(); return nil, err }
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
