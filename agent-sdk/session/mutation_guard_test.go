package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type contextMarkerKey struct{}

func TestContextWithoutRuntimeFencePreservesOtherContextState(t *testing.T) {
	base := context.WithValue(context.Background(), contextMarkerKey{}, "kept")
	parent := session.ContextWithRuntimeFence(base, session.SessionFence{
		SessionRef: session.SessionRef{SessionID: "parent"},
		FenceID:    "fence-parent", OwnerID: "owner-parent", FencingToken: 9,
	})

	isolated := session.ContextWithoutRuntimeFence(parent)
	if guard := session.RuntimeMutationGuard(isolated); guard != (session.MutationGuard{}) {
		t.Fatalf("RuntimeMutationGuard() = %#v, want cleared nested scope", guard)
	}
	if got := isolated.Value(contextMarkerKey{}); got != "kept" {
		t.Fatalf("unrelated context value = %#v, want preserved", got)
	}
	if guard := session.RuntimeMutationGuard(parent); guard.FenceID != "fence-parent" || guard.FencingToken != 9 {
		t.Fatalf("parent RuntimeMutationGuard() = %#v, want unchanged", guard)
	}
}

func TestControlMutationGuardWithRuntimeFenceCarriesExactFence(t *testing.T) {
	fence := session.SessionFence{FenceID: "fence-a", OwnerID: "owner-a", FencingToken: 11}
	ctx := session.ContextWithRuntimeFence(context.Background(), fence)
	guard := session.ControlMutationGuardWithRuntimeFence(ctx, session.ControlMutationPurposeHandoff)
	if guard.Authority != session.MutationAuthorityControl || guard.Purpose != session.ControlMutationPurposeHandoff ||
		guard.FenceID != fence.FenceID || guard.OwnerID != fence.OwnerID || guard.FencingToken != fence.FencingToken {
		t.Fatalf("ControlMutationGuardWithRuntimeFence() = %#v, want Control purpose plus exact Runtime fence", guard)
	}
}

func TestValidateControlMutationGuardFailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		guard session.MutationGuard
	}{
		{name: "unknown purpose", guard: session.ControlMutationGuard("future_unknown")},
		{name: "unfenced handoff", guard: session.ControlMutationGuard(session.ControlMutationPurposeHandoff)},
		{name: "unfenced coordinator", guard: session.ControlMutationGuard(session.ControlMutationPurposeCoordinator)},
		{name: "partial fence", guard: session.MutationGuard{
			Authority: session.MutationAuthorityControl, Purpose: session.ControlMutationPurposeHandoff, FenceID: "fence-a",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := session.ValidateControlMutationGuard(test.guard); !errors.Is(err, session.ErrFenceConflict) {
				t.Fatalf("ValidateControlMutationGuard() error = %v, want ErrFenceConflict", err)
			}
		})
	}
	if err := session.ValidateControlMutationGuard(session.ControlMutationGuard(session.ControlMutationPurposeConfiguration)); err != nil {
		t.Fatalf("ValidateControlMutationGuard(configuration) error = %v", err)
	}
}

func TestControlMutationOverlapPolicyFailsUnknownPurposeClosed(t *testing.T) {
	for _, test := range []struct {
		purpose session.ControlMutationPurpose
		want    bool
	}{
		{purpose: session.ControlMutationPurposeApproval, want: true},
		{purpose: session.ControlMutationPurposeParticipant, want: true},
		{purpose: session.ControlMutationPurposeSystemCommit, want: true},
		{purpose: session.ControlMutationPurposeLifecycle, want: false},
		{purpose: session.ControlMutationPurposeConfiguration, want: false},
		{purpose: session.ControlMutationPurposeHandoff, want: false},
		{purpose: session.ControlMutationPurposeCoordinator, want: false},
		{purpose: session.ControlMutationPurpose("future_unknown"), want: false},
	} {
		if got := session.ControlMutationMayOverlapRuntimeFence(test.purpose); got != test.want {
			t.Fatalf("ControlMutationMayOverlapRuntimeFence(%q) = %v, want %v", test.purpose, got, test.want)
		}
	}
}

func TestAuthorizeMutationGuardAppliesSharedFencePolicy(t *testing.T) {
	active, err := session.NewSessionFenceClaim(session.SessionFence{
		SessionRef: session.SessionRef{SessionID: "session-1"},
		FenceID:    "fence-1", OwnerID: "owner-1", FencingToken: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	placed := session.ContextWithRuntimeFence(context.Background(), active)
	exactRuntime := session.RuntimeMutationGuard(placed)
	exactControl := session.ControlMutationGuardWithRuntimeFence(placed, session.ControlMutationPurposeHandoff)

	for _, test := range []struct {
		name   string
		active session.SessionFence
		guard  session.MutationGuard
		want   bool
	}{
		{name: "unguarded without active fence", active: session.SessionFence{SessionRef: active.SessionRef}},
		{name: "unguarded with active fence", active: active, want: true},
		{name: "matching runtime", active: active, guard: exactRuntime},
		{name: "stale runtime", active: active, guard: session.MutationGuard{Authority: session.MutationAuthorityRuntime, FenceID: "stale"}, want: true},
		{name: "overlapping approval", active: active, guard: session.ControlMutationGuard(session.ControlMutationPurposeApproval)},
		{name: "unfenced configuration", active: active, guard: session.ControlMutationGuard(session.ControlMutationPurposeConfiguration), want: true},
		{name: "matching fenced handoff", active: active, guard: exactControl},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := session.AuthorizeMutationGuard(test.active, test.guard)
			if got := errors.Is(err, session.ErrFenceConflict); got != test.want {
				t.Fatalf("AuthorizeMutationGuard() error = %v, conflict=%v; want %v", err, got, test.want)
			}
		})
	}
}
