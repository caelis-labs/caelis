package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestApplicationReviewerDoesNotChangeNativePolicy(t *testing.T) {
	root := t.TempDir()
	for _, mode := range []string{"manual", "auto-review"} {
		profile := application.Profile{Execution: "workspace-write", Permissions: application.Permissions{ApprovalMode: mode}}
		registry, name, err := applicationPolicyRegistry(profile)
		if err != nil {
			t.Fatal(err)
		}
		selected, ok, err := registry.Lookup(t.Context(), name)
		if err != nil || !ok {
			t.Fatal(selected, err)
		}
		for _, tc := range []struct {
			name, args string
			want       policy.Action
		}{
			{"Write", `{"path":"inside.txt","content":"safe"}`, policy.ActionAllow},
			{"Write", `{"path":` + string(mustJSON(t, filepath.Join(filepath.Dir(root), "outside.txt"))) + `,"content":"exact action"}`, policy.ActionAskApproval},
			{"RunCommand", `{"command":"echo safe","sandbox_permissions":"require_escalated","justification":"test Host action"}`, policy.ActionAskApproval},
		} {
			got, err := selected.DecideTool(t.Context(), policy.ToolContext{
				Tool: tool.Definition{Name: tc.name}, Call: tool.Call{ID: "call", Name: tc.name, Input: json.RawMessage(tc.args)},
				Options: policy.ModeOptions{WorkspaceRoot: root, WritableRoots: []string{root}},
			})
			if err != nil || got.Action != tc.want {
				t.Fatalf("%s %s = %+v, %v", mode, tc.name, got, err)
			}
			if tc.want == policy.ActionAskApproval {
				if got.Approval == nil || len(got.Approval.Options) != 2 || got.Approval.Options[0].Kind != "allow_once" {
					t.Fatalf("review expanded authorization: %+v", got)
				}
				wantRoute := sandbox.RouteSandbox
				if tc.name == "RunCommand" {
					wantRoute = sandbox.RouteHost
				}
				if got.Constraints.Route != wantRoute {
					t.Fatalf("review changed route: %+v", got.Constraints)
				}
			}
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestApplicationReviewerStateUsesCanonicalModelAndSafeErrors(t *testing.T) {
	ctx := t.Context()
	stack := newLocalStateTestHost(t, &runtimeMemoryHostStub{})
	owner := appserver.Principal{ID: stack.composition.authorities.userID}
	revision, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	connected, err := stack.ConfigurationCommands().ConnectModel(ctx, owner, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "connect-review", ExpectedRevision: &revision},
		Config:    appserver.ConnectConfig{Provider: "openai-compatible", Model: "reviewer-model", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY"},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatal(connected, err)
	}
	profile := application.Profile{Version: "v1", Model: "main", ToolsVersion: "v1", Execution: "tools-only",
		Permissions: application.Permissions{ApprovalMode: "auto-review"}, Reviewer: &application.Reviewer{Kind: "guardian", Model: "openai-compatible/reviewer-model"}}
	state, err := stack.composition.applicationReviewerState(ctx, profile)
	if err != nil || state.Status != "ready" || state.ApprovalMode != "auto-review" {
		t.Fatal(state, err)
	}
	stack.composition.lookup.resolveTransportHTTPClient = func(context.Context, ModelConfig) (*http.Client, error) {
		return nil, errors.New("SECRET_TRANSPORT_DETAIL")
	}
	state, err = stack.composition.applicationReviewerState(ctx, profile)
	if err != nil || state.Status != "unavailable" || state.Reason == "" || strings.Contains(state.Reason, "SECRET") {
		t.Fatal("unsafe/incorrect reviewer state", state, err)
	}
	stack.composition.lookup.resolveTransportHTTPClient = nil
	// Canonical removal must win even if process-local refresh was lost.
	doc, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := cloneSessionModelLookup(stack.composition.lookup, doc)
	if err != nil {
		t.Fatal(err)
	}
	stale.resolveAPIKey = func(context.Context, string) (string, error) { return "SYNTHETIC_ONLY", nil }
	deleted, err := stack.ConfigurationCommands().DeleteModel(ctx, owner, appserver.DeleteModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "delete-review", ExpectedRevision: &doc.ConfigurationRevision}, Model: profile.Reviewer.Model,
	})
	if err != nil || deleted.Outcome != appserver.OutcomeCommitted {
		t.Fatal(deleted, err)
	}
	stack.composition.lookup = stale
	if _, err := resolveApplicationReviewer(ctx, stale, profile, 0); err != nil {
		t.Fatalf("fixture lost its stale reviewer: %v", err)
	}
	state, err = stack.composition.applicationReviewerState(ctx, profile)
	if err != nil || state.Status != "unavailable" {
		t.Fatal("stale reviewer availability", state, err)
	}
	profile.Reviewer = nil
	profile.Permissions.ApprovalMode = ""
	state, err = stack.composition.applicationReviewerState(ctx, profile)
	if err != nil || state.Status != "manual" || state.Reviewer != nil {
		t.Fatal("legacy app inherited reviewer", state, err)
	}
}

func TestApplicationReviewResolverIsCreationBound(t *testing.T) {
	resolver := &applicationTurnResolver{binding: application.Binding{SessionID: "application", Profile: application.Profile{
		Permissions: application.Permissions{ApprovalMode: "auto-review"}, Reviewer: &application.Reviewer{Kind: "guardian", Model: "reviewer"},
	}}}
	mode, err := resolver.ResolveApprovalMode(t.Context(), session.SessionRef{SessionID: "application"})
	if err != nil || mode != kernel.ApprovalModeAutoReview {
		t.Fatal(mode, err)
	}
	if _, err := resolver.ResolveApprovalMode(t.Context(), session.SessionRef{SessionID: "worker"}); !errors.Is(err, application.ErrUnauthorized) {
		t.Fatal("application policy escaped its Session", err)
	}
	if _, err := resolver.ResolveApprovalModel(t.Context(), session.SessionRef{SessionID: "worker"}); !errors.Is(err, application.ErrUnauthorized) {
		t.Fatal("application reviewer escaped its Session", err)
	}
}
