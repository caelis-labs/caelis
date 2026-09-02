package gatewayapp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/memorybinding"
	memoryv1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
)

func TestRuntimeMemoryLabelsAlwaysPartitionByWorkspaceAndAllowOpaqueExtensions(t *testing.T) {
	binding := memorybinding.RuntimeMemoryBindingSnapshot{
		BindingRef: "binding:a", RuntimeActorRef: "actor:a", PrincipalRef: "principal:a",
		IssuerCredentialRef: "issuer:a", ViewRef: "view:a", GrantRef: "grant:a",
		Audience: memorybinding.OutputAudiencePrivate, BindingVersion: 1,
	}
	ref := session.SessionRef{AppName: "caelis", UserID: "user", SessionID: "session-a", WorkspaceKey: "/private/workspace/alpha"}
	workspace := session.WorkspaceRef{Key: "/private/workspace/alpha", CWD: "/private/workspace/alpha"}
	selectorCalls := 0
	labeled, err := bindRuntimeMemoryLabels(t.Context(), binding, func(_ context.Context, input MemoryLabelSelectionContext) ([]string, error) {
		selectorCalls++
		if input.SessionRef != ref || input.Workspace != workspace || input.BindingRef != binding.BindingRef {
			t.Fatalf("label selector input = %#v", input)
		}
		return []string{"identity:future-agent"}, nil
	}, ref, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if selectorCalls != 1 || len(labeled.Labels) != 2 || !slices.Contains(labeled.Labels, memoryv1alpha1.Label("identity:future-agent")) {
		t.Fatalf("Runtime labels = %#v, calls=%d", labeled.Labels, selectorCalls)
	}
	encoded := strings.Join([]string{string(labeled.Labels[0]), string(labeled.Labels[1])}, " ")
	if strings.Contains(encoded, workspace.Key) || !strings.Contains(encoded, workspaceMemoryLabelPrefix) {
		t.Fatalf("workspace label exposed its source key: %q", encoded)
	}

	other, err := bindRuntimeMemoryLabels(t.Context(), binding, nil, ref, session.WorkspaceRef{Key: "/private/workspace/beta"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(labeled.Labels, other.Labels) {
		t.Fatalf("different workspaces received equal labels: %#v", labeled.Labels)
	}
}

func TestRuntimeMemoryLabelsRejectInvalidExtensionBeforeCapabilityIssue(t *testing.T) {
	binding := memorybinding.RuntimeMemoryBindingSnapshot{
		BindingRef: "binding:a", RuntimeActorRef: "actor:a", PrincipalRef: "principal:a",
		IssuerCredentialRef: "issuer:a", ViewRef: "view:a", GrantRef: "grant:a",
		Audience: memorybinding.OutputAudiencePrivate, BindingVersion: 1,
	}
	workspace := session.WorkspaceRef{Key: "workspace"}
	_, err := bindRuntimeMemoryLabels(t.Context(), binding, func(context.Context, MemoryLabelSelectionContext) ([]string, error) {
		return []string{" duplicate ", "duplicate"}, nil
	}, session.SessionRef{SessionID: "session"}, workspace)
	if err == nil || !strings.Contains(err.Error(), "invalid Runtime labels") {
		t.Fatalf("invalid label extension error = %v", err)
	}
}
