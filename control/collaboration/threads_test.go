package collaboration

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

type observationBackend struct {
	testBackend
	revision uint64
}

func (b *observationBackend) Read(_ context.Context, _, handle string, after uint64) (ThreadRead, error) {
	r := ThreadRead{Thread: Thread{ID: handle, Handle: handle, State: "completed"}, Cursor: b.revision}
	if b.revision > after {
		r.Output = "public result"
	}
	return r, nil
}

func TestThreadWaitUsesCursorAndMailboxRemainsDestructive(t *testing.T) {
	b := &observationBackend{revision: 2}
	s, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := Identity{"work", "parent"}
	result, err := s.WaitThreads(t.Context(), i, []Target{{Handle: "b"}}, 0)
	if err != nil || result.Reason != "thread" || len(result.Threads) != 1 {
		t.Fatalf("first %v %v", result, err)
	}
	result, err = s.WaitThreads(t.Context(), i, []Target{{Handle: "a"}, {Handle: "b"}}, 0)
	if err != nil || len(result.Threads) != 2 {
		t.Fatalf("multiple targets: %v, %v", result, err)
	}
	result, err = s.WaitThreads(t.Context(), i, []Target{{Handle: "b", After: 2}}, 0)
	if err != nil || result.Reason != "timeout" {
		t.Fatalf("repeated %v %v", result, err)
	}
	_, err = s.Send(t.Context(), Identity{"work", "b"}, "parent", "reply", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.WaitThreads(t.Context(), i, nil, 0)
	if err != nil || result.Reason != "message" || len(result.Messages) != 1 {
		t.Fatalf("mail %v %v", result, err)
	}
	if mail, err := s.Receive(t.Context(), i); err != nil || len(mail) != 0 {
		t.Fatal("mail delivered twice")
	}
	if _, err = s.Read(t.Context(), i, Target{Handle: "parent"}); err == nil {
		t.Fatal("parent transcript exposed")
	}
	for _, def := range Definitions(false) {
		if def.Name == "CloseThread" || def.Name == "StartThread" {
			t.Fatalf("participant tool %s", def.Name)
		}
	}
}

func TestControlWaitObservesOnlySuccessfulOverlappingDelivery(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%v", fail), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				backend := &testBackend{deliver: true, failure: fail}
				s := openTestService(t, backend)
				i := Identity{"work", "b"}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				type outcome struct {
					result WaitResult
					err    error
				}
				results := make(chan outcome, 2)
				// One admission wakes all observers without giving either a copy
				// of the input already handed to Runtime.
				for range 2 {
					go func() {
						result, err := s.WaitThreads(ctx, i, nil, time.Minute)
						results <- outcome{result, err}
					}()
				}
				synctest.Wait()
				if _, err := s.Send(t.Context(), Identity{"work", "a"}, "b", "queued", ""); err != nil {
					t.Fatal(err)
				}
				if err := s.deliverRecipient(t.Context(), i); (err != nil) != fail {
					t.Fatalf("delivery error = %v", err)
				}
				synctest.Wait()
				if fail {
					select {
					case got := <-results:
						t.Fatalf("failed delivery woke wait: %#v", got)
					default:
					}
					cancel()
				}
				for range 2 {
					got := <-results
					if fail && !errors.Is(got.err, context.Canceled) || !fail && (got.err != nil || got.result.Reason != "input") || len(got.result.Messages) != 0 {
						t.Fatalf("wait outcome = %#v", got)
					}
				}
				s.mu.Lock()
				remaining := len(s.waiters)
				s.mu.Unlock()
				if remaining != 0 {
					t.Fatalf("waiter registrations leaked: %d", remaining)
				}
			})
		})
	}
}
