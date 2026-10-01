package application

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic completed payloads are inserted in one transaction so setup does
// not dominate the benchmark. The timed path still uses the real Store and SQL.
func callHistoryFixture(t testing.TB, history, pending int) (*Store, Binding) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "calls.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c, err := s.Register(t.Context(), "principal", Registration{OperationID: "enroll", Name: "synthetic", Credential: fmt.Sprintf("app-client-%064x", 1)})
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{Scope: c.Scope, SessionID: "synthetic-session", Profile: testProfile(), CreationDigest: "synthetic"}
	if err = s.PutBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	result, err := encode(CallResult{Outcome: "succeeded", Content: json.RawMessage(`"` + strings.Repeat("x", 16*1024) + `"`)})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT INTO app_calls(principal,application,connection,session,call,digest,body,state,result) VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stmt.Close() }()
	for i := range history + pending + 1 {
		cc := testCall(binding)
		cc.ItemID = fmt.Sprintf("item-%d", i)
		call := Call{ID: callID(cc), CallContext: cc, Name: "WriteNote", Arguments: json.RawMessage(`{"note":"synthetic"}`), State: "pending"}
		body, err := encode(call)
		if err != nil {
			t.Fatal(err)
		}
		state := "pending"
		var storedResult []byte
		if i < history {
			state, storedResult = "completed", result
		} else if i == history+pending {
			state = "claimed"
		}
		if _, err = stmt.Exec(binding.PrincipalID, binding.ApplicationID, binding.ConnectionID, binding.SessionID, call.ID, digestBytes(body), body, state, storedResult); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s, binding
}

// Run serially: GOMAXPROCS=1 go test ./control/application -run '^$'
// -bench '^BenchmarkWaitCallsHistory$' -benchmem -benchtime=1x -count=5.
// Each completed result contains 16 KiB; there are two pending and one claimed.
func BenchmarkWaitCallsHistory(b *testing.B) {
	for _, history := range []int{0, 100, 10000} {
		b.Run(fmt.Sprintf("completed=%d/result=16KiB/pending=2", history), func(b *testing.B) {
			s, binding := callHistoryFixture(b, history, 2)
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				calls, err := s.WaitCalls(ctx, binding.Scope, binding.SessionID)
				if err != nil || len(calls) != 2 {
					b.Fatalf("pending=%d, err=%v", len(calls), err)
				}
			}
		})
	}
}

// Compare empty-wait rechecks to the former full-history lookup in the same
// process/database. Timers and notifications are unchanged and excluded here.
func BenchmarkWaitCallsEmptyLookup(b *testing.B) {
	for _, history := range []int{0, 100, 10000} {
		b.Run(fmt.Sprintf("completed=%d/result=16KiB/pending=0", history), func(b *testing.B) {
			s, binding := callHistoryFixture(b, history, 0)
			for _, strategy := range []string{"legacy", "indexed"} {
				b.Run(strategy, func(b *testing.B) {
					b.ReportAllocs()
					for range b.N {
						s.mu.Lock()
						_, err := s.active(b.Context(), binding.Scope)
						var pending []Call
						if err == nil {
							if strategy == "indexed" {
								pending, err = s.pendingCalls(b.Context(), binding.Scope, binding.SessionID)
							} else {
								var calls []Call
								calls, err = s.listCalls(b.Context(), binding.Scope, binding.SessionID)
								for _, call := range calls {
									if call.State == "pending" {
										pending = append(pending, call)
									}
								}
							}
						}
						s.mu.Unlock()
						if err != nil || len(pending) != 0 {
							b.Fatalf("pending=%d, err=%v", len(pending), err)
						}
					}
				})
			}
		})
	}
}
