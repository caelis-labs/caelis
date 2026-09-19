package collaboration

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type batchBackend struct {
	mu            sync.Mutex
	closed        bool
	unavailable   bool
	failure       error
	batches       [][]Message
	beforeReturn  func()
	beforeDeliver func()
	lists         int
}

func (b *batchBackend) List(context.Context, string) ([]Thread, error) {
	b.mu.Lock()
	b.lists++
	closed, failure, hook, unavailable := b.closed, b.failure, b.beforeReturn, b.unavailable
	b.beforeReturn = nil
	b.mu.Unlock()
	if hook != nil {
		hook()
	}
	if closed {
		return nil, ErrSessionClosed
	}
	if failure != nil {
		return nil, failure
	}
	return []Thread{{Handle: "a"}, {Handle: "c"}, {Handle: "b", CanDeliver: !unavailable}}, nil
}
func (b *batchBackend) Deliver(_ context.Context, _ string, messages []Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.beforeDeliver != nil {
		b.beforeDeliver()
	}
	b.batches = append(b.batches, append([]Message(nil), messages...))
	return errors.New("unknown remote outcome")
}
func TestDeliveryBatchPreservesMultipleSendersOrderAndDoesNotRetry(t *testing.T) {
	b := &batchBackend{unavailable: true}
	s, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b.beforeDeliver = func() {
		var pending int
		if err := s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM collaboration_mailbox`).Scan(&pending); err != nil || pending != 0 {
			t.Fatalf("batch was not consumed before dispatch: pending=%d err=%v", pending, err)
		}
	}
	var want []Message
	// More than the pull API's 32-message limit still fits one automatic
	// admission. Alternating sources must retain queue order.
	for i := range 65 {
		sender := []string{"a", "c"}[i%2]
		m, err := s.Send(t.Context(), Identity{"work", sender}, "b", sender+" text", "")
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, m)
	}
	if err := s.deliverRecipient(t.Context(), Identity{"work", "b"}); err != nil {
		t.Fatal(err)
	}
	if len(b.batches) != 0 {
		t.Fatal("mail delivered to running non-steering recipient")
	}
	b.unavailable = false
	if err := s.deliverRecipient(t.Context(), Identity{"work", "b"}); err == nil {
		t.Fatal("expected dispatch error")
	}
	if err := s.deliverRecipient(t.Context(), Identity{"work", "b"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.batches, [][]Message{want}) {
		t.Fatalf("batches = %#v", b.batches)
	}
}
func TestClosedSessionCleansHistoricalMailAndTransientErrorsKeepIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	b := &batchBackend{}
	s, err := Open(path, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"b", "c"} {
		if _, err := s.Send(t.Context(), Identity{"work", "a"}, to, "pending", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b.failure = errors.New("temporary store failure")
	if err := s.deliverRecipient(t.Context(), Identity{"work", "b"}); !errors.Is(err, b.failure) {
		t.Fatal(err)
	}
	targets, err := s.pendingRecipients(t.Context())
	if err != nil || len(targets) != 2 {
		t.Fatalf("transient failure purged mail: %v %v", targets, err)
	}
	b.failure = nil
	b.closed = true
	if err := s.deliverRecipient(t.Context(), Identity{"work", "b"}); err != nil {
		t.Fatal(err)
	}
	targets, err = s.pendingRecipients(t.Context())
	if err != nil || len(targets) != 0 {
		t.Fatalf("closed mail remains: %v %v", targets, err)
	}
}
func TestCloseSendRaceCannotStrandMailAfterCleanup(t *testing.T) {
	b := &batchBackend{}
	s, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checked, release := make(chan struct{}), make(chan struct{})
	b.beforeReturn = func() { close(checked); <-release }
	done := make(chan error, 1)
	go func() { _, err := s.Send(t.Context(), Identity{"work", "a"}, "b", "late", ""); done <- err }()
	<-checked
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	if err := s.purgeClosedSession(t.Context(), "work"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("send = %v", err)
	}
	targets, err := s.pendingRecipients(t.Context())
	if err != nil || len(targets) != 0 {
		t.Fatalf("stranded targets %v %v", targets, err)
	}
}

func TestRunStopsPollingClosedHistoricalSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &batchBackend{}
		path := filepath.Join(t.TempDir(), "control.sqlite")
		s, err := Open(path, b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Send(t.Context(), Identity{"work", "a"}, "b", "history", ""); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		b.closed = true
		b.lists = 0
		s, err = Open(path, b)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() {
			s.Run(ctx, func(err error) { t.Errorf("closed session repeatedly reported: %v", err) })
			close(done)
		}()
		time.Sleep(250 * time.Millisecond)
		synctest.Wait()
		b.mu.Lock()
		first := b.lists
		b.mu.Unlock()
		// Delivery and the startup retention sweep may both observe closure.
		if first < 1 || first > 2 {
			t.Fatalf("initial checks %d", first)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		b.mu.Lock()
		later := b.lists
		b.mu.Unlock()
		if later != first {
			t.Fatalf("kept polling closed session: %d -> %d", first, later)
		}
		cancel()
		<-done
	})
}
