package gatewayapp

import (
	"context"
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestGuardianDetachedReviewPersistsUsageDuringParentTurn(t *testing.T) {
	for _, decision := range []struct {
		name     string
		output   string
		approved bool
	}{
		{"approved", `{"option_id":"allow_once"}`, true},
		{"denied", `{"option_id":"reject_once","rationale":"Outside the requested scope."}`, false},
	} {
		t.Run(decision.name, func(t *testing.T) {
			root := t.TempDir()
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := store.StartSession(t.Context(), session.StartSessionRequest{
				AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "w", CWD: t.TempDir()},
			})
			if err != nil {
				t.Fatal(err)
			}
			appendApprovalReviewerTextEvent(t, t.Context(), store, active, session.EventTypeUser, model.RoleUser, "Inspect the workspace without changing files.")
			before, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			wantContext := guardianReceiptParentModelContext(t, active, before)
			fence, err := store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "parent-runtime"})
			if err != nil {
				t.Fatal(err)
			}
			// ACP children intentionally outlive the spawning Turn and cannot carry
			// its fence, but their Guardian receipts still belong to this Session.
			ctx := session.ContextWithoutRuntimeFence(context.WithoutCancel(session.ContextWithRuntimeFence(t.Context(), fence)))
			llm := &guardianMeasuredModel{approvalReviewerFakeModel: &approvalReviewerFakeModel{responses: []string{decision.output}}}
			reviewer := newGuardianApprovalApprover(store)
			req := approvalReviewerTestRequest(active, llm, "inspect", nil)
			req.RuntimeRequest.SessionRef = active.SessionRef
			req.RuntimeRequest.Origin = &agent.ApprovalOrigin{Role: agent.ApprovalRoleSubagent, ParentSessionID: active.SessionID, TaskID: "child-task"}
			result, err := reviewer.Decide(ctx, req)
			if err != nil || result.Approved != decision.approved || len(llm.Requests()) != 1 {
				t.Fatalf("decision changed or repeated: %#v, %v; calls=%d", result, err, len(llm.Requests()))
			}
			usage, invocation, accountingErr := reviewer.ApprovalReviewAccounting(t.Context(), req, result)
			if accountingErr != nil || usage != nil || invocation != nil || strings.Contains(result.DisplayText, "usage accounting could not be persisted") {
				t.Fatalf("detached receipt failed or duplicated legacy accounting: %v, %#v, %#v; %q", accountingErr, usage, invocation, result.DisplayText)
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			after, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			var receipts []*session.Event
			for _, event := range after {
				if session.IsModelInvocationReceipt(event) {
					receipts = append(receipts, event)
				}
			}
			if len(receipts) != 1 {
				t.Fatalf("persisted receipts=%d, want 1", len(receipts))
			}
			receipt := receipts[0]
			measured := session.UsageSnapshotFromSessionEvent(receipt)
			if measured == nil || measured.TotalTokens != 12 || receipt.SessionID != active.SessionID || receipt.Scope == nil || receipt.Scope.Executor.ID != "guardian" || receipt.Meta["usage_category"] != "auto_review" {
				t.Fatalf("receipt lost its measurement or parent ownership: %#v", receipt)
			}
			if got := guardianReceiptParentModelContext(t, active, after); !reflect.DeepEqual(got, wantContext) {
				t.Fatalf("receipt changed rebuilt parent model context: %#v, want %#v", got, wantContext)
			}
			if !reflect.DeepEqual(session.FilterClientReplayEvents(after), session.FilterClientReplayEvents(before)) {
				t.Fatal("Guardian receipt entered client replay")
			}
		})
	}
}

func guardianReceiptParentModelContext(t *testing.T, active session.Session, events []*session.Event) []model.Message {
	t.Helper()
	probe := &approvalReviewerFakeModel{responses: []string{"done"}}
	parent, err := chat.New("parent", probe, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range parent.Run(agent.NewContext(agent.ContextSpec{Context: t.Context(), Session: active, Events: events})) {
		if err != nil {
			t.Fatal(err)
		}
	}
	requests := probe.Requests()
	if len(requests) != 1 || len(requests[0].Messages) == 0 {
		t.Fatalf("parent did not produce model context: %#v", requests)
	}
	return requests[0].Messages
}
