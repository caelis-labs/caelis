package application

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestCompleteOperationKnownResultAfterLeaseEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(*testing.T, *Store, Connection, *time.Time)
		want error
	}{
		{
			name: "expiry",
			end: func(_ *testing.T, _ *Store, c Connection, now *time.Time) {
				*now = c.ExpiresAt.Add(time.Second)
			},
			want: ErrLeaseExpired,
		},
		{
			name: "revocation",
			end: func(t *testing.T, s *Store, c Connection, _ *time.Time) {
				if err := s.Revoke(t.Context(), c.Scope); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrRevoked,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path := testStore(t)
			now := time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return now }
			owner := testConnection(t, s, 913)
			other := testConnection(t, s, 914)
			binding := testBinding(t, s, owner, "session")
			callID := enqueueTest(t, s, testCall(binding))
			if _, err := s.ClaimCall(t.Context(), owner.Scope, binding.SessionID, callID); err != nil {
				t.Fatal(err)
			}
			request := map[string]string{"action": "prompt"}
			admitted, fresh, err := s.BeginOperation(t.Context(), owner.Scope, "known", request)
			if err != nil || !fresh {
				t.Fatalf("admit operation: %+v fresh=%t err=%v", admitted, fresh, err)
			}
			_, _, err = s.BeginOperation(t.Context(), owner.Scope, admitted.ID, map[string]string{"action": "different"})
			assertError(t, err, ErrConflict)
			assertError(t, s.CompleteOperation(t.Context(), other.Scope, admitted.ID, json.RawMessage(`{"outcome":"other"}`)), ErrNotFound)
			_, fresh, err = s.BeginOperation(t.Context(), other.Scope, admitted.ID, map[string]string{"action": "other"})
			if err != nil || !fresh {
				t.Fatalf("other connection admission: fresh=%t err=%v", fresh, err)
			}
			foreignResult := json.RawMessage(`{"outcome":"other"}`)
			if err := s.CompleteOperation(t.Context(), other.Scope, admitted.ID, foreignResult); err != nil {
				t.Fatal(err)
			}

			tc.end(t, s, owner, &now)
			_, _, err = s.BeginOperation(t.Context(), owner.Scope, admitted.ID, request)
			assertError(t, err, tc.want)
			assertError(t, s.CompleteCall(t.Context(), owner.Scope, binding.SessionID, callID, testResult()), tc.want)
			result := json.RawMessage(`{"outcome":"committed"}`)
			if err := s.CompleteOperation(t.Context(), owner.Scope, admitted.ID, result); err != nil {
				t.Fatalf("complete admitted operation after %s: %v", tc.name, err)
			}
			if err := s.CompleteOperation(t.Context(), owner.Scope, admitted.ID, result); err != nil {
				t.Fatalf("same result must be idempotent: %v", err)
			}
			assertError(t, s.CompleteOperation(t.Context(), owner.Scope, admitted.ID, json.RawMessage(`{"outcome":"different"}`)), ErrConflict)
			assertError(t, s.CompleteOperation(t.Context(), owner.Scope, "missing", result), ErrNotFound)
			for _, foreign := range []Scope{
				{PrincipalID: "other", ApplicationID: owner.ApplicationID, ConnectionID: owner.ConnectionID},
				{PrincipalID: owner.PrincipalID, ApplicationID: "other", ConnectionID: owner.ConnectionID},
				{PrincipalID: owner.PrincipalID, ApplicationID: owner.ApplicationID, ConnectionID: other.ConnectionID},
			} {
				assertError(t, s.CompleteOperation(t.Context(), foreign, admitted.ID, result), ErrUnauthorized)
				_, err := s.GetOperation(t.Context(), foreign, admitted.ID)
				assertError(t, err, ErrUnauthorized)
			}
			own, err := s.GetOperation(t.Context(), owner.Scope, admitted.ID)
			if err != nil || own.ID != admitted.ID || own.Digest != admitted.Digest || !bytes.Equal(own.Request, admitted.Request) || !bytes.Equal(own.Result, result) {
				t.Fatalf("owner's original request/result changed: %+v %v", own, err)
			}
			foreign, err := s.GetOperation(t.Context(), other.Scope, admitted.ID)
			if err != nil || !bytes.Equal(foreign.Result, foreignResult) || foreign.Digest == admitted.Digest {
				t.Fatalf("connection results crossed scopes: %+v %v", foreign, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			reopened.now = func() time.Time { return now }
			persisted, err := reopened.GetOperation(t.Context(), owner.Scope, admitted.ID)
			if err != nil || persisted.Digest != admitted.Digest || !bytes.Equal(persisted.Request, admitted.Request) || !bytes.Equal(persisted.Result, result) {
				t.Fatalf("reopened known result: %+v %v", persisted, err)
			}
			if tc.name == "expiry" {
				if _, err := reopened.Renew(t.Context(), owner.Scope); err != nil {
					t.Fatal(err)
				}
				replayed, fresh, err := reopened.BeginOperation(t.Context(), owner.Scope, admitted.ID, request)
				if err != nil || fresh || !bytes.Equal(replayed.Result, result) || !bytes.Equal(replayed.Request, admitted.Request) {
					t.Fatalf("renewal regranted dispatch or lost result: %+v fresh=%t err=%v", replayed, fresh, err)
				}
				_, _, err = reopened.BeginOperation(t.Context(), owner.Scope, admitted.ID, map[string]string{"action": "different"})
				assertError(t, err, ErrConflict)
				assertError(t, reopened.CompleteCall(t.Context(), owner.Scope, binding.SessionID, callID, testResult()), ErrAlreadyClaimed)
			} else {
				_, err := reopened.Renew(t.Context(), owner.Scope)
				assertError(t, err, ErrRevoked)
				_, _, err = reopened.BeginOperation(t.Context(), owner.Scope, admitted.ID, request)
				assertError(t, err, ErrRevoked)
				assertError(t, reopened.CompleteCall(t.Context(), owner.Scope, binding.SessionID, callID, testResult()), ErrRevoked)
			}
		})
	}
}
