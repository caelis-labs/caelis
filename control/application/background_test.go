package application

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestBackgroundGrantDurabilityScopeCollisionAndRevocation(t *testing.T) {
	s, path := testStore(t)
	ctx := t.Context()
	first := testConnection(t, s, 91)
	other := testConnection(t, s, 92)
	firstBinding := testBinding(t, s, first, "worker-one")
	otherBinding := testBinding(t, s, first, "worker-two")
	testBinding(t, s, other, "another-connection")
	req := BackgroundGrantRequest{OperationID: "grant-op", Source: "authorized-timer-42", AuthorizationOperationID: "user-opt-in-42"}
	if _, err := s.CreateBackgroundGrant(ctx, first.Scope, "missing", req); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Session: %v", err)
	}
	grant, err := s.CreateBackgroundGrant(ctx, first.Scope, firstBinding.SessionID, req)
	if err != nil || grant.ID == "" || grant.Revoked || grant.Source != req.Source || grant.AuthorizationOperationID != req.AuthorizationOperationID || grant.Scope != first.Scope {
		t.Fatalf("created grant: %+v %v", grant, err)
	}
	retry, err := s.CreateBackgroundGrant(ctx, first.Scope, firstBinding.SessionID, req)
	if err != nil || retry != grant {
		t.Fatalf("idempotent retry: %+v %v", retry, err)
	}
	if _, err := s.CreateBackgroundGrant(ctx, first.Scope, otherBinding.SessionID, req); !errors.Is(err, ErrConflict) {
		t.Fatalf("same op different Session: %v", err)
	}
	for _, changed := range []BackgroundGrantRequest{
		{OperationID: req.OperationID, Source: "other-timer", AuthorizationOperationID: req.AuthorizationOperationID},
		{OperationID: req.OperationID, Source: req.Source, AuthorizationOperationID: "other-opt-in"},
	} {
		if _, err := s.CreateBackgroundGrant(ctx, first.Scope, firstBinding.SessionID, changed); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed intent: %v", err)
		}
	}
	if _, _, err := s.BeginOperation(ctx, first.Scope, "other-mutation", map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBackgroundGrant(ctx, first.Scope, firstBinding.SessionID, BackgroundGrantRequest{OperationID: "other-mutation", Source: "timer", AuthorizationOperationID: "opt"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("grant operation collision: %v", err)
	}
	if _, _, err := s.BeginOperation(ctx, first.Scope, req.OperationID, map[string]any{"kind": "prompt"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("prompt operation collision: %v", err)
	}
	for _, mismatch := range []struct {
		scope   Scope
		session string
	}{{other.Scope, firstBinding.SessionID}, {first.Scope, otherBinding.SessionID}} {
		if _, err := s.GetBackgroundGrant(ctx, mismatch.scope, mismatch.session, grant.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-scope grant read: %v", err)
		}
		if _, err := s.AdmitBackgroundSource(ctx, mismatch.scope, mismatch.session, grant.ID, "prompt"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-scope grant admission: %v", err)
		}
	}
	source, err := s.AdmitBackgroundSource(ctx, first.Scope, firstBinding.SessionID, grant.ID, "prompt")
	if err != nil || source != (Source{Kind: "authorized_background", OperationID: "prompt", GrantID: grant.ID, AuthorizedSource: req.Source}) || ValidateSource(source) != nil {
		t.Fatalf("admitted source: %+v %v", source, err)
	}
	if _, err := s.AdmitBackgroundSource(ctx, first.Scope, firstBinding.SessionID, "missing", "prompt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing grant admission: %v", err)
	}
	out, err := s.ListBackgroundGrants(ctx, first.Scope, firstBinding.SessionID)
	if err != nil || !reflect.DeepEqual(out, []BackgroundGrant{grant}) {
		t.Fatalf("list: %+v %v", out, err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = s.AdmitBackgroundSource(ctx, first.Scope, firstBinding.SessionID, grant.ID, "in-flight")
	}()
	revoked, err := s.RevokeBackgroundGrant(ctx, first.Scope, firstBinding.SessionID, grant.ID)
	wg.Wait()
	if err != nil || !revoked.Revoked {
		t.Fatalf("revoke: %+v %v", revoked, err)
	}
	if _, err := s.AdmitBackgroundSource(ctx, first.Scope, firstBinding.SessionID, grant.ID, "new-prompt"); !errors.Is(err, ErrRevoked) {
		t.Fatalf("admission after revocation: %v", err)
	}
	got, err := s.CreateBackgroundGrant(ctx, first.Scope, firstBinding.SessionID, req)
	if err != nil || got != revoked {
		t.Fatalf("retry revived grant: %+v %v", got, err)
	}
	if err := s.ArchiveBinding(ctx, first.Scope, firstBinding.SessionID); err != nil {
		t.Fatal(err)
	}
	got, err = s.CreateBackgroundGrant(ctx, first.Scope, firstBinding.SessionID, req)
	if err != nil || got != revoked {
		t.Fatalf("archived Session lost original grant receipt: %+v %v", got, err)
	}
	if _, err := s.CreateBackgroundGrant(ctx, first.Scope, firstBinding.SessionID, BackgroundGrantRequest{OperationID: "new-after-archive", Source: "schedule", AuthorizationOperationID: "opt-in"}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("created new grant after archive: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.GetBackgroundGrant(ctx, first.Scope, firstBinding.SessionID, grant.ID)
	if err != nil || persisted != revoked {
		t.Fatalf("reopened grant: %+v %v", persisted, err)
	}
	if _, err := reopened.AdmitBackgroundSource(ctx, first.Scope, firstBinding.SessionID, grant.ID, "after-restart"); !errors.Is(err, ErrRevoked) {
		t.Fatalf("reopen admitted revoked grant: %v", err)
	}
}

func TestBackgroundGrantRequiresExactAuthorizationAttestation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	connection := testConnection(t, s, 93)
	binding := testBinding(t, s, connection, "worker")
	for _, req := range []BackgroundGrantRequest{
		{OperationID: "", Source: "schedule", AuthorizationOperationID: "opt-in"},
		{OperationID: "grant", Source: " ", AuthorizationOperationID: "opt-in"},
		{OperationID: "grant", Source: "schedule", AuthorizationOperationID: ""},
	} {
		if _, err := s.CreateBackgroundGrant(context.Background(), connection.Scope, binding.SessionID, req); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted incomplete authorization %+v: %v", req, err)
		}
	}
	if ValidateSource(Source{Kind: "authorized_background", OperationID: "prompt", GrantID: "grant"}) == nil ||
		ValidateSource(Source{Kind: "application_summary", OperationID: "prompt", GrantID: "grant"}) == nil {
		t.Fatal("accepted model summary or incomplete background source as authority")
	}
}
