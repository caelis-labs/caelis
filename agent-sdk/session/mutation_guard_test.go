package session_test

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type contextMarkerKey struct{}

func TestContextWithoutRuntimeLeasePreservesOtherContextState(t *testing.T) {
	base := context.WithValue(context.Background(), contextMarkerKey{}, "kept")
	parent := session.ContextWithRuntimeLease(base, session.SessionLease{
		SessionRef: session.SessionRef{SessionID: "parent"},
		LeaseID:    "lease-parent", OwnerID: "owner-parent", FencingToken: 9,
	})

	isolated := session.ContextWithoutRuntimeLease(parent)
	if guard := session.RuntimeMutationGuard(isolated); guard != (session.MutationGuard{}) {
		t.Fatalf("RuntimeMutationGuard() = %#v, want cleared nested scope", guard)
	}
	if got := isolated.Value(contextMarkerKey{}); got != "kept" {
		t.Fatalf("unrelated context value = %#v, want preserved", got)
	}
	if guard := session.RuntimeMutationGuard(parent); guard.LeaseID != "lease-parent" || guard.FencingToken != 9 {
		t.Fatalf("parent RuntimeMutationGuard() = %#v, want unchanged", guard)
	}
}

func TestControlMutationGuardWithRuntimeLeaseCarriesExactFence(t *testing.T) {
	lease := session.SessionLease{LeaseID: "lease-a", OwnerID: "owner-a", FencingToken: 11}
	ctx := session.ContextWithRuntimeLease(context.Background(), lease)
	guard := session.ControlMutationGuardWithRuntimeLease(ctx, session.ControlMutationPurposeHandoff)
	if guard.Authority != session.MutationAuthorityControl || guard.Purpose != session.ControlMutationPurposeHandoff ||
		guard.LeaseID != lease.LeaseID || guard.OwnerID != lease.OwnerID || guard.FencingToken != lease.FencingToken {
		t.Fatalf("ControlMutationGuardWithRuntimeLease() = %#v, want Control purpose plus exact Runtime fence", guard)
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
			Authority: session.MutationAuthorityControl, Purpose: session.ControlMutationPurposeHandoff, LeaseID: "lease-a",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := session.ValidateControlMutationGuard(test.guard); !errors.Is(err, session.ErrLeaseConflict) {
				t.Fatalf("ValidateControlMutationGuard() error = %v, want ErrLeaseConflict", err)
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
		{purpose: session.ControlMutationPurposeWatchdog, want: true},
		{purpose: session.ControlMutationPurposeParticipant, want: true},
		{purpose: session.ControlMutationPurposeSystemCommit, want: true},
		{purpose: session.ControlMutationPurposeLifecycle, want: false},
		{purpose: session.ControlMutationPurposeConfiguration, want: false},
		{purpose: session.ControlMutationPurposeHandoff, want: false},
		{purpose: session.ControlMutationPurposeCoordinator, want: false},
		{purpose: session.ControlMutationPurpose("future_unknown"), want: false},
	} {
		if got := session.ControlMutationMayOverlapRuntimeLease(test.purpose); got != test.want {
			t.Fatalf("ControlMutationMayOverlapRuntimeLease(%q) = %v, want %v", test.purpose, got, test.want)
		}
	}
}
