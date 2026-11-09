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

type focusedDeliveryBackend struct {
	mu         sync.Mutex
	lists      int
	claims     map[string]int
	available  map[string]bool
	delivered  []Message
	generation string
}

func (b *focusedDeliveryBackend) List(ctx context.Context, _ string) ([]Thread, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lists++
	// A shared discovery result is deliberately stale. It must not be used
	// to take mail or mark it seen for any recipient's current instance.
	return []Thread{{Handle: "parent"}, {ID: "a", Handle: "a", Generation: "discovery", CanDeliver: true},
		{ID: "b", Handle: "b", Generation: "discovery", CanDeliver: true}, {ID: "c", Handle: "c", Generation: "discovery", CanDeliver: true}}, nil
}

func (b *focusedDeliveryBackend) Recipient(ctx context.Context, target Identity) (Thread, error) {
	if err := ctx.Err(); err != nil {
		return Thread{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.claims[target.Member]++
	return Thread{ID: target.Member, Handle: target.Member, Generation: b.generation, CanDeliver: b.available[target.Member]}, nil
}

func (b *focusedDeliveryBackend) Deliver(_ context.Context, _ string, messages []Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.delivered = append(b.delivered, messages...)
	if messages[0].To == "b" {
		return errors.New("receipt unknown after admission")
	}
	return nil
}

func TestRunSharesOnlyDiscoveryAndRechecksEachClaim(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := &focusedDeliveryBackend{claims: map[string]int{}, available: map[string]bool{"b": true, "c": true}, generation: "current"}
		service, err := Open(filepath.Join(t.TempDir(), "mail.sqlite"), backend)
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		want := map[string]Message{}
		for _, target := range []string{"a", "b", "c"} {
			message, err := service.Send(t.Context(), Identity{"session", "parent"}, target, "synthetic mail", "")
			if err != nil {
				t.Fatal(err)
			}
			want[target] = message
		}
		backend.lists = 0
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); service.Run(ctx, nil) }()
		defer func() { cancel(); <-done }()
		time.Sleep(250 * time.Millisecond)
		synctest.Wait()
		backend.mu.Lock()
		// One initial lifecycle sweep plus one shared candidate lookup; not
		// one full discovery per recipient. Each claim used Recipient instead.
		if backend.lists != 2 || !reflect.DeepEqual(backend.claims, map[string]int{"a": 1, "b": 1, "c": 1}) || len(backend.delivered) != 2 {
			t.Errorf("lists=%d claims=%v delivered=%d", backend.lists, backend.claims, len(backend.delivered))
		}
		backend.available["a"], backend.generation = true, "next"
		backend.mu.Unlock()
		var pending int
		if err := service.db.QueryRow(`SELECT COUNT(*) FROM collaboration_mailbox`).Scan(&pending); err != nil || pending != 1 {
			t.Fatalf("stale discovery consumed unavailable mail: %d, %v", pending, err)
		}
		var seenCurrent int
		if err := service.db.QueryRow(`SELECT COUNT(*) FROM collaboration_seen WHERE id=? AND instance=?`, want["c"].ID, "c\x00c\x00\x00current").Scan(&seenCurrent); err != nil || seenCurrent != 1 {
			t.Fatalf("fresh recipient instance not marked seen: %d, %v", seenCurrent, err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		cancel()
		<-done
		backend.mu.Lock()
		defer backend.mu.Unlock()
		if len(backend.delivered) != 3 {
			t.Fatalf("accepted/unknown mail replayed: %d deliveries", len(backend.delivered))
		}
		seen := map[string]bool{}
		for _, message := range backend.delivered {
			if want[message.To] != message || seen[message.ID] {
				t.Fatalf("mail identity changed or replayed: %+v", message)
			}
			seen[message.ID] = true
		}
		if backend.claims["a"] != 2 || backend.claims["b"] != 1 || backend.claims["c"] != 1 {
			t.Fatalf("unexpected retry after accepted/unknown outcome: %v", backend.claims)
		}
	})
}

type cancelFocusedBackend struct {
	focusedDeliveryBackend
	phase   string
	entered chan struct{}
	once    sync.Once
}

func (b *cancelFocusedBackend) List(ctx context.Context, id string) ([]Thread, error) {
	if b.phase == "discovery" {
		b.once.Do(func() { close(b.entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return b.focusedDeliveryBackend.List(ctx, id)
}

func (b *cancelFocusedBackend) Recipient(ctx context.Context, target Identity) (Thread, error) {
	if b.phase == "claim" {
		b.once.Do(func() { close(b.entered) })
		<-ctx.Done()
		return Thread{}, ctx.Err()
	}
	return b.focusedDeliveryBackend.Recipient(ctx, target)
}

func TestRunCancellationJoinsCandidateAndClaimReads(t *testing.T) {
	for _, phase := range []string{"discovery", "claim"} {
		t.Run(phase, func(t *testing.T) {
			backend := &cancelFocusedBackend{focusedDeliveryBackend: focusedDeliveryBackend{
				claims: map[string]int{}, available: map[string]bool{"a": true, "b": true, "c": true},
			}, entered: make(chan struct{})}
			service, err := Open(filepath.Join(t.TempDir(), "mail.sqlite"), backend)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()
			for _, target := range []string{"a", "b", "c"} {
				if _, err := service.Send(t.Context(), Identity{"session", "parent"}, target, "retained on cancellation", ""); err != nil {
					t.Fatal(err)
				}
			}
			backend.phase = phase
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); service.Run(ctx, nil) }()
			select {
			case <-backend.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("read did not start")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("Run did not join canceled reads")
			}
			var pending int
			if err := service.db.QueryRow(`SELECT COUNT(*) FROM collaboration_mailbox`).Scan(&pending); err != nil || pending != 3 {
				t.Fatalf("canceled observation consumed mail: %d, %v", pending, err)
			}
		})
	}
}
