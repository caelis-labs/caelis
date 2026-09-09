package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type testBackend struct {
	deliver   bool
	failure   bool
	delivered []Message
}

func (b *testBackend) List(_ context.Context, id string) ([]Thread, error) {
	if id != "work" {
		return nil, errors.New("unknown Session")
	}
	return []Thread{{ID: "a", SessionID: "a", Handle: "a"}, {ID: "b", SessionID: "b", Handle: "b", CanDeliver: b.deliver}}, nil
}
func (b *testBackend) Deliver(_ context.Context, _ string, messages []Message) error {
	b.delivered = append(b.delivered, messages...)
	if b.failure {
		return errors.New("peer disconnected")
	}
	return nil
}

func openTestService(t *testing.T, b *testBackend) *Service {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestConcurrentReceiveRemovesEachMessageOnce(t *testing.T) {
	s := openTestService(t, &testBackend{})
	for range 50 {
		if _, err := s.Send(t.Context(), Identity{"work", "a"}, "b", "hello", ""); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]bool{}
	for range 8 {
		wg.Go(func() {
			messages, err := s.Receive(t.Context(), Identity{"work", "b"})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, m := range messages {
				if seen[m.ID] {
					t.Errorf("duplicate %s", m.ID)
				}
				seen[m.ID] = true
			}
		})
	}
	wg.Wait()
	if len(seen) != 50 {
		t.Fatalf("received %d messages", len(seen))
	}
}

func TestMailboxSurvivesReopenAndTakeDoesNot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	backend := &testBackend{}
	s, err := Open(path, backend)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Send(t.Context(), Identity{"work", "a"}, "b", "persistent", "00000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path, backend)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Receive(t.Context(), Identity{"work", "b"})
	if err != nil || len(got) != 1 || got[0] != m {
		t.Fatalf("receive %v %v", got, err)
	}
	_ = s.Close()
	s, err = Open(path, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	got, err = s.Receive(t.Context(), Identity{"work", "b"})
	if err != nil || len(got) != 0 {
		t.Fatalf("removed mail returned: %v %v", got, err)
	}
}

func TestUnsupportedSteeringKeepsMailUntilIdleAndFailureDoesNotRetry(t *testing.T) {
	b := &testBackend{}
	s := openTestService(t, b)
	_, err := s.Send(t.Context(), Identity{"work", "a"}, "b", "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.deliverRecipient(t.Context(), Identity{"work", "b"}); err != nil {
		t.Fatal(err)
	}
	if len(b.delivered) != 0 {
		t.Fatal("delivered while unavailable")
	}
	b.deliver = true
	b.failure = true
	if err = s.deliverRecipient(t.Context(), Identity{"work", "b"}); err == nil {
		t.Fatal("expected dispatch error")
	}
	if err = s.deliverRecipient(t.Context(), Identity{"work", "b"}); err != nil {
		t.Fatal(err)
	}
	if len(b.delivered) != 1 {
		t.Fatalf("attempts %d", len(b.delivered))
	}
}

func TestWaitReceivesMailAndIdentityCannotCrossSession(t *testing.T) {
	s := openTestService(t, &testBackend{})
	done := make(chan []Message, 1)
	go func() {
		got, err := s.WaitThreads(t.Context(), Identity{"work", "b"}, nil, time.Second)
		if err != nil {
			t.Error(err)
		}
		done <- got.Messages
	}()
	if _, err := s.Send(t.Context(), Identity{"work", "a"}, "b", "question", ""); err != nil {
		t.Fatal(err)
	}
	if got := <-done; len(got) != 1 {
		t.Fatalf("wait returned %v", got)
	}
	if _, err := s.Receive(t.Context(), Identity{"other", "b"}); err == nil {
		t.Fatal("cross-session access")
	}
	grant := s.Prepare(Identity{"work", "b"}, "b")
	if err := grant.Bind("b"); err != nil {
		t.Fatal(err)
	}
	id, _, err := s.Authenticate(t.Context(), grant.Token())
	if err != nil || id.Member != "b" {
		t.Fatal("identity changed")
	}
	if _, err = s.Call(t.Context(), id, Request{Tool: "StartThread", Arguments: []byte(`{}`)}); err == nil {
		t.Fatal("participant created thread")
	}
}

type isolatedDeliveryBackend struct {
	started chan string
	expired chan error
}

func (b *isolatedDeliveryBackend) List(context.Context, string) ([]Thread, error) {
	return []Thread{{Handle: "a"}, {Handle: "b", CanDeliver: true}}, nil
}
func (b *isolatedDeliveryBackend) Deliver(ctx context.Context, id string, messages []Message) error {
	b.started <- id + ":" + messages[0].Text
	if id == "blocked" {
		<-ctx.Done()
		b.expired <- ctx.Err()
		return ctx.Err()
	}
	return nil
}
func TestDeliveryIsolatesRecipientsAndExpiresWithoutRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &isolatedDeliveryBackend{started: make(chan string, 8), expired: make(chan error, 8)}
		s, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), b)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		for _, entry := range []struct{ session, text string }{{"blocked", "first"}, {"blocked", "second"}, {"other", "ready"}} {
			if _, err := s.Send(t.Context(), Identity{entry.session, "a"}, "b", entry.text, ""); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { s.Run(ctx, nil); close(done) }()
		time.Sleep(250 * time.Millisecond)
		synctest.Wait()
		seen := map[string]bool{}
		for len(b.started) > 0 {
			seen[<-b.started] = true
		}
		if len(seen) != 2 || !seen["blocked:first"] || !seen["other:ready"] {
			t.Fatalf("concurrent attempts: %v", seen)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if len(b.started) != 0 {
			t.Fatal("same recipient started overlapping delivery")
		}
		time.Sleep(deliveryTimeout)
		synctest.Wait()
		if err := <-b.expired; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		cancel()
		<-done
		for len(b.started) > 0 {
			if got := <-b.started; got == "blocked:first" {
				t.Fatal("retried removed message")
			}
		}
	})
}
func TestMailboxBatchesUseEncodedByteBudget(t *testing.T) {
	s := openTestService(t, &testBackend{})
	text := strings.Repeat("<", 65536)
	for range 32 {
		if _, err := s.Send(t.Context(), Identity{"work", "a"}, "b", text, ""); err != nil {
			t.Fatal(err)
		}
	}
	var receiver tool.Tool
	for _, one := range Tools(func(ctx context.Context, req Request) (json.RawMessage, error) {
		return s.Call(ctx, Identity{"work", "b"}, req)
	}) {
		if one.Definition().Name == "ReceiveMessages" {
			receiver = one
		}
	}
	if receiver == nil {
		t.Fatal("native ReceiveMessages tool missing")
	}
	seen := map[string]bool{}
	for len(seen) < 32 {
		result, err := receiver.Call(t.Context(), tool.Call{Name: "ReceiveMessages", Input: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		raw := []byte(result.Content[0].Text.Text)
		if len(raw) > MaxResponseBytes {
			t.Fatalf("oversized response: %d", len(raw))
		}
		var batch []Message
		if err := json.Unmarshal(raw, &batch); err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			t.Fatalf("lost messages: %d", len(seen))
		}
		for _, m := range batch {
			if seen[m.ID] || m.Text != text {
				t.Fatal("duplicate or damaged message")
			}
			seen[m.ID] = true
		}
	}
	for _, reply := range []string{"not-a-uuid", strings.Repeat("x", 65536)} {
		if _, err := s.Send(t.Context(), Identity{"work", "a"}, "b", "test", reply); err == nil {
			t.Fatal("unbounded reply_to accepted")
		}
	}
}
