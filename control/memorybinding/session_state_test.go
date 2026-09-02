package memorybinding

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	memoryv1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
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
	binding := runtimeSnapshotForState(1, "view-a", OutputAudiencePrivate, "actor-a")
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
	newer := runtimeSnapshotForState(2, "view-a", OutputAudiencePrivate, "actor-a")
	if err := AdmitSession(ctx, store, active.SessionRef, newer); err != nil {
		t.Fatal(err)
	}
	if token, err := ConsistencyToken(ctx, store, active.SessionRef, newer); err != nil || token != "token-a" {
		t.Fatalf("newer same-View token = %q, %v", token, err)
	}
	moved := runtimeSnapshotForState(3, "view-b", OutputAudiencePrivate, "actor-a")
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
	base := runtimeSnapshotForState(2, "view-a", OutputAudiencePrivate, "actor-a")
	if err := AdmitSession(ctx, store, active.SessionRef, base); err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]RuntimeMemoryBindingSnapshot{
		"audience":           runtimeSnapshotForState(3, "view-a", OutputAudienceShared, "actor-a"),
		"actor":              runtimeSnapshotForState(3, "view-a", OutputAudiencePrivate, "actor-b"),
		"downgrade":          runtimeSnapshotForState(1, "view-a", OutputAudiencePrivate, "actor-a"),
		"same version drift": runtimeSnapshotForState(2, "view-b", OutputAudiencePrivate, "actor-a"),
		"binding": func() RuntimeMemoryBindingSnapshot {
			changed := runtimeSnapshotForState(3, "view-a", OutputAudiencePrivate, "actor-a")
			changed.BindingRef = "binding-b"
			return changed
		}(),
		"principal": func() RuntimeMemoryBindingSnapshot {
			changed := runtimeSnapshotForState(3, "view-a", OutputAudiencePrivate, "actor-a")
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

func TestSessionAdmissionPinsRuntimeLabelsAndClearsLegacyCursor(t *testing.T) {
	ctx := context.Background()
	store := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "session-memory-labels",
	})
	if err != nil {
		t.Fatal(err)
	}
	base := runtimeSnapshotForState(1, "view-a", OutputAudiencePrivate, "actor-a")
	base.Labels = memoryv1alpha1.LabelSet{"workspace:alpha"}
	if err := AdmitSession(ctx, store, active.SessionRef, base); err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.Labels = memoryv1alpha1.LabelSet{"workspace:beta"}
	if err := AdmitSession(ctx, store, active.SessionRef, changed); !errors.Is(err, ErrSessionAdmissionConflict) {
		t.Fatalf("changed Runtime labels error = %v, want ErrSessionAdmissionConflict", err)
	}

	legacy, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "session-memory-labels-legacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyState := sessionBindingStateFromSnapshot(runtimeSnapshotForState(1, "view-a", OutputAudiencePrivate, "actor-a"))
	legacyState.Version = legacyLogicalBindingStateVersion
	legacyState.Labels = nil
	legacyState.ConsistencyToken = "legacy-empty-label-token"
	_, err = store.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef:    legacy.SessionRef,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
		Update: func(state map[string]any) (map[string]any, error) {
			return encodeSessionBindingState(state, legacyState), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := AdmitSession(ctx, store, legacy.SessionRef, base); err != nil {
		t.Fatalf("migrate pre-LabelSet Session admission: %v", err)
	}
	if token, err := ConsistencyToken(ctx, store, legacy.SessionRef, base); err != nil || token != "" {
		t.Fatalf("migrated consistency token = %q, %v; want cleared", token, err)
	}
	state, err := store.SnapshotState(ctx, legacy.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(state[SessionStateKey])
	if !strings.Contains(string(raw), `"version":4`) || !strings.Contains(string(raw), "workspace:alpha") || strings.Contains(string(raw), "legacy-empty-label-token") {
		t.Fatalf("migrated Session Memory state = %s", raw)
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
	if err := AdmitSession(ctx, store, active.SessionRef, runtimeSnapshotForState(1, "view-a", OutputAudiencePrivate, "actor-a")); err == nil {
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
	if err := AdmitSession(ctx, store, active.SessionRef, runtimeSnapshotForState(1, "view-a", OutputAudiencePrivate, "actor-a")); err == nil {
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
	binding := runtimeSnapshotForState(1, "view-a", OutputAudiencePrivate, "actor-a")
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

func runtimeSnapshotForState(version uint64, view string, audience OutputAudience, actor RuntimeActorRef) RuntimeMemoryBindingSnapshot {
	return RuntimeMemoryBindingSnapshot{
		BindingRef: "binding-a", RuntimeActorRef: actor,
		PrincipalRef: "principal:a", IssuerCredentialRef: "memory-issuer:a",
		ViewRef: view, GrantRef: "grant-a", Audience: audience, BindingVersion: version,
	}
}
