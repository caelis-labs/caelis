package gatewayapp_test

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig/typesafe"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestRuntimeTeamHTTPCommitsGuardianPairAtomically(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, &applicationHTTPModel{})
	defer host.close(t)
	connectApplicationChildModel(t, ctx, host, "guardian-review")
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "connect-classifier", ExpectedRevision: &status.Configuration.Revision},
		Config:    appserver.ConnectConfig{Provider: "custom-judgment", Model: typesafe.DefaultModel, BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY"},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("connect classifier: %+v, %v", connected, err)
	}
	catalog, err := host.host.AgentBindingStatus(ctx, appserver.AgentRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var review, classifier agentbinding.Binding
	for _, profile := range catalog.Targets {
		if profile.Judgment {
			classifier = agentbinding.Binding{Handle: agentbinding.HandleGuardianScreen, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort}
		} else if strings.HasSuffix(profile.ID, "/guardian-review") {
			review = agentbinding.Binding{Handle: agentbinding.HandleGuardian, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort}
		}
	}
	if review.ProfileID == "" || classifier.ProfileID == "" {
		t.Fatalf("missing fixture profiles: %+v", catalog.Targets)
	}
	revision := connected.Revision
	invalid := classifier
	invalid.ProfileID = review.ProfileID
	rejected, err := host.host.BindAgentBinding(ctx, appserver.BindAgentBindingRequest{
		WriteBase:        appserver.WriteBase{OperationID: "guardian-pair-invalid", ExpectedRevision: &revision},
		GuardianBindings: []agentbinding.Binding{review, invalid},
	})
	if err == nil || rejected.Outcome != appserver.OutcomeRejected {
		t.Fatalf("invalid pair: %+v, %v", rejected, err)
	}
	after, err := host.host.AgentBindingStatus(ctx, appserver.AgentRequest{})
	if err != nil || !reflect.DeepEqual(after.Handles, catalog.Handles) {
		t.Fatalf("invalid pair changed bindings: %+v, %v", after.Handles, err)
	}
	req := appserver.BindAgentBindingRequest{
		WriteBase:        appserver.WriteBase{OperationID: "guardian-pair", ExpectedRevision: &revision},
		GuardianBindings: []agentbinding.Binding{review, classifier},
	}
	result, err := host.host.BindAgentBinding(ctx, req)
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != revision+1 {
		t.Fatalf("apply pair: %+v, %v", result, err)
	}
	after, err = host.host.AgentBindingStatus(ctx, appserver.AgentRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range after.Handles {
		switch item.Definition.Handle {
		case review.Handle:
			if item.Binding != review {
				t.Fatalf("review binding = %+v, want %+v", item.Binding, review)
			}
		case classifier.Handle:
			if item.Binding != classifier {
				t.Fatalf("classifier binding = %+v, want %+v", item.Binding, classifier)
			}
		}
	}
	replayed, err := host.host.BindAgentBinding(ctx, req)
	if err != nil || replayed != result {
		t.Fatalf("replayed pair: %+v, %v", replayed, err)
	}
	cleared, err := host.host.BindAgentBinding(ctx, appserver.BindAgentBindingRequest{
		WriteBase:        appserver.WriteBase{OperationID: "guardian-pair-reset", ExpectedRevision: &result.Revision},
		GuardianBindings: []agentbinding.Binding{{Handle: review.Handle}, {Handle: classifier.Handle}},
	})
	if err != nil || cleared.Outcome != appserver.OutcomeCommitted || cleared.Revision != result.Revision+1 {
		t.Fatalf("reset pair: %+v, %v", cleared, err)
	}
	after, err = host.host.AgentBindingStatus(ctx, appserver.AgentRequest{})
	if err != nil || !reflect.DeepEqual(after.Handles, catalog.Handles) {
		t.Fatalf("reset pair did not restore defaults: %+v, %v", after.Handles, err)
	}
}

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
	root := t.TempDir()
	audit := filepath.Join(root, "mcp-audit")
	t.Setenv("CAELIS_ATOMIC_MCP_HELPER", "1")
	t.Setenv("CAELIS_ATOMIC_MCP_AUDIT", audit)
	ready, release := make(chan struct{}), make(chan struct{})
	provider := &reviewerHTTPProvider{toolName: "js_exec", args: reviewerMCPArgs, decision: `{"option_id":"allow_once"}`,
		toolReady: ready, entered: make(chan struct{}), release: release}
	host, client, sid := setupReviewerHTTP(t, ctx, root, provider)
	defer host.close(t)
	connectApplicationChildModel(t, ctx, host, "reviewer-next")
	original, err := client.SessionProfile(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	first := promptReviewerHTTP(t, ctx, client, sid, "team-first")
	waitAtomicApplicationMCPRunning(t, ctx, client, sid, "documents")
	close(ready)
	select {
	case <-provider.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	bindRuntimeTeamHTTP(t, ctx, host, agentbinding.HandleGuardian, "reviewer-next", "hot-guardian")
	if strings.Contains(atomicAudit(audit), "documents:call:") {
		t.Fatal("MCP effect occurred before the pending Guardian decision")
	}
	close(release)
	waitApplicationHTTPIdle(t, ctx, client, sid)
	if got := strings.Count(atomicAudit(audit), "documents:call:"); got != 1 {
		t.Fatalf("first provider review dispatched %d MCP effects", got)
	}
	second := promptReviewerHTTP(t, ctx, client, sid, "team-second")
	waitApplicationHTTPIdle(t, ctx, client, sid)
	if got := strings.Count(atomicAudit(audit), "documents:call:"); got != 2 {
		t.Fatalf("two approved Turns dispatched %d MCP effects", got)
	}
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
	if first.Target.TurnID == second.Target.TurnID {
		t.Fatal("distinct approved Turns shared an identity")
	}
	current, err := client.SessionProfile(ctx, sid)
	if err != nil || !reflect.DeepEqual(current, original) {
		t.Fatalf("Team update rewrote creation evidence: %+v, %v", current, err)
	}
}

func TestRuntimeTeamNewAutoReviewProfileNeedsNoReviewerOverride(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	t.Setenv("CAELIS_ATOMIC_MCP_HELPER", "1")
	ready := make(chan struct{})
	provider := &reviewerHTTPProvider{toolName: "js_exec", args: reviewerMCPArgs, decision: `{"option_id":"allow_once"}`, toolReady: ready}
	host, client, _ := setupReviewerHTTP(t, ctx, root, provider)
	defer host.close(t)
	profile := applicationHTTPProfile()
	profile.Permissions.ApprovalMode = "auto-review"
	profile.MCPServers = []sessionprofile.MCPServer{reviewerFixtureMCP(root)}
	created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create-team-profile"}, Profile: profilePointer(profile)})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("create: %+v, %v", created, err)
	}
	promptReviewerHTTP(t, ctx, client, created.SessionID, "team-no-override")
	waitAtomicApplicationMCPRunning(t, ctx, client, created.SessionID, "documents")
	close(ready)
	waitApplicationHTTPIdle(t, ctx, client, created.SessionID)
	reviews := reviewerHTTPReviewEvents(t, applicationHTTPHistory(t, ctx, client, created.SessionID), "approved")
	if len(reviews) != 1 {
		t.Fatalf("missing native Guardian decision: %+v", reviews)
	}
}
