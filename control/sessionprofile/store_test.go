package sessionprofile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
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

type testPrincipal struct{ Scope }

func testOwner(t *testing.T, s *Store, seed int) testPrincipal {
	t.Helper()
	return testPrincipal{PrincipalScope(fmt.Sprintf("principal-%d", seed))}
}
func testBinding(t *testing.T, s *Store, c testPrincipal, id string) Binding {
	t.Helper()
	b := Binding{Scope: c.Scope, SessionID: id, Profile: testProfile(), CreationDigest: "creation-" + id}
	if err := s.PutBinding(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	return b
}
func testCall(b Binding) CallContext {
	return CallContext{Scope: b.Scope, SessionID: b.SessionID, TurnID: "turn-1", ItemID: "item-1", CallID: "provider-call", ToolsVersion: b.Profile.ToolsVersion, ConfigurationRevision: 1, Source: Source{Kind: "user", OperationID: "prompt-1"}}
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

func TestApplicationTerminalResultPersistsOnOriginalCall(t *testing.T) {
	s, path := testStore(t)
	connection := testOwner(t, s, 9123)
	binding := testBinding(t, s, connection, "terminal-session")
	provenance := testCall(binding)
	id := enqueueTest(t, s, provenance)
	if _, err := s.ClaimCall(t.Context(), connection.Scope, binding.SessionID, id); err != nil {
		t.Fatal(err)
	}
	invalid := CallResult{Outcome: "unknown", Content: json.RawMessage(`null`), TurnComplete: true}
	assertError(t, s.CompleteCall(t.Context(), connection.Scope, binding.SessionID, id, invalid), ErrInvalid)
	result := CallResult{Outcome: "succeeded", Content: json.RawMessage(`{"accepted":true}`), TurnComplete: true}
	if err := s.CompleteCall(t.Context(), connection.Scope, binding.SessionID, id, result); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteCall(t.Context(), connection.Scope, binding.SessionID, id, result); err != nil {
		t.Fatalf("same original receipt was not idempotent: %v", err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	call, err := reopened.GetCall(t.Context(), connection.Scope, binding.SessionID, id)
	if err != nil || call.State != "completed" || call.Result == nil || !call.Result.TurnComplete || call.ID != id {
		t.Fatalf("recovered original call = %+v, %v", call, err)
	}
	projected, err := reopened.projectResult(t.Context(), provenance, "WriteNote", "", *call.Result)
	if err != nil || !projected.TurnComplete || projected.ID != provenance.CallID {
		t.Fatalf("projected terminal result = %+v, %v", projected, err)
	}
}
func assertError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
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
	c := testOwner(t, s, 1)
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
	c := testOwner(t, s, 1)
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

func TestA06CrashRecoveryNeverRegrantsAndPreservesCompleted(t *testing.T) {
	s, path := testStore(t)
	c := testOwner(t, s, 1)
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

func TestA08ResourcesImmutableScopedVerifiedAndReopen(t *testing.T) {
	s, path := testStore(t)
	c := testOwner(t, s, 1)
	b := testBinding(t, s, c, "s")
	other := testOwner(t, s, 2)
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
	if err = s.ArchiveBinding(t.Context(), c.Scope, b.SessionID); err != nil {
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
	if _, err = reopened.db.Exec(`UPDATE session_resources SET data=? WHERE id=?`, []byte("corrupted"), r.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = reopened.ReadResource(t.Context(), c.Scope, b.SessionID, r.ID)
	assertError(t, err, ErrInvalid)
}

func TestA02CallsCannotCrossConnectionOrSession(t *testing.T) {
	s, _ := testStore(t)
	c := testOwner(t, s, 1)
	b := testBinding(t, s, c, "owned")
	second := testBinding(t, s, c, "same-connection-other-session")
	foreign := testOwner(t, s, 2)
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
	c := testOwner(t, s, 1)
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
