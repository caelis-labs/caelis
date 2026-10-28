package gatewayapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestControlObservationPreservesIdleAndPinsRuntime(t *testing.T) {
	for _, during := range []string{"dwell", "generation"} {
		t.Run(during, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, work, probe, _ := idleCoordinatorFixture(t, true)
				window := <-work.released
				work.released <- window
				rt := &sessionRuntime{sessionID: window.SessionRef().SessionID, instance: &sessionRuntimeInstance{runtimeComposition: runtimeComposition{sessions: c.composition.sessions}}, workRefs: 1}
				rt.instance.idleCompaction = c
				r := &sessionRuntimeRegistry{sessionService: c.composition.sessions, lifecycleCtx: t.Context(), sessions: map[string]*sessionRuntime{rt.sessionID: rt}, observers: map[string]uint64{rt.sessionID: 1}}
				c.composition.idleQuiet = func(ctx context.Context, ref session.SessionRef) (bool, error) {
					return r.idleCompactionQuiet(ctx, rt, ref)
				}
				probe.blocked = make(chan struct{})
				unblock := sync.OnceFunc(func() { close(probe.blocked) })
				defer unblock()
				go c.run(work)
				time.Sleep(59 * time.Second)
				if during == "generation" {
					time.Sleep(time.Second)
				}
				synctest.Wait()
				service := ControlRuntimeService{registry: r, sessions: r.sessionService}
				lease, err := service.Acquire(t.Context(), appserver.Principal{ID: "fixture"}, appserver.ActionSessionInspect, rt.sessionID, false)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close(context.Background())
				if work.ctx.Err() != nil {
					t.Fatal("observation cancelled idle work")
				}
				time.Sleep(time.Second)
				synctest.Wait()
				if probe.calls.Load() != 1 {
					t.Fatalf("read crossing dwell suppressed dispatch: calls=%d", probe.calls.Load())
				}
				unblock()
				synctest.Wait()
				// Explicit teardown must still wait for synchronous observation.
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				if err := r.waitForRuntimeUses(ctx); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("read did not pin Runtime: %v", err)
				}
				if err := lease.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := r.waitForRuntimeUses(t.Context()); err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Hour)
				synctest.Wait()
				if probe.calls.Load() != 1 {
					t.Fatal("read release rearmed idle work")
				}
			})
		})
	}
}

func TestControlMutationStillCancelsIdle(t *testing.T) {
	for _, action := range []appserver.Action{appserver.ActionConversationInteraction, appserver.ActionSessionConfigure, appserver.ActionSessionClose} {
		t.Run(string(action), func(t *testing.T) {
			c, work, _, _ := idleCoordinatorFixture(t, true)
			defer work.cancel()
			window := <-work.released
			rt := &sessionRuntime{sessionID: window.SessionRef().SessionID, instance: &sessionRuntimeInstance{runtimeComposition: runtimeComposition{sessions: c.composition.sessions}}, workRefs: 1}
			rt.instance.idleCompaction = c
			r := &sessionRuntimeRegistry{sessionService: c.composition.sessions, lifecycleCtx: t.Context(), sessions: map[string]*sessionRuntime{rt.sessionID: rt}}
			service := ControlRuntimeService{registry: r, sessions: r.sessionService}
			lease, err := service.Acquire(t.Context(), appserver.Principal{ID: "fixture"}, action, rt.sessionID, false)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close(t.Context())
			if !errors.Is(work.ctx.Err(), context.Canceled) {
				t.Fatalf("%s did not cancel idle", action)
			}
		})
	}
}
