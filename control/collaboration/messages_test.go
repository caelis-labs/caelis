package collaboration

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestSharedMessagesIndependentReadersDeliveryGapsAndReplay(t *testing.T) {
	b := &testBackend{deliver: true}
	s := openTestService(t, b)
	first, err := s.Send(t.Context(), Identity{"work", "a"}, "parent", "group first", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Send(t.Context(), Identity{"work", "parent"}, "b", "direct second", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.deliverRecipient(t.Context(), Identity{"work", "b"}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ReadMessages(t.Context(), Identity{"work", "b"}, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != first.ID || page.HasMore {
		t.Fatalf("gap handling = %#v", page)
	}
	next, err := s.ReadMessages(t.Context(), Identity{"work", "b"}, nil, 32)
	if err != nil || len(next.Messages) != 0 {
		t.Fatalf("repeated bodies = %#v %v", next, err)
	}
	// Reading shared history did not take parent's directed message.
	mail, err := s.Receive(t.Context(), Identity{"work", "parent"})
	if err != nil || len(mail) != 1 || mail[0].ID != first.ID {
		t.Fatalf("mail = %#v %v", mail, err)
	}
	a, err := s.ReadMessages(t.Context(), Identity{"work", "a"}, nil, 1)
	if err != nil || len(a.Messages) != 1 || a.Messages[0].ID != second.ID {
		t.Fatalf("reader a = %#v %v", a, err)
	}
	zero := uint64(0)
	replay, err := s.ReadMessages(t.Context(), Identity{"work", "b"}, &zero, 1)
	if err != nil || len(replay.Messages) != 1 || !replay.HasMore {
		t.Fatalf("replay = %#v %v", replay, err)
	}
	replay, err = s.ReadMessages(t.Context(), Identity{"work", "b"}, &replay.Cursor, 1)
	if err != nil || len(replay.Messages) != 1 || replay.Messages[0].ID != second.ID {
		t.Fatalf("second page = %#v %v", replay, err)
	}
}

func TestSharedMessagesConcurrentReadsCancelAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail.sqlite")
	b := &testBackend{}
	s, err := Open(path, b)
	if err != nil {
		t.Fatal(err)
	}
	for range 12 {
		if _, err = s.Send(t.Context(), Identity{"work", "a"}, "parent", "public", ""); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = s.ReadMessages(ctx, Identity{"work", "b"}, nil, 32); err == nil {
		t.Fatal("cancel succeeded")
	}
	var wg sync.WaitGroup
	results := make(chan MessagePage, 12)
	for range 12 {
		wg.Go(func() {
			p, err := s.ReadMessages(t.Context(), Identity{"work", "b"}, nil, 1)
			if err != nil {
				t.Error(err)
			}
			results <- p
		})
	}
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	for p := range results {
		for _, m := range p.Messages {
			if seen[m.ID] {
				t.Fatal("concurrent duplicate")
			}
			seen[m.ID] = true
		}
	}
	if len(seen) != 12 {
		t.Fatalf("received %d", len(seen))
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.ReadMessages(t.Context(), Identity{"work", "b"}, nil, 32)
	if err != nil || len(p.Messages) != 0 {
		t.Fatalf("restart progress = %#v %v", p, err)
	}
	p, err = s.ReadMessages(t.Context(), Identity{"work", "parent"}, nil, 32)
	if err != nil || len(p.Messages) != 12 {
		t.Fatalf("independent restart = %#v %v", p, err)
	}
}

func TestSharedReadDoesNotDeliverAndFailedDeliveryIsReadable(t *testing.T) {
	b := &testBackend{deliver: true, failure: true}
	s := openTestService(t, b)
	m, err := s.Send(t.Context(), Identity{"work", "a"}, "b", "public", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadMessages(t.Context(), Identity{"work", "parent"}, nil, 32); err != nil {
		t.Fatal(err)
	}
	if len(b.delivered) != 0 {
		t.Fatal("shared read started execution")
	}
	if err = s.deliverRecipient(t.Context(), Identity{"work", "b"}); err == nil {
		t.Fatal("expected failed dispatch")
	}
	p, err := s.ReadMessages(t.Context(), Identity{"work", "b"}, nil, 32)
	if err != nil || len(p.Messages) != 1 || p.Messages[0].ID != m.ID {
		t.Fatalf("failed dispatch suppressed = %#v %v", p, err)
	}
	if mail, err := s.Receive(t.Context(), Identity{"work", "b"}); err != nil || len(mail) != 0 {
		t.Fatalf("read resurrected consumed mail = %#v %v", mail, err)
	}
}

type pendingAdmissionBackend struct {
	testBackend
	entered chan struct{}
	release chan struct{}
}

func (b *pendingAdmissionBackend) Deliver(ctx context.Context, _ string, _ []Message) error {
	close(b.entered)
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSharedReadWaitsForConcurrentAutomaticAdmission(t *testing.T) {
	b := &pendingAdmissionBackend{testBackend: testBackend{deliver: true}, entered: make(chan struct{}), release: make(chan struct{})}
	s, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Send(t.Context(), Identity{"work", "a"}, "b", "in flight", ""); err != nil {
		t.Fatal(err)
	}
	i := Identity{"work", "b"}
	delivered := make(chan error, 1)
	go func() { delivered <- s.deliverRecipient(t.Context(), i) }()
	<-b.entered
	if s.readerLock(i).TryLock() {
		s.readerLock(i).Unlock()
		close(b.release)
		t.Fatal("in-flight delivery did not exclude a same-member group read")
	}
	read := make(chan MessagePage, 1)
	go func() {
		page, err := s.ReadMessages(t.Context(), i, nil, 32)
		if err != nil {
			t.Error(err)
		}
		read <- page
	}()
	close(b.release)
	if err := <-delivered; err != nil {
		t.Fatal(err)
	}
	if page := <-read; len(page.Messages) != 0 {
		t.Fatalf("admitted body duplicated by concurrent read: %#v", page)
	}
}

func TestSharedRetentionReportsGap(t *testing.T) {
	s := openTestService(t, &testBackend{})
	// Exercise the owning append/retention transaction without populating an
	// unrelated directed mailbox with thousands of messages.
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < retainedMessages+2; i++ {
		m := Message{ID: fmt.Sprint(i), From: "a", To: "parent", Text: "public"}
		raw, _ := json.Marshal(m)
		if err = appendSharedMessage(t.Context(), tx, Identity{"work", "a"}, "a", m, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	p, err := s.ReadMessages(t.Context(), Identity{"work", "b"}, nil, 1)
	if err != nil || p.TruncatedBefore == 0 || len(p.Messages) != 1 || p.Messages[0].ID != "2" {
		t.Fatalf("retention = %#v %v", p, err)
	}
}
