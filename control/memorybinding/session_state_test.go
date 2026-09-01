package memorybinding

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestSessionAdmissionPinsActorAudienceAndCausalCursor(t *testing.T) {
	ctx := context.Background()
	store := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "session-memory",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := runtimeSnapshotForState(1, "endpoint-a", "view-a", OutputAudiencePrivate, "actor-a")
	if err := AdmitSession(ctx, store, active.SessionRef, binding); err != nil {
		t.Fatal(err)
	}
	fence, err := store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef, OwnerID: "owner-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeCtx := session.ContextWithRuntimeFence(ctx, fence)
	if err := AdvanceConsistency(runtimeCtx, store, active.SessionRef, binding, "token-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseSessionFence(ctx, session.SessionFenceReleaseRequest(fence)); err != nil {
		t.Fatal(err)
	}
	if token, err := ConsistencyToken(ctx, store, active.SessionRef, binding); err != nil || token != "token-a" {
		t.Fatalf("ConsistencyToken() = %q, %v", token, err)
	}
	newer := runtimeSnapshotForState(2, "endpoint-a", "view-a", OutputAudiencePrivate, "actor-a")
	if err := AdmitSession(ctx, store, active.SessionRef, newer); err != nil {
		t.Fatal(err)
	}
	if token, err := ConsistencyToken(ctx, store, active.SessionRef, newer); err != nil || token != "token-a" {
		t.Fatalf("newer same-View token = %q, %v", token, err)
	}
	moved := runtimeSnapshotForState(3, "endpoint-b", "view-b", OutputAudiencePrivate, "actor-a")
	if err := AdmitSession(ctx, store, active.SessionRef, moved); err != nil {
		t.Fatal(err)
	}
	if token, err := ConsistencyToken(ctx, store, active.SessionRef, moved); err != nil || token != "" {
		t.Fatalf("moved binding token = %q, %v", token, err)
	}
}

func TestSessionAdmissionRejectsAudienceActorDowngradeAndUnversionedDrift(t *testing.T) {
	ctx := context.Background()
	store := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user", PreferredSessionID: "session-memory"})
	if err != nil {
		t.Fatal(err)
	}
	base := runtimeSnapshotForState(2, "endpoint-a", "view-a", OutputAudiencePrivate, "actor-a")
	if err := AdmitSession(ctx, store, active.SessionRef, base); err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]RuntimeMemoryBindingSnapshot{
		"audience":           runtimeSnapshotForState(3, "endpoint-a", "view-a", OutputAudienceShared, "actor-a"),
		"actor":              runtimeSnapshotForState(3, "endpoint-a", "view-a", OutputAudiencePrivate, "actor-b"),
		"downgrade":          runtimeSnapshotForState(1, "endpoint-a", "view-a", OutputAudiencePrivate, "actor-a"),
		"same version drift": runtimeSnapshotForState(2, "endpoint-a", "view-b", OutputAudiencePrivate, "actor-a"),
		"binding": func() RuntimeMemoryBindingSnapshot {
			changed := runtimeSnapshotForState(3, "endpoint-a", "view-a", OutputAudiencePrivate, "actor-a")
			changed.BindingRef = "binding-b"
			return changed
		}(),
		"principal": func() RuntimeMemoryBindingSnapshot {
			changed := runtimeSnapshotForState(3, "endpoint-a", "view-a", OutputAudiencePrivate, "actor-a")
			changed.PrincipalRef = "principal:b"
			return changed
		}(),
		"same version grant": func() RuntimeMemoryBindingSnapshot {
			changed := base
			changed.GrantRef = "grant-b"
			return changed
		}(),
		"same version issuer": func() RuntimeMemoryBindingSnapshot {
			changed := base
			changed.IssuerCredentialRef = "memory-issuer:b"
			return changed
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := AdmitSession(ctx, store, active.SessionRef, changed); !errors.Is(err, ErrSessionAdmissionConflict) {
				t.Fatalf("AdmitSession() error = %v, want ErrSessionAdmissionConflict", err)
			}
		})
	}
}

func TestSessionAdmissionRejectsMalformedPersistedState(t *testing.T) {
	ctx := context.Background()
	store := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user", PreferredSessionID: "session-memory"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
		Update: func(state map[string]any) (map[string]any, error) {
			state[SessionStateKey] = map[string]any{"version": 999, "consistency_token": strings.Repeat("x", 32)}
			return state, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := AdmitSession(ctx, store, active.SessionRef, runtimeSnapshotForState(1, "endpoint-a", "view-a", OutputAudiencePrivate, "actor-a")); err == nil {
		t.Fatal("AdmitSession() repaired malformed authority state")
	}
}

func TestSessionAdmissionRejectsLegacyPartialAuthorityState(t *testing.T) {
	ctx := context.Background()
	store := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user", PreferredSessionID: "session-memory"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
		Update: func(state map[string]any) (map[string]any, error) {
			state[SessionStateKey] = map[string]any{
				"version": 1, "endpoint_id": "endpoint-a", "runtime_actor_ref": "actor-a",
				"audience": "private", "view_ref": "view-a", "binding_version": 1,
			}
			return state, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := AdmitSession(ctx, store, active.SessionRef, runtimeSnapshotForState(1, "endpoint-a", "view-a", OutputAudiencePrivate, "actor-a")); err == nil {
		t.Fatal("AdmitSession() widened a legacy partial authority pin")
	}
}

func TestPrepareConsistencyPinsLegacySessionUnderRuntimeFence(t *testing.T) {
	ctx := context.Background()
	store := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user", PreferredSessionID: "legacy-session"})
	if err != nil {
		t.Fatal(err)
	}
	binding := runtimeSnapshotForState(1, "endpoint-a", "view-a", OutputAudiencePrivate, "actor-a")
	if err := ValidateSessionAdmission(ctx, store, active.SessionRef, binding); err != nil {
		t.Fatal(err)
	}
	fence, err := store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "owner-a"})
	if err != nil {
		t.Fatal(err)
	}
	runtimeCtx := session.ContextWithRuntimeFence(ctx, fence)
	if token, err := PrepareConsistency(runtimeCtx, store, active.SessionRef, binding); err != nil || token != "" {
		t.Fatalf("PrepareConsistency() = %q, %v", token, err)
	}
	if err := store.ReleaseSessionFence(ctx, session.SessionFenceReleaseRequest(fence)); err != nil {
		t.Fatal(err)
	}
	if token, err := ConsistencyToken(ctx, store, active.SessionRef, binding); err != nil || token != "" {
		t.Fatalf("ConsistencyToken() = %q, %v", token, err)
	}
}

func runtimeSnapshotForState(version uint64, endpoint, view string, audience OutputAudience, actor RuntimeActorRef) RuntimeMemoryBindingSnapshot {
	return RuntimeMemoryBindingSnapshot{
		Endpoint: EndpointConfig{ID: endpoint}, BindingRef: "binding-a", RuntimeActorRef: actor,
		PrincipalRef: "principal:a", IssuerCredentialRef: "memory-issuer:a",
		ViewRef: view, GrantRef: "grant-a", Audience: audience, BindingVersion: version,
	}
}
