package gatewayapp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestApplicationGuardianUsesOnlyOwnCanonicalEvidenceWithoutQueryTools(t *testing.T) {
	ctx := t.Context()
	service := inmemory.NewStore(inmemory.Config{})
	active, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "reviewed-application",
		Workspace: session.WorkspaceRef{Key: "reviewed", CWD: t.TempDir()},
		ExecutionConfig: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{
			Set: map[string]string{"APP_PRIVATE_CREDENTIAL": "private execution credential sentinel"},
		}},
		Metadata: map[string]any{"private_configuration": "private metadata sentinel"},
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "another-application",
		Workspace: session.WorkspaceRef{Key: "another", CWD: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	appendApprovalReviewerTextEvent(t, ctx, service, other, session.EventTypeUser, model.RoleUser, "other session private instruction sentinel")
	appendApprovalReviewerTextEvent(t, ctx, service, active, session.EventTypeUser, model.RoleUser, "inspect my own action; preserve the ledger")
	call := guardianSource(0, session.EventTypeToolCall, "prior own action sentinel")
	call.ID, call.Seq = "", 0
	if _, err := service.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: call}); err != nil {
		t.Fatal(err)
	}

	llm := &approvalReviewerFakeModel{responses: []string{`{"option_id":"allow_once"}`}}
	reviewer := newApplicationGuardianApprover(service)
	defer reviewer.Close()
	// An enabled main-agent network must not become a reviewer query grant.
	reviewer.queryNetwork = sandbox.NetworkEnabled
	req := approvalReviewerTestRequest(active, llm, "review this exact action", map[string]any{"command": "exact pending action sentinel"})
	result, err := reviewer.Decide(ctx, req)
	if err != nil || !result.Approved {
		t.Fatalf("Decide() = %+v, %v", result, err)
	}
	requests := llm.Requests()
	if len(requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(requests))
	}
	if len(requests[0].Tools) != 0 {
		t.Fatalf("application reviewer received query tools: %#v", requests[0].Tools)
	}
	encoded, err := json.Marshal(requests[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, want := range []string{"preserve the ledger", "prior own action sentinel", "exact pending action sentinel"} {
		if !strings.Contains(text, want) {
			t.Fatalf("reviewer omitted own canonical evidence %q: %s", want, text)
		}
	}
	for _, absent := range []string{"other session private instruction sentinel", "private execution credential sentinel", "private metadata sentinel", "environment_context", "Read, Grep and RunCommand", "query network policy", "query sandbox"} {
		if strings.Contains(text, absent) {
			t.Fatalf("reviewer request exposes %q: %s", absent, text)
		}
	}
	if !strings.Contains(text, "No filesystem, shell, network, external tools or independent retrieval are available") {
		t.Fatalf("reviewer did not state its actual evidence boundary: %s", text)
	}
	_, q, release, err := reviewer.acquireResident(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if q.runtime != nil || q.root != "" || q.scratch != "" || q.calls != 0 {
		t.Fatal("application reviewer initialized an ambient query sandbox")
	}
	release()
}

func TestNormalGuardianStillAdmitsScopedQueryTools(t *testing.T) {
	service, active := newApprovalReviewerTestSession(t, t.Context())
	llm := &approvalReviewerFakeModel{responses: []string{`{"option_id":"allow_once"}`}}
	reviewer := newGuardianApprovalApprover(service)
	defer reviewer.Close()
	result, err := reviewer.Decide(t.Context(), approvalReviewerTestRequest(active, llm, "ordinary approval", nil))
	if err != nil || !result.Approved {
		t.Fatalf("Decide() = %+v, %v", result, err)
	}
	requests := llm.Requests()
	if len(requests) != 1 || len(requests[0].Tools) != 3 {
		t.Fatalf("normal Guardian tools = %#v", requests)
	}
	for i, want := range []string{"Read", "Grep", "RunCommand"} {
		if requests[0].Tools[i].Function.Name != want {
			t.Fatalf("normal Guardian tool[%d] = %q, want %q", i, requests[0].Tools[i].Function.Name, want)
		}
	}
	instructions := requests[0].Instructions[0].Text.Text
	if !strings.Contains(instructions, "environment_context") || !strings.Contains(instructions, "Read, Grep and RunCommand") {
		t.Fatalf("normal Guardian lost query instructions: %s", instructions)
	}
}
