package collaboration

import (
	"context"
	"errors"
)

// RecipientBackend is an optional focused, fresh lookup at the mailbox claim
// boundary. Recipient must recheck canonical membership, instance/generation,
// and current input availability; a prior discovery snapshot cannot authorize
// delivery. Recipient returns all matching observations in List order, including
// any legacy duplicate handles; the ordinary claim loop retains selection and
// reader-instance ownership. An absent recipient returns no observations.
type RecipientBackend interface {
	Recipient(context.Context, Identity) ([]Thread, error)
}

func (s *Service) deliveryGroups(targets []Identity) [][]Identity {
	groups := [][]Identity{}
	if _, ok := s.backend.(RecipientBackend); !ok {
		for _, target := range targets {
			groups = append(groups, []Identity{target})
		}
		return groups
	}
	bySession := map[string]int{}
	for _, target := range targets {
		index, ok := bySession[target.Session]
		if !ok {
			index = len(groups)
			bySession[target.Session] = index
			groups = append(groups, nil)
		}
		groups[index] = append(groups[index], target)
	}
	return groups
}

func (s *Service) deliveryCandidates(ctx context.Context, targets []Identity) (map[string]bool, error) {
	out := make(map[string]bool, len(targets))
	if len(targets) == 1 {
		out[targets[0].Member] = true
		return out, nil
	}
	threads, err := s.backend.List(ctx, targets[0].Session)
	if errors.Is(err, ErrSessionClosed) {
		return nil, s.purgeClosedSession(ctx, targets[0].Session)
	}
	if err != nil {
		return nil, err
	}
	for _, thread := range threads {
		// This is a scheduling hint only. A fresh Recipient call inside the
		// reader lock decides whether each target's mail may actually be taken.
		out[thread.Handle] = out[thread.Handle] || thread.CanDeliver
	}
	return out, nil
}
