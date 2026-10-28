package gatewayapp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

type idleCoordinatorProbe struct {
	calls, closed atomic.Int32
	fresh         bool
	blocked       chan struct{}
	accounting    atomic.Bool
}

func (p *idleCoordinatorProbe) Fresh() bool { return p.fresh }
func (p *idleCoordinatorProbe) Run(context.Context, session.IdleCompactionWindow) (bool, error) {
	p.calls.Add(1)
	if p.blocked != nil {
		<-p.blocked
	}
	return true, nil
}
func (p *idleCoordinatorProbe) FlushAccounting(context.Context) error {
	if p.accounting.Load() {
		return errors.New("fixture: accounting pending")
	}
	return nil
}
func (p *idleCoordinatorProbe) Close() { p.closed.Add(1) }

func idleCoordinatorFixture(t *testing.T, quiet bool) (*idleCompactionCoordinator, *idleCompactionWork, *idleCoordinatorProbe, *atomic.Bool) {
	t.Helper()
	store := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "idle", UserID: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	message := model.NewTextMessage(model.RoleAssistant, "finished source")
	if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{Type: session.EventTypeAssistant, Message: &message}}); err != nil {
		t.Fatal(err)
	}
	fence, err := store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "host"})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := session.CheckpointSourceStamp(loaded)
	if err != nil {
		t.Fatal(err)
	}
	window, err := store.ReleaseSessionFenceForIdleCompaction(t.Context(), session.NewIdleCompactionRelease(fence, session.IdleCompactionSource{JobID: "job", HostID: "host", ActivationID: "activation", TurnID: "turn", ThroughSeq: loaded.Events[0].Seq, Stamp: stamp, Coverage: session.IdleCompactionCoverage{Revision: 1, EventCount: 1, EndSeq: loaded.Events[0].Seq, EndID: loaded.Events[0].ID}}))
	if err != nil {
		t.Fatal(err)
	}
	composition := &runtimeComposition{sessions: store, authorities: runtimeHostAuthorities{idleGeneration: make(chan struct{}, 1)}, idleQuiet: func(context.Context, session.SessionRef) (bool, error) { return quiet, nil }}
	coordinator := &idleCompactionCoordinator{composition: composition, dwell: 60 * time.Second}
	probe := &idleCoordinatorProbe{fresh: true}
	released := &atomic.Bool{}
	ctx, cancel := context.WithCancel(context.Background())
	work := &idleCompactionWork{job: probe, ctx: ctx, cancel: cancel, released: make(chan session.IdleCompactionWindow, 1), done: make(chan struct{}), releaseWork: func() { released.Store(true) }}
	coordinator.current = work
	work.released <- window
	return coordinator, work, probe, released
}

func TestIdleCoordinatorOneShotAndQuietEligibility(t *testing.T) {
	for _, scenario := range []string{"once", "foreground", "other_work", "global_busy", "cache_expired"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, work, probe, released := idleCoordinatorFixture(t, scenario != "other_work")
				if scenario == "global_busy" {
					c.composition.authorities.idleGeneration <- struct{}{}
				}
				if scenario == "cache_expired" {
					probe.fresh = false
				}
				go c.run(work)
				synctest.Wait()
				time.Sleep(59 * time.Second)
				synctest.Wait()
				if probe.calls.Load() != 0 {
					t.Fatal("dispatched before dwell")
				}
				if scenario == "foreground" {
					c.cancel()
				}
				time.Sleep(time.Second)
				synctest.Wait()
				want := int32(0)
				if scenario == "once" {
					want = 1
				}
				if probe.calls.Load() != want {
					t.Fatalf("calls=%d want=%d", probe.calls.Load(), want)
				}
				if !released.Load() || probe.closed.Load() != 1 {
					t.Fatal("completed opportunity leaked work or source")
				}
				time.Sleep(time.Hour)
				synctest.Wait()
				if probe.calls.Load() != want {
					t.Fatal("idle timer restarted without a completion event")
				}
			})
		})
	}
}

func TestIdleCoordinatorDrainRetainsDependenciesThroughProducerAndAccounting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, work, probe, released := idleCoordinatorFixture(t, true)
		probe.blocked = make(chan struct{})
		probe.accounting.Store(true)
		go c.run(work)
		time.Sleep(60 * time.Second)
		synctest.Wait()
		if probe.calls.Load() != 1 {
			t.Fatal("generation did not start")
		}
		c.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := c.drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("drain=%v", err)
		}
		if released.Load() || probe.closed.Load() != 0 {
			t.Fatal("resources closed before provider drain")
		}
		close(probe.blocked)
		synctest.Wait()
		if released.Load() || probe.closed.Load() != 0 {
			t.Fatal("resources closed before terminal accounting was confirmed")
		}
		probe.accounting.Store(false)
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if !released.Load() || probe.closed.Load() != 1 {
			t.Fatal("settled accounting retained resources")
		}
	})
}
