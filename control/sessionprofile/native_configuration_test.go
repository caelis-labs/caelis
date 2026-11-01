package sessionprofile

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/internal/operationreceipt"
)

func TestNativeConfigurationSelectionOwnershipAndReceiptRollback(t *testing.T) {
	s, _ := testStore(t)
	owner := testOwner(t, s, 1)
	active := session.Session{SessionRef: session.SessionRef{SessionID: "ordinary", UserID: owner.PrincipalID}}
	initial, err := s.NativeConfiguration(t.Context(), owner.Scope, active)
	if err != nil || initial.Native == nil || initial.Native.Plugins != nil || initial.Revision != 1 {
		t.Fatalf("initial inheritance: %+v %v", initial, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM session_profiles`).Scan(&count); err != nil || count != 0 {
		t.Fatal("read created configuration", err)
	}
	empty := []Plugin{}
	request := UpdateConfigurationRequest{OperationID: "disable", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{Plugins: &empty}}
	admitted := false
	if _, err := s.UpdateNativeConfiguration(mutationContext(t.Context()), PrincipalScope("foreign"), active, request, func(context.Context, []Plugin) error { admitted = true; return nil }); !errors.Is(err, ErrUnauthorized) || admitted {
		t.Fatal("unauthorized native mutation reached admission", err)
	}
	failure := errors.New("receipt commit failed")
	failedReceipt := operationreceipt.WithTransaction(t.Context(), func(context.Context, *sql.Tx, []byte) error { return failure })
	if _, err := s.UpdateNativeConfiguration(failedReceipt, owner.Scope, active, request, nil); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM session_profiles`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed receipt left native selection", err)
	}
	committed, err := s.UpdateNativeConfiguration(mutationContext(t.Context()), owner.Scope, active, request, nil)
	if err != nil || committed.Revision != 2 || committed.Native.Plugins == nil || len(*committed.Native.Plugins) != 0 {
		t.Fatalf("clear: %+v %v", committed, err)
	}
	if _, err := s.UpdateNativeConfiguration(mutationContext(t.Context()), owner.Scope, active, request, func(context.Context, []Plugin) error { admitted = true; return nil }); !errors.Is(err, ErrConfigurationStale) || admitted {
		t.Fatal("stale native mutation reached admission", err)
	}
	for _, check := range []func() error{
		func() error { _, err := s.GetBinding(t.Context(), owner.Scope, active.SessionID); return err },
		func() error { _, err := s.BindingForSession(t.Context(), active.SessionID); return err },
		func() error { _, err := s.Configuration(t.Context(), owner.Scope, active.SessionID); return err },
	} {
		if err := check(); !errors.Is(err, ErrNotFound) {
			t.Fatal("ordinary native adjunct became explicit Profile", err)
		}
	}
	bindings, err := s.ListBindings(t.Context(), owner.Scope)
	if err != nil || len(bindings) != 0 {
		t.Fatal("native adjunct appeared in profile list", err)
	}
	if _, err := s.NativeConfiguration(t.Context(), PrincipalScope("foreign"), active); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("foreign native configuration read", err)
	}
	request.ExpectedConfigurationRevision = 2
	request.Patch = ConfigurationPatch{Model: pointer("foreign/model")}
	if _, err := s.UpdateNativeConfiguration(mutationContext(t.Context()), owner.Scope, active, request, nil); !errors.Is(err, ErrUnsupported) {
		t.Fatal("native profile copied model authority", err)
	}
	request.Patch = ConfigurationPatch{Plugins: &empty}
	noop, err := s.UpdateNativeConfiguration(mutationContext(t.Context()), owner.Scope, active, request, nil)
	if err != nil || noop.Revision != 2 {
		t.Fatal("native no-op changed revision", err)
	}
	read, err := s.NativeConfiguration(t.Context(), owner.Scope, active)
	if err != nil || read.Native.Plugins == nil {
		t.Fatal("native [] lost on database read", err)
	}
}

func TestNativeConfigurationWirePresence(t *testing.T) {
	empty := []Plugin{}
	for _, selected := range []*[]Plugin{nil, &empty} {
		value := Configuration{SessionID: "ordinary", Revision: 1, Native: &NativeConfiguration{Plugins: selected}}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]json.RawMessage
		_ = json.Unmarshal(raw, &object)
		if _, ok := object["profile"]; ok {
			t.Fatal("native selector fabricated Profile", string(raw))
		}
		var read Configuration
		if err := json.Unmarshal(raw, &read); err != nil || read.Native == nil || (read.Native.Plugins == nil) != (selected == nil) {
			t.Fatalf("native selector presence: %s %v", raw, err)
		}
	}
	var bad NativeConfiguration
	if err := json.Unmarshal([]byte(`{"plugins":null}`), &bad); err == nil {
		t.Fatal("null native selection became inheritance")
	}
	for _, raw := range []string{`{"session_id":"ordinary","revision":1,"native":null}`, `{"session_id":"ordinary","revision":1,"native":{},"profile":{}}`} {
		var mixed Configuration
		if err := json.Unmarshal([]byte(raw), &mixed); err == nil {
			t.Fatal("ambiguous native configuration accepted", raw)
		}
	}
}
