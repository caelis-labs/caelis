package sessionprofile

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

func TestWaitCallsLargeHistoryPreservesReceiptsAndOrder(t *testing.T) {
	s, binding := callHistoryFixture(t, 1000, 3)
	before, err := s.ListCalls(t.Context(), binding.Scope, binding.SessionID)
	if err != nil || len(before) != 1004 {
		t.Fatalf("history=%d, err=%v", len(before), err)
	}
	// IDs are hashes rather than insertion keys; pending order must stay rowid.
	want := before[1000:1003]
	got, err := s.WaitCalls(t.Context(), binding.Scope, binding.SessionID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("wait did not preserve pending receipts/order: %v", err)
	}
	for _, i := range []int{0, 1003} {
		call, err := s.GetCall(t.Context(), binding.Scope, binding.SessionID, before[i].ID)
		if err != nil || !reflect.DeepEqual(call, before[i]) {
			t.Fatalf("receipt %d changed: %v", i, err)
		}
	}
	after, err := s.ListCalls(t.Context(), binding.Scope, binding.SessionID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("wait changed historical snapshot: %v", err)
	}
	other := testOwner(t, s, 2)
	foreign := testBinding(t, s, other, "foreign")
	enqueueTest(t, s, testCall(foreign))
	second := testBinding(t, s, testPrincipal{Scope: binding.Scope}, "second")
	enqueueTest(t, s, testCall(second))
	got, err = s.WaitCalls(t.Context(), binding.Scope, binding.SessionID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("foreign pending calls leaked: %v", err)
	}
	for _, scope := range []Scope{other.Scope, {PrincipalID: "other"}} {
		_, err = s.WaitCalls(t.Context(), scope, binding.SessionID)
		if !errors.Is(err, ErrUnauthorized) && !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign scope allowed: %v", err)
		}
	}
}

func TestWaitCallsDoesNotDecodeUnrelatedTerminalPayloads(t *testing.T) {
	s, binding := callHistoryFixture(t, 2, 1)
	// Deliberately corrupt each irrelevant JSON column: Wait must never read it.
	// Exact receipt/history reads must still report the corruption.
	if _, err := s.db.Exec(`UPDATE client_tool_calls SET body=X'7B' WHERE rowid=1; UPDATE client_tool_calls SET result=X'7B' WHERE rowid=2`); err != nil {
		t.Fatal(err)
	}
	calls, err := s.WaitCalls(t.Context(), binding.Scope, binding.SessionID)
	if err != nil || len(calls) != 1 {
		t.Fatalf("unrelated payload decoded: %v", err)
	}
	if _, err = s.ListCalls(t.Context(), binding.Scope, binding.SessionID); err == nil {
		t.Fatal("history silently skipped invalid payload")
	}
	for _, i := range []int{0, 1} {
		cc := testCall(binding)
		cc.ItemID = fmt.Sprintf("item-%d", i)
		if _, err = s.GetCall(t.Context(), binding.Scope, binding.SessionID, callID(cc)); err == nil {
			t.Fatal("receipt silently skipped invalid payload")
		}
	}
}

func TestWaitCallsEmptyWakeConcurrentClaimsAndUnknownRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, binding := callHistoryFixture(t, 100, 0)
		const n = 8
		type observed struct {
			calls []Call
			err   error
		}
		observations := make(chan observed, n)
		for range n {
			go func() {
				calls, err := s.WaitCalls(t.Context(), binding.Scope, binding.SessionID)
				observations <- observed{calls, err}
			}()
		}
		synctest.Wait()
		// A global notification without a pending candidate must only recheck.
		s.mu.Lock()
		s.signal()
		s.mu.Unlock()
		synctest.Wait()
		select {
		case result := <-observations:
			t.Fatalf("empty wait returned on unrelated change: %+v", result)
		default:
		}
		cc := testCall(binding)
		cc.ItemID = "new-intent"
		id := enqueueTest(t, s, cc)
		synctest.Wait()
		for range n {
			result := <-observations
			if result.err != nil || len(result.calls) != 1 || result.calls[0].ID != id {
				t.Fatalf("waiter lost insertion: %+v", result)
			}
		}
		claims := make(chan error, n)
		for range n {
			go func() {
				_, err := s.ClaimCall(t.Context(), binding.Scope, binding.SessionID, id)
				claims <- err
			}()
		}
		granted := 0
		for range n {
			if err := <-claims; err == nil {
				granted++
			} else {
				assertError(t, err, ErrAlreadyClaimed)
			}
		}
		if granted != 1 {
			t.Fatalf("dispatch grants=%d", granted)
		}
		if err := s.CancelCall(t.Context(), binding.Scope, binding.SessionID, id); err != nil {
			t.Fatal(err)
		}
		receipt, err := s.GetCall(t.Context(), binding.Scope, binding.SessionID, id)
		if err != nil || receipt.State != "unknown" {
			t.Fatalf("uncertain claim lost: %+v %v", receipt, err)
		}
		assertError(t, s.CompleteCall(t.Context(), binding.Scope, binding.SessionID, id, testResult()), ErrAlreadyClaimed)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { _, err := s.WaitCalls(ctx, binding.Scope, binding.SessionID); done <- err }()
		synctest.Wait()
		cancel()
		assertError(t, <-done, context.Canceled)
		// An insertion racing the first scan is also visible without another wake.
		cc.ItemID = "racing-insertion"
		go func() {
			calls, err := s.WaitCalls(t.Context(), binding.Scope, binding.SessionID)
			observations <- observed{calls, err}
		}()
		id = enqueueTest(t, s, cc)
		result := <-observations
		if result.err != nil || len(result.calls) != 1 || result.calls[0].ID != id {
			t.Fatalf("scan/subscription race lost wake: %+v", result)
		}
	})
}

func TestWaitCallsEmptyLifecycle(t *testing.T) {
	for _, reason := range []string{"cancel", "timeout", "close", "archive"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, binding := callHistoryFixture(t, 100, 0)
				timeout := time.Hour
				if reason == "timeout" {
					timeout = time.Second
				}
				ctx, cancel := context.WithTimeout(t.Context(), timeout)
				defer cancel()
				done := make(chan error, 1)
				go func() { _, err := s.WaitCalls(ctx, binding.Scope, binding.SessionID); done <- err }()
				synctest.Wait()
				want := context.Canceled
				switch reason {
				case "cancel":
					cancel()
				case "timeout":
					// Deadline before the lease, without any store notification.
					want = context.DeadlineExceeded
				case "close":
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					want = ErrClosed
				case "archive":
					if err := s.ArchiveBinding(t.Context(), binding.Scope, binding.SessionID); err != nil {
						t.Fatal(err)
					}
					want = ErrRevoked
				}
				assertError(t, <-done, want)
			})
		})
	}
}
