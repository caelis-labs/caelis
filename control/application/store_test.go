package application

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}
func testProfile() Profile {
	return Profile{Version: "v1", Instructions: "Use the explicitly provided callback.", Model: "test/model", ToolsVersion: "tools-v1", Execution: "tools-only", Tools: []ToolDefinition{{Name: "WriteNote", Description: "Record one note.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"note": map[string]any{"type": "string"}}, "required": []any{"note"}, "additionalProperties": false}}}}
}
func testConnection(t *testing.T, s *Store, seed int) Connection {
	t.Helper()
	c, err := s.Register(t.Context(), "principal", Registration{OperationID: fmt.Sprintf("enroll-%d", seed), Name: "app", Credential: fmt.Sprintf("app-client-%064x", seed)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func testBinding(t *testing.T, s *Store, c Connection, id string) Binding {
	t.Helper()
	b := Binding{Scope: c.Scope, SessionID: id, Profile: testProfile(), CreationDigest: "creation-" + id}
	if err := s.PutBinding(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	return b
}
func testCall(b Binding) CallContext {
	return CallContext{Scope: b.Scope, SessionID: b.SessionID, TurnID: "turn-1", ItemID: "item-1", CallID: "provider-call", ToolsVersion: b.Profile.ToolsVersion, Source: Source{Kind: "user", OperationID: "prompt-1"}}
}
func enqueueTest(t *testing.T, s *Store, c CallContext) string {
	t.Helper()
	id, err := s.enqueue(t.Context(), c, "WriteNote", json.RawMessage(`{"note":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func testResult() CallResult {
	return CallResult{Outcome: "succeeded", Content: json.RawMessage(`{"recorded":true}`)}
}
func assertError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestA02EnrollmentHashLeaseAndScope(t *testing.T) {
	s, path := testStore(t)
	now := time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	credential := "app-client-" + strings.Repeat("a", 64)
	req := Registration{OperationID: "enroll", Name: "notes", Credential: credential}
	c, err := s.Register(t.Context(), "owner", req)
	if err != nil {
		t.Fatal(err)
	}
	if c.ExpiresAt.Sub(now) != 10*time.Minute {
		t.Fatal("wrong lease duration")
	}
	same, err := s.Register(t.Context(), "owner", req)
	if err != nil || same != c {
		t.Fatalf("idempotent enrollment: %+v %v", same, err)
	}
	for _, change := range []Registration{{OperationID: "enroll", Name: "different", Credential: credential}, {OperationID: "enroll", Name: "notes", Credential: "app-client-" + strings.Repeat("b", 64)}, {OperationID: "other", Name: "notes", Credential: credential}} {
		_, err = s.Register(t.Context(), "owner", change)
		assertError(t, err, ErrConflict)
	}
	_, err = s.Register(t.Context(), "another-owner", req)
	assertError(t, err, ErrConflict)
	var hash, digest string
	if err = s.db.QueryRow(`SELECT credential_hash,digest FROM app_connections`).Scan(&hash, &digest); err != nil {
		t.Fatal(err)
	}
	if hash == credential || strings.Contains(digest, credential) {
		t.Fatal("raw credential persisted")
	}
	for _, bad := range []string{"", strings.TrimPrefix(credential, "app-client-"), credential + "0", "app-client-" + strings.Repeat("g", 64)} {
		_, err = s.Authenticate(t.Context(), bad)
		assertError(t, err, ErrUnauthorized)
	}
	for _, bad := range []Scope{{PrincipalID: "other", ApplicationID: c.ApplicationID, ConnectionID: c.ConnectionID}, {PrincipalID: c.PrincipalID, ApplicationID: "other", ConnectionID: c.ConnectionID}, {PrincipalID: c.PrincipalID, ApplicationID: c.ApplicationID, ConnectionID: "other"}} {
		_, err = s.Connection(t.Context(), bad)
		assertError(t, err, ErrUnauthorized)
	}
	b := testBinding(t, s, c, "session")
	now = now.Add(9 * time.Minute)
	renewed, err := s.Renew(t.Context(), c.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.ExpiresAt != now.Add(LeaseDuration) {
		t.Fatal("renewal duration")
	}
	now = renewed.ExpiresAt
	scope, err := s.Authenticate(t.Context(), credential)
	if err != nil || scope != c.Scope {
		t.Fatalf("expired read auth: %v", err)
	}
	_, err = s.GetBinding(t.Context(), c.Scope, b.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.BeginOperation(t.Context(), c.Scope, "op", map[string]any{"name": "x"})
	assertError(t, err, ErrLeaseExpired)
	renewed, err = s.Renew(t.Context(), c.Scope)
	if err != nil || renewed.Scope != c.Scope || renewed.ExpiresAt != now.Add(LeaseDuration) {
		t.Fatalf("expired renewal: %+v %v", renewed, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(credential)) {
		t.Fatal("credential exists in SQLite bytes")
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	scope, err = reopened.Authenticate(t.Context(), credential)
	if err != nil || scope != c.Scope {
		t.Fatalf("durable auth: %v", err)
	}
}

func TestA02RevokePreservesReadAuthority(t *testing.T) {
	s, _ := testStore(t)
	c := testConnection(t, s, 1)
	b := testBinding(t, s, c, "s")
	if err := s.Revoke(t.Context(), c.Scope); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(t.Context(), c.Scope); err != nil {
		t.Fatal(err)
	}
	scope, err := s.Authenticate(t.Context(), fmt.Sprintf("app-client-%064x", 1))
	if err != nil || scope != c.Scope {
		t.Fatalf("revoked authentication: %v", err)
	}
	if _, err = s.GetBinding(t.Context(), scope, b.SessionID); err != nil {
		t.Fatal(err)
	}
	assertError(t, s.CheckActive(t.Context(), scope), ErrRevoked)
	_, err = s.Renew(t.Context(), scope)
	assertError(t, err, ErrRevoked)
}

func TestA02ExpiredRenewalNeverRegrantsCallsOrOperations(t *testing.T) {
	s, _ := testStore(t)
	now := time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	c := testConnection(t, s, 1)
	b := testBinding(t, s, c, "s")
	contexts := map[string]CallContext{}
	ids := map[string]string{}
	for _, state := range []string{"pending", "claimed", "completed"} {
		native := testCall(b)
		native.ItemID = state
		contexts[state] = native
		ids[state] = enqueueTest(t, s, native)
		if state != "pending" {
			if _, err := s.ClaimCall(t.Context(), c.Scope, b.SessionID, ids[state]); err != nil {
				t.Fatal(err)
			}
		}
		if state == "completed" {
			if err := s.CompleteCall(t.Context(), c.Scope, b.SessionID, ids[state], testResult()); err != nil {
				t.Fatal(err)
			}
		}
	}
	request := map[string]any{"action": "prompt", "input": "write one note"}
	op, fresh, err := s.BeginOperation(t.Context(), c.Scope, "existing-op", request)
	if err != nil || !fresh {
		t.Fatalf("initial operation: %+v %v %v", op, fresh, err)
	}
	// Advance the Store clock without an Invoke waiter or receipt observation:
	// Renew itself must retire effects from the expired lease.
	now = c.ExpiresAt
	renewed, err := s.Renew(t.Context(), c.Scope)
	if err != nil || renewed.Scope != c.Scope || renewed.ExpiresAt != now.Add(LeaseDuration) {
		t.Fatalf("renewal: %+v %v", renewed, err)
	}
	if err := s.CheckActive(t.Context(), c.Scope); err != nil {
		t.Fatal(err)
	}
	for prior, want := range map[string]string{"pending": "cancelled", "claimed": "unknown", "completed": "completed"} {
		// Even the same native invocation cannot recreate a dispatchable intent.
		if got := enqueueTest(t, s, contexts[prior]); got != ids[prior] {
			t.Fatalf("%s receipt changed from %s to %s", prior, ids[prior], got)
		}
		call, err := s.GetCall(t.Context(), c.Scope, b.SessionID, ids[prior])
		if err != nil || call.State != want {
			t.Fatalf("%s after renewal: %+v %v", prior, call, err)
		}
		_, err = s.ClaimCall(t.Context(), c.Scope, b.SessionID, call.ID)
		assertError(t, err, ErrAlreadyClaimed)
		if prior != "completed" {
			assertError(t, s.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, testResult()), ErrAlreadyClaimed)
		} else if call.Result == nil || !reflect.DeepEqual(*call.Result, testResult()) {
			t.Fatalf("completed receipt changed: %+v", call.Result)
		}
	}
	replayed, fresh, err := s.BeginOperation(t.Context(), c.Scope, op.ID, request)
	if err != nil || fresh || !reflect.DeepEqual(replayed, op) {
		t.Fatalf("existing operation regranted or changed: %+v %v %v", replayed, fresh, err)
	}
	_, _, err = s.BeginOperation(t.Context(), c.Scope, op.ID, map[string]any{"action": "different"})
	assertError(t, err, ErrConflict)
	// Renewal explicitly authorizes new work in the existing Session and Turn.
	native := testCall(b)
	native.ItemID = "fresh-after-renewal"
	id := enqueueTest(t, s, native)
	if _, err := s.ClaimCall(t.Context(), c.Scope, b.SessionID, id); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteCall(t.Context(), c.Scope, b.SessionID, id, testResult()); err != nil {
		t.Fatal(err)
	}
}

func TestA02ExpiredRenewalRollsBackBeforeNotification(t *testing.T) {
	s, _ := testStore(t)
	now := time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	c := testConnection(t, s, 1)
	b := testBinding(t, s, c, "s")
	ids := map[string]string{}
	for _, state := range []string{"pending", "claimed"} {
		native := testCall(b)
		native.ItemID = state
		ids[state] = enqueueTest(t, s, native)
	}
	if _, err := s.ClaimCall(t.Context(), c.Scope, b.SessionID, ids["claimed"]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_renewal BEFORE UPDATE OF expires ON app_connections BEGIN SELECT RAISE(ABORT, 'injected renewal failure'); END`); err != nil {
		t.Fatal(err)
	}
	now = c.ExpiresAt
	changed := s.changed
	if _, err := s.Renew(t.Context(), c.Scope); err == nil {
		t.Fatal("injected renewal failure was ignored")
	}
	select {
	case <-changed:
		t.Fatal("failed transaction notified waiters")
	default:
	}
	connection, err := s.Connection(t.Context(), c.Scope)
	if err != nil || connection != c {
		t.Fatalf("failed renewal changed lease: %+v %v", connection, err)
	}
	// Query raw state because public receipt observation itself retires expiry.
	for want, id := range ids {
		var state string
		if err := s.db.QueryRow(`SELECT state FROM app_calls WHERE connection=? AND session=? AND call=?`, c.ConnectionID, b.SessionID, id).Scan(&state); err != nil || state != want {
			t.Fatalf("failed renewal changed %s: %s %v", want, state, err)
		}
	}
}

func TestA02LiveRenewalPreservesUnfinishedCalls(t *testing.T) {
	s, _ := testStore(t)
	now := time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	c := testConnection(t, s, 1)
	b := testBinding(t, s, c, "s")
	pending := enqueueTest(t, s, testCall(b))
	native := testCall(b)
	native.ItemID = "claimed"
	claimed := enqueueTest(t, s, native)
	if _, err := s.ClaimCall(t.Context(), c.Scope, b.SessionID, claimed); err != nil {
		t.Fatal(err)
	}
	now = now.Add(LeaseDuration / 2)
	if _, err := s.Renew(t.Context(), c.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimCall(t.Context(), c.Scope, b.SessionID, pending); err != nil {
		t.Fatalf("live renewal withdrew pending claim: %v", err)
	}
	for _, id := range []string{pending, claimed} {
		if err := s.CompleteCall(t.Context(), c.Scope, b.SessionID, id, testResult()); err != nil {
			t.Fatalf("live renewal withdrew result: %v", err)
		}
	}
}

func TestA02ExpiredConnectionCanBePermanentlyRevoked(t *testing.T) {
	s, _ := testStore(t)
	now := time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	c := testConnection(t, s, 1)
	b := testBinding(t, s, c, "s")
	id := enqueueTest(t, s, testCall(b))
	if _, err := s.ClaimCall(t.Context(), c.Scope, b.SessionID, id); err != nil {
		t.Fatal(err)
	}
	now = c.ExpiresAt
	if err := s.Revoke(t.Context(), c.Scope); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(t.Context(), c.Scope); err != nil {
		t.Fatalf("repeated expired revocation: %v", err)
	}
	_, err := s.Renew(t.Context(), c.Scope)
	assertError(t, err, ErrRevoked)
	assertError(t, s.CheckActive(t.Context(), c.Scope), ErrRevoked)
	assertError(t, s.CompleteCall(t.Context(), c.Scope, b.SessionID, id, testResult()), ErrRevoked)
	call, err := s.GetCall(t.Context(), c.Scope, b.SessionID, id)
	if err != nil || call.State != "unknown" {
		t.Fatalf("expired revoked receipt: %+v %v", call, err)
	}
}

func TestA05ImmutableProfileBindingAndOperationReopen(t *testing.T) {
	s, path := testStore(t)
	c := testConnection(t, s, 1)
	b := testBinding(t, s, c, "s1")
	other := testConnection(t, s, 2)
	if err := s.PutBinding(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	changed := b
	changed.Profile.Instructions = "changed"
	assertError(t, s.PutBinding(t.Context(), changed), ErrConflict)
	changed = b
	changed.CreationDigest = "other"
	assertError(t, s.PutBinding(t.Context(), changed), ErrConflict)
	changed = b
	changed.Scope = other.Scope
	assertError(t, s.PutBinding(t.Context(), changed), ErrConflict)
	if _, err := s.GetBinding(t.Context(), other.Scope, b.SessionID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign binding = %v", err)
	}
	got, err := s.BindingForSession(t.Context(), b.SessionID)
	if err != nil || !reflect.DeepEqual(got, b) {
		t.Fatalf("canonical binding = %+v %v", got, err)
	}
	request := map[string]any{"action": "prompt", "profile": b.Profile}
	op, fresh, err := s.BeginOperation(t.Context(), c.Scope, "op", request)
	if err != nil || !fresh || len(op.Result) != 0 {
		t.Fatalf("begin = %+v %v %v", op, fresh, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	gotOp, fresh, err := s.BeginOperation(t.Context(), c.Scope, "op", request)
	if err != nil || fresh || !reflect.DeepEqual(gotOp, op) {
		t.Fatalf("reopen regranted dispatch = %+v %v %v", gotOp, fresh, err)
	}
	_, _, err = s.BeginOperation(t.Context(), c.Scope, "op", map[string]any{"action": "different"})
	assertError(t, err, ErrConflict)
	if _, err = s.GetOperation(t.Context(), other.Scope, "op"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign operation %v", err)
	}
	result := json.RawMessage(`{"session":"s1"}`)
	if err = s.CompleteOperation(t.Context(), c.Scope, "op", result); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteOperation(t.Context(), c.Scope, "op", result); err != nil {
		t.Fatal(err)
	}
	assertError(t, s.CompleteOperation(t.Context(), c.Scope, "op", json.RawMessage(`{"session":"s2"}`)), ErrConflict)
	if err = s.ArchiveBinding(t.Context(), c.Scope, "s1"); err != nil {
		t.Fatal(err)
	}
	if err = s.ArchiveBinding(t.Context(), c.Scope, "s1"); err != nil {
		t.Fatal(err)
	}
	assertError(t, s.PutBinding(t.Context(), b), ErrConflict)
	got, err = s.GetBinding(t.Context(), c.Scope, "s1")
	if err != nil || !got.Archived {
		t.Fatalf("archive = %+v %v", got, err)
	}
}

func TestA05ProfileRejectsInheritedAndIncompleteAuthority(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Profile)
		want   error
	}{
		{"version", func(p *Profile) { p.Version = "" }, ErrInvalid},
		{"model", func(p *Profile) { p.Model = "" }, ErrInvalid},
		{"tools-version", func(p *Profile) { p.ToolsVersion = "" }, ErrInvalid},
		{"execution", func(p *Profile) { p.Execution = "" }, ErrInvalid},
		{"all-inheritance", func(p *Profile) { p.Inherit = Inheritance{true, true, true, true} }, ErrUnsupported},
		{"duplicate", func(p *Profile) { p.Tools = append(p.Tools, p.Tools[0]) }, ErrInvalid},
		{"schema", func(p *Profile) { p.Tools[0].InputSchema = nil }, ErrInvalid},
		{"remote-schema", func(p *Profile) {
			p.Tools[0].InputSchema = map[string]any{"type": "object", "$ref": "https://invalid.example/schema"}
		}, ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { p := testProfile(); tc.change(&p); assertError(t, ValidateProfile(p), tc.want) })
	}
	err := ValidateSource(Source{Kind: "background", OperationID: "job"})
	assertError(t, err, ErrUnsupported)
	if errorcode.CodeOf(err) != errorcode.Unsupported {
		t.Fatal("unsupported code missing")
	}
}

func TestA06InvokeIntentClaimCompleteAndNativeIdentity(t *testing.T) {
	s, _ := testStore(t)
	c := testConnection(t, s, 1)
	b := testBinding(t, s, c, "s")
	source := Source{Kind: "user", OperationID: "prompt"}
	configuration, err := s.Configuration(t.Context(), c.Scope, b.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := s.ToolsForConfiguration(t.Context(), b, configuration, source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type returned struct {
		result tool.Result
		err    error
	}
	done := make(chan returned, 1)
	native := tool.Call{ID: "provider-id", Name: "WriteNote", Input: json.RawMessage(`{"note":"hello"}`), Execution: tool.InvocationContext{SessionID: b.SessionID, TurnID: "turn", ItemID: "step"}}
	go func() { r, e := tools[0].Call(ctx, native); done <- returned{r, e} }()
	calls, err := s.WaitCalls(ctx, c.Scope, b.SessionID)
	if err != nil || len(calls) != 1 {
		t.Fatalf("intent = %+v %v", calls, err)
	}
	call := calls[0]
	if call.CallID != native.ID || call.ItemID != "step" || call.TurnID != "turn" || call.Source != source || call.ToolsVersion != b.Profile.ToolsVersion {
		t.Fatalf("lost native provenance: %+v", call)
	}
	claimed, err := s.ClaimCall(ctx, c.Scope, b.SessionID, call.ID)
	if err != nil || claimed.State != "claimed" {
		t.Fatalf("claim = %+v %v", claimed, err)
	}
	_, err = s.ClaimCall(ctx, c.Scope, b.SessionID, call.ID)
	assertError(t, err, ErrAlreadyClaimed)
	if err = s.CompleteCall(ctx, c.Scope, b.SessionID, call.ID, testResult()); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.result.ID != native.ID || outcome.result.IsError {
			t.Fatalf("native result %+v %v", outcome.result, outcome.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Retrying exact completed intent returns its result without granting another claim.
	result, err := s.Invoke(ctx, call.CallContext, call.Name, call.Arguments)
	if err != nil || !reflect.DeepEqual(result, testResult()) {
		t.Fatalf("requery = %+v %v", result, err)
	}
	_, err = s.Invoke(ctx, call.CallContext, call.Name, json.RawMessage(`{"note":"different"}`))
	assertError(t, err, ErrConflict)
	changed := call.CallContext
	changed.TurnID = "other-turn"
	id := enqueueTest(t, s, changed)
	if id == call.ID {
		t.Fatal("provider id reuse aliased prior effect")
	}
	native.Execution = tool.InvocationContext{}
	native.Metadata = map[string]any{"session_id": b.SessionID, "turn_id": "forged", "item_id": "forged"}
	_, err = tools[0].Call(ctx, native)
	assertError(t, err, ErrUnauthorized)
}

func TestA06ConcurrentClaimGrantsExactlyOnce(t *testing.T) {
	s, _ := testStore(t)
	c := testConnection(t, s, 1)
	b := testBinding(t, s, c, "s")
	id := enqueueTest(t, s, testCall(b))
	const n = 12
	start := make(chan struct{})
	results := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() { <-start; _, err := s.ClaimCall(t.Context(), c.Scope, b.SessionID, id); results <- err })
	}
	close(start)
	wg.Wait()
	close(results)
	granted := 0
	for err := range results {
		if err == nil {
			granted++
		} else {
			assertError(t, err, ErrAlreadyClaimed)
		}
	}
	if granted != 1 {
		t.Fatalf("claims granted %d", granted)
	}
}

func TestA06CancellationRevocationAndLateResults(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		for _, reason := range []string{"cancel", "revoke", "archive"} {
			t.Run(fmt.Sprintf("%s/claimed=%v", reason, claimed), func(t *testing.T) {
				s, _ := testStore(t)
				c := testConnection(t, s, 1)
				b := testBinding(t, s, c, "s")
				native := testCall(b)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() { _, err := s.Invoke(ctx, native, "WriteNote", json.RawMessage(`{"note":"hello"}`)); done <- err }()
				bounded, stop := context.WithTimeout(t.Context(), 5*time.Second)
				defer stop()
				calls, err := s.WaitCalls(bounded, c.Scope, b.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				id := calls[0].ID
				if claimed {
					if _, err = s.ClaimCall(bounded, c.Scope, b.SessionID, id); err != nil {
						t.Fatal(err)
					}
				}
				switch reason {
				case "cancel":
					cancel()
				case "revoke":
					if err = s.Revoke(bounded, c.Scope); err != nil {
						t.Fatal(err)
					}
				case "archive":
					if err = s.ArchiveBinding(bounded, c.Scope, b.SessionID); err != nil {
						t.Fatal(err)
					}
				}
				select {
				case <-done:
				case <-bounded.Done():
					t.Fatal("waiter stranded")
				}
				call, err := s.GetCall(bounded, c.Scope, b.SessionID, id)
				if err != nil {
					t.Fatal(err)
				}
				want := "cancelled"
				if claimed {
					want = "unknown"
				}
				if call.State != want {
					t.Fatalf("state %s want %s", call.State, want)
				}
				if err = s.CompleteCall(bounded, c.Scope, b.SessionID, id, testResult()); err == nil {
					t.Fatal("late result revived call")
				}
				call, err = s.GetCall(bounded, c.Scope, b.SessionID, id)
				if err != nil || call.State != want || call.Result != nil {
					t.Fatalf("late mutation %+v %v", call, err)
				}
			})
		}
	}
}

func TestA06CrashRecoveryNeverRegrantsAndPreservesCompleted(t *testing.T) {
	s, path := testStore(t)
	c := testConnection(t, s, 1)
	b := testBinding(t, s, c, "s")
	base := testCall(b)
	ids := map[string]string{}
	for _, state := range []string{"pending", "claimed", "completed"} {
		cc := base
		cc.ItemID = state
		id := enqueueTest(t, s, cc)
		ids[state] = id
		if state != "pending" {
			if _, err := s.ClaimCall(t.Context(), c.Scope, b.SessionID, id); err != nil {
				t.Fatal(err)
			}
		}
		if state == "completed" {
			if err := s.CompleteCall(t.Context(), c.Scope, b.SessionID, id, testResult()); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Simulate lost process without orderly Close terminalization.
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	for original, id := range ids {
		call, err := reopened.GetCall(t.Context(), c.Scope, b.SessionID, id)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"pending": "cancelled", "claimed": "unknown", "completed": "completed"}[original]
		if call.State != want {
			t.Fatalf("%s recovered %s", original, call.State)
		}
		_, err = reopened.ClaimCall(t.Context(), c.Scope, b.SessionID, id)
		assertError(t, err, ErrAlreadyClaimed)
		if original == "completed" && !reflect.DeepEqual(call.Result, &CallResult{Outcome: "succeeded", Content: json.RawMessage(`{"recorded":true}`)}) {
			t.Fatal("completed outcome changed")
		}
	}
}

func TestA06WaiterLeaseExpiryAndCancellationWithoutPolling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := testStore(t)
		c := testConnection(t, s, 1)
		b := testBinding(t, s, c, "s")
		native := testCall(b)
		id := enqueueTest(t, s, native)
		if _, err := s.ClaimCall(t.Context(), c.Scope, b.SessionID, id); err != nil {
			t.Fatal(err)
		}
		result, err := s.Invoke(t.Context(), native, "WriteNote", json.RawMessage(`{"note":"hello"}`))
		if err != nil || result.Outcome != "unknown" {
			t.Fatalf("expired claimed invocation %+v %v", result, err)
		}
		if !time.Now().Equal(c.ExpiresAt) {
			t.Fatalf("wait completed at %s, want lease deadline %s", time.Now(), c.ExpiresAt)
		}
		call, err := s.GetCall(t.Context(), c.Scope, b.SessionID, id)
		if err != nil || call.State != "unknown" {
			t.Fatalf("expired receipt %+v %v", call, err)
		}
		_, err = s.ClaimCall(t.Context(), c.Scope, b.SessionID, id)
		assertError(t, err, ErrLeaseExpired)
	})
	s, _ := testStore(t)
	c := testConnection(t, s, 2)
	b := testBinding(t, s, c, "s2")
	canceled, stop := context.WithCancel(t.Context())
	stop()
	_, err := s.WaitCalls(canceled, c.Scope, b.SessionID)
	assertError(t, err, context.Canceled)
}

func TestA08ResourcesImmutableScopedVerifiedAndReopen(t *testing.T) {
	s, path := testStore(t)
	c := testConnection(t, s, 1)
	b := testBinding(t, s, c, "s")
	other := testConnection(t, s, 2)
	otherBinding := testBinding(t, s, c, "other-session")
	testBinding(t, s, other, "foreign")
	content := []byte("immutable resource\x00bytes")
	r, err := s.CreateResource(t.Context(), c.Scope, b.SessionID, "resource-op", "../../outside", "application/octet-stream", content)
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.CreateResource(t.Context(), c.Scope, b.SessionID, "resource-op", "../../outside", "application/octet-stream", content)
	if err != nil || r != same {
		t.Fatalf("resource retry %+v %v", same, err)
	}
	_, err = s.CreateResource(t.Context(), c.Scope, b.SessionID, "resource-op", "../../outside", "application/octet-stream", []byte("changed"))
	assertError(t, err, ErrConflict)
	for _, foreign := range []struct {
		scope   Scope
		session string
	}{{other.Scope, b.SessionID}, {c.Scope, otherBinding.SessionID}} {
		_, _, err = s.ReadResource(t.Context(), foreign.scope, foreign.session, r.ID)
		assertError(t, err, ErrNotFound)
	}
	_, err = s.CreateResource(t.Context(), c.Scope, b.SessionID, "oversized", "x", "text/plain", make([]byte, MaxResourceBytes+1))
	assertError(t, err, ErrInvalid)
	if err = s.Revoke(t.Context(), c.Scope); err != nil {
		t.Fatal(err)
	}
	got, read, err := s.ReadResource(t.Context(), c.Scope, b.SessionID, r.ID)
	if err != nil || got != r || !bytes.Equal(read, content) {
		t.Fatalf("revoked resource read %+v %q %v", got, read, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, read, err = reopened.ReadResource(t.Context(), c.Scope, b.SessionID, r.ID)
	if err != nil || got != r || !bytes.Equal(read, content) {
		t.Fatalf("resource reopen %+v %q %v", got, read, err)
	}
	if _, err = reopened.db.Exec(`UPDATE app_resources SET data=? WHERE id=?`, []byte("corrupted"), r.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = reopened.ReadResource(t.Context(), c.Scope, b.SessionID, r.ID)
	assertError(t, err, ErrInvalid)
}

func TestAdditiveSchemaGuardPreservesLegacyData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec(`CREATE TABLE bot_legacy(id TEXT PRIMARY KEY, body BLOB); INSERT INTO bot_legacy VALUES('keep',X'000102'); PRAGMA user_version=99`); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	var body []byte
	var version int
	if err = db.QueryRow(`SELECT body FROM bot_legacy WHERE id='keep'`).Scan(&body); err != nil || !bytes.Equal(body, []byte{0, 1, 2}) {
		t.Fatalf("legacy data %x %v", body, err)
	}
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 99 {
		t.Fatalf("shared schema version changed %d %v", version, err)
	}
	if _, err = db.Exec(`UPDATE app_schema SET version=99; INSERT INTO app_calls VALUES('p','a','c','s','call','digest','{}','claimed',NULL)`); err != nil {
		t.Fatal(err)
	}
	_, err = Open(path)
	assertError(t, err, ErrInvalid)
	var state string
	if err = db.QueryRow(`SELECT state FROM app_calls WHERE call='call'`).Scan(&state); err != nil || state != "claimed" {
		t.Fatalf("future schema was mutated %s %v", state, err)
	}
}

func TestA02CallsCannotCrossConnectionOrSession(t *testing.T) {
	s, _ := testStore(t)
	c := testConnection(t, s, 1)
	b := testBinding(t, s, c, "owned")
	second := testBinding(t, s, c, "same-connection-other-session")
	foreign := testConnection(t, s, 2)
	id := enqueueTest(t, s, testCall(b))
	for _, target := range []struct {
		scope   Scope
		session string
	}{{foreign.Scope, b.SessionID}, {c.Scope, second.SessionID}} {
		_, err := s.GetCall(t.Context(), target.scope, target.session, id)
		assertError(t, err, ErrNotFound)
		_, err = s.ClaimCall(t.Context(), target.scope, target.session, id)
		assertError(t, err, ErrNotFound)
		assertError(t, s.CompleteCall(t.Context(), target.scope, target.session, id, testResult()), ErrNotFound)
		assertError(t, s.CancelCall(t.Context(), target.scope, target.session, id), ErrNotFound)
	}
	_, err := s.ListCalls(t.Context(), foreign.Scope, b.SessionID)
	assertError(t, err, ErrNotFound)
	call, err := s.GetCall(t.Context(), c.Scope, b.SessionID, id)
	if err != nil || call.State != "pending" {
		t.Fatalf("cross-scope access changed call: %+v %v", call, err)
	}
}

func TestA06InvalidAdmissionPersistsNoIntent(t *testing.T) {
	s, _ := testStore(t)
	c := testConnection(t, s, 1)
	b := testBinding(t, s, c, "s")
	base := testCall(b)
	cases := []struct {
		name   string
		change func(*CallContext)
		tool   string
		args   string
		want   error
	}{
		{"unknown-tool", nil, "RunCommand", `{"note":"hello"}`, ErrUnauthorized},
		{"extra-argument", nil, "WriteNote", `{"note":"hello","scope":"forged"}`, ErrInvalid},
		{"missing-argument", nil, "WriteNote", `{}`, ErrInvalid},
		{"wrong-type", nil, "WriteNote", `{"note":7}`, ErrInvalid},
		{"malformed", nil, "WriteNote", `{"note":`, ErrInvalid},
		{"missing-item", func(c *CallContext) { c.ItemID = "" }, "WriteNote", `{"note":"hello"}`, ErrInvalid},
		{"tools-version", func(c *CallContext) { c.ToolsVersion = "changed" }, "WriteNote", `{"note":"hello"}`, ErrConflict},
		{"background", func(c *CallContext) { c.Source.Kind = "background" }, "WriteNote", `{"note":"hello"}`, ErrUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc := base
			if tc.change != nil {
				tc.change(&cc)
			}
			_, err := s.Invoke(t.Context(), cc, tc.tool, json.RawMessage(tc.args))
			assertError(t, err, tc.want)
		})
	}
	calls, err := s.ListCalls(t.Context(), c.Scope, b.SessionID)
	if err != nil || len(calls) != 0 {
		t.Fatalf("invalid intents persisted %+v %v", calls, err)
	}
}

func TestA06ClaimRevocationSerializedOutcome(t *testing.T) {
	s, _ := testStore(t)
	c := testConnection(t, s, 1)
	b := testBinding(t, s, c, "s")
	id := enqueueTest(t, s, testCall(b))
	start := make(chan struct{})
	claimed := make(chan error, 1)
	revoked := make(chan error, 1)
	go func() { <-start; _, err := s.ClaimCall(t.Context(), c.Scope, b.SessionID, id); claimed <- err }()
	go func() { <-start; revoked <- s.Revoke(t.Context(), c.Scope) }()
	close(start)
	claimErr := <-claimed
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	want := "unknown"
	if claimErr != nil {
		assertError(t, claimErr, ErrRevoked)
		want = "cancelled"
	}
	call, err := s.GetCall(t.Context(), c.Scope, b.SessionID, id)
	if err != nil || call.State != want {
		t.Fatalf("claim/revoke state %+v %v want %s", call, err, want)
	}
}

func TestA05ConcurrentOperationDispatchGrantedOnce(t *testing.T) {
	s, _ := testStore(t)
	c := testConnection(t, s, 1)
	start := make(chan struct{})
	results := make(chan bool, 12)
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			<-start
			_, fresh, err := s.BeginOperation(t.Context(), c.Scope, "one", map[string]any{"action": "create"})
			results <- fresh
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	granted := 0
	for fresh := range results {
		if fresh {
			granted++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if granted != 1 {
		t.Fatalf("operation dispatch grants %d", granted)
	}
}

func TestA06WaitCallsUsesLeaseDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := testStore(t)
		c := testConnection(t, s, 1)
		b := testBinding(t, s, c, "s")
		_, err := s.WaitCalls(t.Context(), c.Scope, b.SessionID)
		assertError(t, err, ErrLeaseExpired)
		if !time.Now().Equal(c.ExpiresAt) {
			t.Fatalf("wait completed at %s, want lease deadline %s", time.Now(), c.ExpiresAt)
		}
	})
}

func TestA06RenewalMovesWaitingCallDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := testStore(t)
		c := testConnection(t, s, 1)
		b := testBinding(t, s, c, "s")
		native := testCall(b)
		id := enqueueTest(t, s, native)
		if _, err := s.ClaimCall(t.Context(), c.Scope, b.SessionID, id); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := s.Invoke(t.Context(), native, "WriteNote", json.RawMessage(`{"note":"hello"}`))
			done <- err
		}()
		synctest.Wait()
		<-time.After(LeaseDuration / 2)
		renewed, err := s.Renew(t.Context(), c.Scope)
		if err != nil {
			t.Fatal(err)
		}
		if err = <-done; err != nil {
			t.Fatal(err)
		}
		if !time.Now().Equal(renewed.ExpiresAt) {
			t.Fatalf("renewed wait completed at %s, want %s", time.Now(), renewed.ExpiresAt)
		}
	})
}

func TestA05CreationIntentRecoversOwnedTargetWithoutRedispatch(t *testing.T) {
	s, path := testStore(t)
	c := testConnection(t, s, 1)
	request := struct {
		Action  string  `json:"action"`
		Profile Profile `json:"profile"`
	}{"create", testProfile()}
	op, fresh, err := s.BeginOperation(t.Context(), c.Scope, "create", request)
	if err != nil || !fresh {
		t.Fatalf("create intent %+v %v %v", op, fresh, err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(op.Request, encoded) {
		t.Fatalf("intent request %s, want %s", op.Request, encoded)
	}
	binding := Binding{Scope: c.Scope, SessionID: SessionID(c.Scope, op.ID), Profile: request.Profile, CreationDigest: op.Digest}
	if err = s.PutBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	// The create effect is proven by binding; the operation reply was lost.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	recovered, err := reopened.GetOperation(t.Context(), c.Scope, op.ID)
	if err != nil || len(recovered.Result) != 0 || !bytes.Equal(recovered.Request, encoded) {
		t.Fatalf("lost receipt request %+v %v", recovered, err)
	}
	target, err := reopened.GetBinding(t.Context(), c.Scope, SessionID(c.Scope, recovered.ID))
	if err != nil || !reflect.DeepEqual(target, binding) || target.CreationDigest != recovered.Digest {
		t.Fatalf("recovered creation target %+v %v", target, err)
	}
	_, fresh, err = reopened.BeginOperation(t.Context(), c.Scope, op.ID, request)
	if err != nil || fresh {
		t.Fatalf("lost reply regranted effect %v %v", fresh, err)
	}
	for _, other := range []Scope{{PrincipalID: "other", ApplicationID: c.ApplicationID, ConnectionID: c.ConnectionID}, {PrincipalID: c.PrincipalID, ApplicationID: "other", ConnectionID: c.ConnectionID}, {PrincipalID: c.PrincipalID, ApplicationID: c.ApplicationID, ConnectionID: "other"}} {
		if SessionID(other, op.ID) == binding.SessionID {
			t.Fatal("different scope aliases session")
		}
	}
	if SessionID(c.Scope, "different-op") == binding.SessionID {
		t.Fatal("different operation aliases session")
	}
	if _, err = reopened.db.Exec(`UPDATE app_operations SET request=? WHERE connection=? AND operation=?`, []byte(`{"action":"different"}`), c.ConnectionID, op.ID); err != nil {
		t.Fatal(err)
	}
	_, err = reopened.GetOperation(t.Context(), c.Scope, op.ID)
	assertError(t, err, ErrInvalid)
}

func TestSchemaRejectsMissingIntentShapeWithoutRecoveryMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incompatible.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE app_schema(singleton INTEGER PRIMARY KEY,version INTEGER NOT NULL);
 INSERT INTO app_schema VALUES(1,1);
 CREATE TABLE app_operations(principal TEXT,application TEXT,connection TEXT,operation TEXT,digest TEXT,result BLOB);
 INSERT INTO app_operations VALUES('p','a','c','op','digest',NULL);
 CREATE TABLE app_calls(state TEXT);
 INSERT INTO app_calls VALUES('claimed');`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(path)
	assertError(t, err, ErrInvalid)
	var state string
	if err = db.QueryRow(`SELECT state FROM app_calls`).Scan(&state); err != nil || state != "claimed" {
		t.Fatalf("incompatible intent schema mutated calls: %s %v", state, err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name GLOB 'app_*'`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("failed Open left schema changes %d %v", count, err)
	}
}
