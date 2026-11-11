package collaboration

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
)

type interruptBackend struct {
	mu      sync.Mutex
	threads []Thread
	listed  func()
	deliver func() error
	inputs  int
	mail    int
}

func (b *interruptBackend) List(context.Context, string) ([]Thread, error) {
	b.mu.Lock()
	threads := append([]Thread(nil), b.threads...)
	hook := b.listed
	b.listed = nil
	b.mu.Unlock()
	if hook != nil {
		hook()
	}
	return threads, nil
}

func (b *interruptBackend) Deliver(context.Context, string, []Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mail++
	return nil
}

func (b *interruptBackend) DeliverUserInput(context.Context, UserInput) error {
	b.mu.Lock()
	b.inputs++
	hook := b.deliver
	b.mu.Unlock()
	if hook != nil {
		return hook()
	}
	return nil
}

func (b *interruptBackend) revoke() {
	b.mu.Lock()
	b.threads = []Thread{{Handle: "parent", CanDeliver: true}}
	b.mu.Unlock()
}

func newInterruptBackend() *interruptBackend {
	child := userInputTestTarget()
	child.Handle = "child"
	return &interruptBackend{threads: []Thread{{Handle: "parent", CanDeliver: true}, child}}
}

func openInterruptService(t *testing.T, b *interruptBackend) *Service {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestInterruptSessionRetainsHistoryPositionsAndReceiptsAcrossReopen(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	b := newInterruptBackend()
	s, err := Open(path, b)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, id := range []string{"work", "other"} {
		if _, err := s.Send(ctx, Identity{id, "child"}, "parent", "retained history", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReadMessages(ctx, Identity{id, "parent"}, nil, 32); err != nil {
			t.Fatal(err)
		}
		for _, state := range []string{"queued", "sending", "unknown", "sent", "failed"} {
			inputID := id + "-" + state
			if _, err := s.EnqueueUserInput(ctx, inputID, "user", id, "participant", "task", "guide", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE collaboration_user_inputs SET state=?,detail='original' WHERE id=?`, state, inputID); err != nil {
				t.Fatal(err)
			}
		}
	}
	b.revoke()
	for range 2 {
		if err := s.InterruptSession(ctx, "work"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"work", "other"} {
		want := 0
		if id == "other" {
			want = 1
		}
		assertCollaborationRows(t, s, "collaboration_mailbox", id, want)
		assertCollaborationRows(t, s, "collaboration_messages", id, 1)
		assertCollaborationRows(t, s, "collaboration_readers", id, 1)
		assertCollaborationRows(t, s, "collaboration_seen", id, 1)
		for _, state := range []string{"queued", "sending", "unknown", "sent", "failed"} {
			want := state
			if state == "queued" && id == "work" {
				want = "failed"
			}
			assertInputState(t, s, id+"-"+state, id, want)
		}
	}
	// A pre-interrupt discovery snapshot cannot revive a cancelled receipt.
	if err := s.deliverUserInput(ctx, b, UserInput{ID: "work-queued", SessionID: "work", ParticipantID: "participant", TaskID: "task", ChildSessionID: "child", Generation: "generation"}); err != nil {
		t.Fatal(err)
	}
	if err := s.deliverRecipient(ctx, Identity{"work", "parent"}); err != nil {
		t.Fatal(err)
	}
	if b.mail != 0 || b.inputs != 0 {
		t.Fatalf("interrupted work dispatched: mail=%d input=%d", b.mail, b.inputs)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, b)
	if err != nil {
		t.Fatal(err)
	}
	assertCollaborationRows(t, s, "collaboration_mailbox", "work", 0)
	assertInputState(t, s, "work-queued", "work", "failed")
	assertInputState(t, s, "work-sending", "work", "unknown")
	assertInputState(t, s, "work-unknown", "work", "unknown")
	page, err := s.ReadMessages(ctx, Identity{"work", "parent"}, nil, 32)
	if err != nil || len(page.Messages) != 0 || page.Cursor == 0 {
		t.Fatalf("reader position lost: %#v, %v", page, err)
	}
	zero := uint64(0)
	page, err = s.ReadMessages(ctx, Identity{"work", "parent"}, &zero, 32)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "retained history" {
		t.Fatalf("history lost: %#v, %v", page, err)
	}
	status, err := s.EnqueueUserInput(ctx, "work-queued", "user", "work", "participant", "task", "guide", nil)
	if err != nil || status.State != "failed" {
		t.Fatalf("original ID receipt = %#v, %v", status, err)
	}
	// Interruption has no separate cancellation epoch: fresh authorized work is allowed.
	b.mu.Lock()
	b.threads = newInterruptBackend().threads
	b.threads[1].Generation = "new-generation"
	b.mu.Unlock()
	if _, err := s.Send(ctx, Identity{"work", "child"}, "parent", "fresh work", ""); err != nil {
		t.Fatal(err)
	}
	assertCollaborationRows(t, s, "collaboration_mailbox", "work", 1)
}

func assertInputState(t *testing.T, s *Service, id, sessionID, want string) {
	t.Helper()
	statuses, err := s.UserInputStatuses(t.Context(), "user", sessionID, []string{id})
	if err != nil || len(statuses) != 1 || statuses[0].ID != id || statuses[0].State != want {
		t.Fatalf("receipt %s = %#v, %v; want %s", id, statuses, err, want)
	}
}

func TestInterruptSessionRacingLateSendAndEnqueue(t *testing.T) {
	for _, mode := range []string{"mail", "user-input"} {
		t.Run(mode, func(t *testing.T) {
			b := newInterruptBackend()
			s := openInterruptService(t, b)
			listed, resume := make(chan struct{}), make(chan struct{})
			b.listed = func() {
				close(listed)
				<-resume
			}
			finished := make(chan error, 1)
			go func() {
				if mode == "mail" {
					_, err := s.Send(t.Context(), Identity{"work", "child"}, "parent", "late old sender", "")
					finished <- err
				} else {
					_, err := s.EnqueueUserInput(t.Context(), "late-input", "user", "work", "participant", "task", "late input", nil)
					finished <- err
				}
			}()
			<-listed
			b.revoke()
			interrupted := make(chan error, 1)
			go func() { interrupted <- s.InterruptSession(t.Context(), "work") }()
			claimed := make(chan error, 1)
			go func() { claimed <- s.deliverRecipient(t.Context(), Identity{"work", "parent"}) }()
			close(resume)
			err := <-finished
			if mode == "mail" && err == nil {
				t.Fatal("revoked sender accepted")
			}
			if mode == "user-input" && err != nil {
				t.Fatal(err)
			}
			if err := <-interrupted; err != nil {
				t.Fatal(err)
			}
			if err := <-claimed; err != nil {
				t.Fatal(err)
			}
			assertCollaborationRows(t, s, "collaboration_mailbox", "work", 0)
			if mode == "user-input" {
				assertInputState(t, s, "late-input", "work", "failed")
			}
			if err := s.deliverRecipient(t.Context(), Identity{"work", "parent"}); err != nil {
				t.Fatal(err)
			}
			if b.mail != 0 {
				t.Fatal("late old message reopened parent")
			}
		})
	}
}

func TestInterruptSessionPreservesInFlightReceiptAndRejectsRequeue(t *testing.T) {
	for _, outcome := range []string{"not-ready", "unknown", "sent"} {
		t.Run(outcome, func(t *testing.T) {
			b := newInterruptBackend()
			s := openInterruptService(t, b)
			if _, err := s.EnqueueUserInput(t.Context(), "input", "user", "work", "participant", "task", "guide", nil); err != nil {
				t.Fatal(err)
			}
			pending, err := s.pendingUserInputs(t.Context())
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending=%v,%v", pending, err)
			}
			b.deliver = func() error {
				b.revoke()
				if err := s.InterruptSession(t.Context(), "work"); err != nil {
					t.Fatal(err)
				}
				if err := s.deliverUserInput(t.Context(), b, pending[0]); err != nil {
					t.Fatal(err)
				}
				assertInputState(t, s, "input", "work", "sending")
				switch outcome {
				case "not-ready":
					return agent.ErrChildInputNotReady
				case "unknown":
					return errors.New("lost receipt")
				default:
					return nil
				}
			}
			if err := s.deliverUserInput(t.Context(), b, pending[0]); err != nil {
				t.Fatal(err)
			}
			want := outcome
			if outcome == "not-ready" {
				want = "failed"
			}
			assertInputState(t, s, "input", "work", want)
			// Stale queue discovery cannot overwrite a completed or unknown receipt.
			if err := s.deliverUserInput(t.Context(), b, pending[0]); err != nil {
				t.Fatal(err)
			}
			assertInputState(t, s, "input", "work", want)
			if b.inputs != 1 {
				t.Fatalf("dispatch count=%d", b.inputs)
			}
		})
	}
}
