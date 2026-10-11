package acpagentbridge

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

type approvalDisplayCallbacks struct {
	permissions int
	last        acpsdk.RequestPermissionRequest
}

func (*approvalDisplayCallbacks) SessionUpdate(context.Context, eventstream.SessionNotification) error {
	return nil
}
func (c *approvalDisplayCallbacks) RequestPermission(_ context.Context, req acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	c.permissions++
	c.last = req
	return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeSelected("allow_once")}, nil
}

type approvalEvidenceReviewer struct{ last approval.ReviewRequest }

func (r *approvalEvidenceReviewer) ReviewApproval(_ context.Context, req approval.ReviewRequest) (approval.ReviewResult, error) {
	r.last = req
	return approval.ReviewResult{Approved: true}, nil
}

func TestRuntimeAgentApprovalDisplayCopyKeepsCanonicalReviewAndResolve(t *testing.T) {
	const secret = "DIRECT-MARKED-SECRET"
	original := &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "native-call", Name: "custom", Title: secret, RawInput: map[string]any{"credential": secret}}, Options: []session.ProtocolApprovalOption{{ID: "allow_once", Name: "Allow", Kind: "allow_once"}, {ID: "reject_once", Name: "Reject", Kind: "reject_once"}}}
	req := agent.ApprovalRequest{SessionRef: session.SessionRef{SessionID: "session"}, Tool: tool.Definition{Name: "custom", InputSchema: map[string]any{"properties": map[string]any{"credential": map[string]any{"writeOnly": true}}}},
		Call: tool.Call{ID: "native-call", Name: "custom", Input: json.RawMessage(`{"credential":"DIRECT-MARKED-SECRET"}`)}, Approval: original}
	callbacks := &approvalDisplayCallbacks{}
	response, err := (approvalRequester{callbacks: callbacks, mode: approval.ModeManual}).RequestApproval(t.Context(), req)
	raw, _ := json.Marshal(callbacks.last)
	if err != nil || !response.Approved || response.OptionID != "allow_once" || callbacks.permissions != 1 || strings.Contains(string(raw), secret) {
		t.Fatalf("callback display or canonical resolve = %s, %+v, %v", raw, response, err)
	}
	if len(callbacks.last.Options) != len(original.Options) {
		t.Fatal("display removed original choices")
	}
	for i, native := range original.Options {
		if string(callbacks.last.Options[i].OptionId) != native.ID || string(callbacks.last.Options[i].Kind) != native.Kind || callbacks.last.Options[i].Name != native.Name {
			t.Fatal("display replaced native option identity/order/kind")
		}
	}
	reviewer := &approvalEvidenceReviewer{}
	_, err = (approvalRequester{reviewer: reviewer, mode: approval.ModeAutoReview}).RequestApproval(t.Context(), req)
	if err != nil || reviewer.last.Approval.RawInput["credential"] != secret || reviewer.last.Approval.ToolTitle != secret || !reflect.DeepEqual(reviewer.last.RuntimeRequest.Approval, original) || req.Approval.ToolCall.RawInput["credential"] != secret {
		t.Fatalf("canonical review contaminated by display: %+v, %v", reviewer.last, err)
	}
}
