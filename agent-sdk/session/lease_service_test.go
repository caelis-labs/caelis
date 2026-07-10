package session_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestSessionLeaseServiceConformance(t *testing.T) {
	t.Parallel()

	for _, store := range []string{"memory", "file"} {
		store := store
		t.Run(store, func(t *testing.T) {
			t.Parallel()
			clock := &leaseTestClock{now: time.Unix(100, 0)}
			var service session.Service
			var reopen func() session.Service
			switch store {
			case "memory":
				base := inmemory.NewStore(inmemory.Config{Clock: clock.Now})
				service = inmemory.NewService(base)
				reopen = func() session.Service { return inmemory.NewService(base) }
			case "file":
				root := t.TempDir()
				service = sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root, Clock: clock.Now}))
				reopen = func() session.Service {
					return sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root, Clock: clock.Now}))
				}
			}
			ctx := context.Background()
			active, err := service.StartSession(ctx, session.StartSessionRequest{
				AppName: "caelis", UserID: "user-1", PreferredSessionID: "lease-session",
			})
			if err != nil {
				t.Fatal(err)
			}
			leases := service.(session.SessionLeaseService)
			first, err := leases.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
				SessionRef: active.SessionRef, OwnerID: "host-a", TTL: time.Minute,
			})
			if err != nil {
				t.Fatalf("AcquireSessionLease() error = %v", err)
			}
			if first.LeaseID == "" || first.Revision != 1 || first.OwnerID != "host-a" {
				t.Fatalf("first lease = %#v", first)
			}

			reopened := reopen().(session.SessionLeaseService)
			if _, err := reopened.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
				SessionRef: active.SessionRef, OwnerID: "host-b", TTL: time.Minute,
			}); !errors.Is(err, session.ErrLeaseConflict) {
				t.Fatalf("competing acquire error = %v, want ErrLeaseConflict", err)
			}
			if _, err := reopened.HeartbeatSessionLease(ctx, session.HeartbeatSessionLeaseRequest{
				SessionRef: active.SessionRef, LeaseID: first.LeaseID, OwnerID: first.OwnerID,
				ExpectedLeaseRevision: first.Revision + 1, TTL: time.Minute,
			}); !errors.Is(err, session.ErrLeaseConflict) {
				t.Fatalf("stale heartbeat error = %v, want ErrLeaseConflict", err)
			}
			heartbeat, err := reopened.HeartbeatSessionLease(ctx, session.HeartbeatSessionLeaseRequest{
				SessionRef: active.SessionRef, LeaseID: first.LeaseID, OwnerID: first.OwnerID,
				ExpectedLeaseRevision: first.Revision, TTL: time.Minute,
			})
			if err != nil || heartbeat.Revision != 2 {
				t.Fatalf("HeartbeatSessionLease() = %#v, %v", heartbeat, err)
			}
			if err := leases.ReleaseSessionLease(ctx, session.ReleaseSessionLeaseRequest{
				SessionRef: active.SessionRef, LeaseID: heartbeat.LeaseID, OwnerID: heartbeat.OwnerID,
				ExpectedLeaseRevision: heartbeat.Revision,
			}); err != nil {
				t.Fatalf("ReleaseSessionLease() error = %v", err)
			}
			second, err := reopened.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
				SessionRef: active.SessionRef, OwnerID: "host-b", TTL: time.Minute,
			})
			if err != nil || second.OwnerID != "host-b" {
				t.Fatalf("second acquire = %#v, %v", second, err)
			}
			clock.Advance(2 * time.Minute)
			takenOver, err := leases.AcquireSessionLease(ctx, session.AcquireSessionLeaseRequest{
				SessionRef: active.SessionRef, OwnerID: "host-c", TTL: time.Minute,
			})
			if err != nil || takenOver.OwnerID != "host-c" || takenOver.LeaseID == second.LeaseID {
				t.Fatalf("expired takeover = %#v, %v", takenOver, err)
			}
		})
	}
}

type leaseTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *leaseTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *leaseTestClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}
