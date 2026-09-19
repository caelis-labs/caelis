package collaboration

import (
	"context"
	"errors"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
)

// ThreadRead is a bounded observation of a participant's latest public result.
// It contains no reasoning, tool trace or parent conversation history.
type ThreadRead struct {
	Truncated bool   `json:"truncated,omitempty"`
	Thread    Thread `json:"thread"`
	Cursor    uint64 `json:"cursor"`
	Output    string `json:"output,omitempty"`
}

// ThreadObserver supplies authorized latest-result observations from the owner.
type ThreadObserver interface {
	Read(context.Context, string, string, uint64) (ThreadRead, error)
}

// ThreadRemover detaches a participant without deleting its Session history.
type ThreadRemover interface {
	Remove(context.Context, string, string) error
}

// Target identifies a thread and the last observation already received.
type Target struct {
	Handle string `json:"handle"`
	After  uint64 `json:"after,omitempty"`
}

// WaitResult separates destructive mailbox consumption from thread observation.
type WaitResult struct {
	Reason   string       `json:"reason"`
	Messages []Message    `json:"messages"`
	Threads  []ThreadRead `json:"threads"`
}

// Read exposes only participant output; the parent transcript is never shared.
func (s *Service) Read(ctx context.Context, i Identity, target Target) (ThreadRead, error) {
	threads, err := s.members(ctx, i)
	if err != nil {
		return ThreadRead{}, err
	}
	found := false
	for _, t := range threads {
		if t.Handle == target.Handle {
			found = true
		}
	}
	if !found || target.Handle == "parent" {
		return ThreadRead{}, errors.New("thread output is not available to this participant")
	}
	observer, ok := s.backend.(ThreadObserver)
	if !ok {
		return ThreadRead{}, errors.New("thread observation unavailable")
	}
	return observer.Read(ctx, i.Session, target.Handle, target.After)
}

// WaitThreads wakes on incoming mail, successful automatic Runtime admission,
// or a new terminal/attention observation. Cursors suppress repeated
// observations; they are not message acknowledgements.
func (s *Service) WaitThreads(ctx context.Context, i Identity, targets []Target, timeout time.Duration) (WaitResult, error) {
	result, err := s.waitThreads(ctx, i, targets, timeout)
	if err == nil {
		_, err = s.members(ctx, i)
	}
	return result, err
}

func (s *Service) waitThreads(ctx context.Context, i Identity, targets []Target, timeout time.Duration) (WaitResult, error) {
	empty := WaitResult{Reason: "timeout", Messages: []Message{}, Threads: []ThreadRead{}}
	if timeout < 0 || timeout > time.Minute || len(targets) > 8 {
		return empty, errors.New("wait accepts at most 8 threads and 0 to 60 seconds")
	}
	// Native tools observe the Runtime queue, including input admitted before
	// this wait. Other Control callers observe admissions overlapping their wait.
	delivery := agent.InputReady(ctx)
	if delivery == nil {
		wake := s.registerDeliveryWait(i)
		defer s.unregisterDeliveryWait(i, wake)
		delivery = wake
	}
	// Validate all targets before consuming any messages.
	for _, target := range targets {
		if _, err := s.Read(ctx, i, target); err != nil {
			return empty, err
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		result := empty
		mail, err := s.Receive(ctx, i)
		if err != nil {
			return result, err
		}
		result.Messages = mail
		if len(mail) > 0 {
			result.Reason = "message"
			return result, nil
		}
		for _, target := range targets {
			read, err := s.Read(ctx, i, target)
			if err != nil {
				return result, err
			}
			if read.Cursor <= target.After {
				continue
			}
			switch read.Thread.State {
			case "completed", "failed", "cancelled", "interrupted", "terminated", "unknown_outcome", "waiting_input", "waiting_approval":
				result.Threads = append(result.Threads, read)
			}
		}
		if len(result.Threads) > 0 {
			result.Reason = "thread"
			return result, nil
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-delivery:
			// Automatic delivery already removed the mail and admitted it to the
			// active Runtime. Returning reaches the normal safe-point drain.
			result.Reason = "input"
			return result, nil
		case <-timer.C:
			return result, nil
		case <-tick.C:
		}
	}
}

// Remove is an internal Control operation, never a model-facing tool. It revokes
// collaboration membership and mail while preserving the participant history.
func (s *Service) Remove(ctx context.Context, i Identity, handle string) error {
	if i.Member != "parent" || handle == "parent" {
		return errors.New("only the controller can remove a participant")
	}
	if _, err := s.members(ctx, i); err != nil {
		return err
	}
	remover, ok := s.backend.(ThreadRemover)
	if !ok {
		return errors.New("participant removal unavailable")
	}
	if err := remover.Remove(ctx, i.Session, handle); err != nil {
		return err
	}
	s.Revoke(Identity{Session: i.Session, Member: handle})
	_, err := s.db.ExecContext(ctx, `DELETE FROM collaboration_mailbox WHERE session=? AND recipient=?`, i.Session, handle)
	return err
}
