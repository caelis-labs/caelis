package gatewayapp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func bindRuntimeTeamHTTP(t *testing.T, ctx context.Context, host *applicationHTTPHost, handle agentbinding.Handle, name, operation string) {
	t.Helper()
	catalog, err := host.host.AgentBindingStatus(ctx, appserver.AgentRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var binding agentbinding.Binding
	for _, profile := range catalog.Targets {
		if strings.HasSuffix(profile.ID, "/"+name) {
			binding = agentbinding.Binding{Handle: handle, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort}
		}
	}
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := host.host.BindAgentBinding(ctx, appserver.BindAgentBindingRequest{WriteBase: appserver.WriteBase{OperationID: operation, ExpectedRevision: &status.Configuration.Revision}, Binding: binding})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("bind %s: %+v, %v", handle, result, err)
	}
}

func TestRuntimeTeamHTTPUpdateKeepsPendingReviewAndExactOnceEffect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	provider := &reviewerHTTPProvider{toolName: "ApplicationLookup", args: `{"key":"one"}`, decision: `{"option_id":"allow_once"}`, entered: make(chan struct{}), release: make(chan struct{})}
	host, client, sid := setupReviewerHTTP(t, ctx, t.TempDir(), provider)
	defer host.close(t)
	connectApplicationChildModel(t, ctx, host, "reviewer-next")
	original, err := client.SessionProfile(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	first := promptReviewerHTTP(t, ctx, client, sid, "team-first")
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	reviewerHTTPNoCallbacks(t, ctx, client, sid)
	bindRuntimeTeamHTTP(t, ctx, host, agentbinding.HandleGuardian, "reviewer-next", "hot-guardian")
	reviewerHTTPNoCallbacks(t, ctx, client, sid)
	close(provider.release)
	complete := func(turn string) {
		t.Helper()
		calls, err := client.WaitSessionToolCalls(ctx, sid)
		if err != nil || len(calls) != 1 || calls[0].TurnID != turn {
			t.Fatalf("exact pending action: %+v, %v", calls, err)
		}
		if _, err := client.ClaimSessionToolCall(ctx, sid, calls[0].ID); err != nil {
			t.Fatal(err)
		}
		result := sessionprofile.CallResult{Outcome: "succeeded", Content: json.RawMessage(`{"value":"once"}`)}
		if err := client.CompleteSessionToolCall(ctx, sid, calls[0].ID, result); err != nil {
			t.Fatal(err)
		}
		if err := client.CompleteSessionToolCall(ctx, sid, calls[0].ID, result); err != nil {
			t.Fatal(err)
		}
		waitApplicationHTTPIdle(t, ctx, client, sid)
		completed, err := client.SessionToolCalls(ctx, sid)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range completed {
			if call.State != "completed" {
				t.Fatalf("unexpected repeated action: %+v", call)
			}
		}
	}
	complete(first.Target.TurnID)
	second := promptReviewerHTTP(t, ctx, client, sid, "team-second")
	complete(second.Target.TurnID)
	var models []string
	for _, req := range provider.snapshot() {
		if name, ok := req["model"].(string); ok && strings.HasPrefix(name, "reviewer-") {
			models = append(models, name)
		}
	}
	if len(models) != 2 || models[0] != "reviewer-model" || models[1] != "reviewer-next" {
		t.Fatalf("review requests = %v", models)
	}
	reviews := reviewerHTTPReviewEvents(t, applicationHTTPHistory(t, ctx, client, sid), "approved")
	if len(reviews) != 2 || reviews[0].ApprovalRequestID == reviews[1].ApprovalRequestID {
		t.Fatalf("review identity/decision changed: %+v", reviews)
	}
	current, err := client.SessionProfile(ctx, sid)
	if err != nil || current.SessionID != original.SessionID || current.Profile.Reviewer == nil || *current.Profile.Reviewer != *original.Profile.Reviewer {
		t.Fatalf("upgrade rewrote creation evidence: %+v, %v", current, err)
	}
}

func TestRuntimeTeamNewAutoReviewProfileNeedsNoReviewerOverride(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	provider := &reviewerHTTPProvider{toolName: "ApplicationLookup", args: `{"key":"one"}`, decision: `{"option_id":"allow_once"}`}
	host, client, _ := setupReviewerHTTP(t, ctx, t.TempDir(), provider)
	defer host.close(t)
	profile := applicationHTTPProfile()
	profile.Version = "runtime-team-profile"
	profile.Permissions.ApprovalMode = "auto-review"
	profile.Tools[0].ApprovalPolicy = "required"
	created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create-team-profile"}, Profile: profilePointer(profile)})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("create: %+v, %v", created, err)
	}
	promptReviewerHTTP(t, ctx, client, created.SessionID, "team-no-override")
	call := backgroundCall(t, ctx, client, created.SessionID)
	completeBackgroundCall(t, ctx, client, call)
	waitApplicationHTTPIdle(t, ctx, client, created.SessionID)
	reviews := reviewerHTTPReviewEvents(t, applicationHTTPHistory(t, ctx, client, created.SessionID), "approved")
	if len(reviews) != 1 {
		t.Fatalf("missing native Guardian decision: %+v", reviews)
	}
}
