package runtime

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestApprovalReviewFinalSettlementReachesModel(t *testing.T) {
	options := []session.ProtocolApprovalOption{
		{ID: "allow-execution", Kind: "reject_once", Name: "Do not allow execution"},
		{ID: "stop", Kind: "allow_once", Name: "Do not reject execution"},
	}
	for _, tc := range []struct {
		name        string
		options     []session.ProtocolApprovalOption
		review      approval.ReviewResult
		wantOption  string
		wantOutcome approval.Status
		allow       bool
	}{
		{"select_reject", options, approval.ReviewResult{OptionID: "allow-execution", Approved: true}, "allow-execution", approval.StatusSelected, false},
		{"approve_by_outcome", options, approval.ReviewResult{Approved: true}, "stop", approval.StatusSelected, true},
		{"approve_without_offered_allow", options[:1], approval.ReviewResult{Outcome: "approved"}, "", approval.StatusRejected, false},
		{"cancel_with_stray_option", options, approval.ReviewResult{Outcome: "cancelled", OptionID: "stop", Approved: true}, "", "cancelled", false},
		{"approve_without_options", nil, approval.ReviewResult{Outcome: "allow"}, "", approval.StatusApproved, true},
		{"reject_without_options", nil, approval.ReviewResult{Outcome: "denied", Approved: true}, "", approval.StatusRejected, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: mcpFixturePolicy()}, DefaultPolicyMode: "mcp-test"})
			if err != nil {
				t.Fatal(err)
			}
			llm := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "approval-call", Name: "ApprovalProbe", Args: `{}`}}
				}
				return nil
			}}
			approver := approval.ReviewerAdapter{Reviewer: settlementReviewer{result: tc.review}}
			var requestID string
			requester := approvalRequesterFunc(func(ctx context.Context, req approval.Request) (approval.Response, error) {
				requestID = req.ApprovalRequestID
				result, err := approver.Decide(ctx, approval.ReviewRequest{Approval: approval.PayloadFromRuntimeRequest(req), RuntimeRequest: req})
				if result.Approved != tc.allow || result.OptionID != tc.wantOption || result.Outcome != string(tc.wantOutcome) {
					t.Errorf("finalized review = %+v", result)
				}
				return approval.RuntimeResponseFromFinalReview(result), err
			})
			var effects atomic.Int32
			probe := tool.NamedTool{Def: tool.Definition{Name: "ApprovalProbe"}, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
				// The controller permission bridge preserves an endpoint's empty
				// option set and still uses the same durable settlement as tool policy.
				scope := lifecycleScopeFromContext(ctx)
				bridge := controllerApprovalRequester{runtime: rt, requester: requester, session: active, sessionRef: active.SessionRef, runID: scope.runID, turnID: scope.turnID}
				decision, err := bridge.RequestControllerApproval(ctx, controller.ApprovalRequest{
					EndpointSessionID: "fixture-endpoint",
					Approval:          &session.ProtocolApproval{Options: tc.options, ToolCall: session.ProtocolToolCall{ID: call.ID, Name: call.Name}},
				})
				if err != nil {
					return tool.Result{}, err
				}
				if !decision.Approved {
					return tool.Result{Content: []tool.ContentBlock{tool.Text("endpoint approval denied")}}, tool.FailedReply("endpoint approval denied")
				}
				effects.Add(1)
				return tool.Result{Content: []tool.ContentBlock{tool.Text("effect completed")}}, nil
			}}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "Run the approval probe", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{probe}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			record, err := rt.approvalRecord(t.Context(), active.SessionRef, requestID)
			if err != nil || record.Status != session.ApprovalRecordResolved || record.Approved != tc.allow || record.OptionID != tc.wantOption || record.Outcome != string(tc.wantOutcome) {
				t.Fatalf("durable settlement = %+v, %v", record, err)
			}
			var wantEffects int32
			wantText := "endpoint approval denied"
			if tc.allow {
				wantEffects, wantText = 1, "effect completed"
			}
			if len(llm.requests) != 2 || effects.Load() != wantEffects {
				t.Fatalf("model requests = %d, effects = %d, want effects = %d", len(llm.requests), effects.Load(), wantEffects)
			}
			result := errorDeliveryModelResult(t, llm.requests[1], "approval-call")
			if result.IsError == tc.allow || !bytes.Contains(errorDeliveryJSON(t, result.Content), []byte(wantText)) {
				t.Fatalf("next model tool result = %+v, want %q", result, wantText)
			}
		})
	}
}

type settlementReviewer struct {
	result approval.ReviewResult
}

func (r settlementReviewer) ReviewApproval(context.Context, approval.ReviewRequest) (approval.ReviewResult, error) {
	return r.result, nil
}
