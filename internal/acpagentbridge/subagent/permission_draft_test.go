package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	v2 "github.com/caelis-labs/acp-go-sdk/experimental/v2"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

func TestDraftChildNativeToolDisplayDoesNotTrustExternalMetadata(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		builtin, sensitive, opaque, expose  bool
		missingHint, wrongTool, independent bool
	}{
		{name: "native", builtin: true, expose: true},
		{name: "external native claims"},
		{name: "native sensitive", builtin: true, sensitive: true},
		{name: "native opaque", builtin: true, opaque: true},
		{name: "native missing context", builtin: true, missingHint: true},
		{name: "native mismatched tool", builtin: true, wrongTool: true},
		{name: "native independent subject", builtin: true, independent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req v2.RequestPermissionRequest
			if err := json.Unmarshal([]byte(`{"sessionId":"child","title":"Native resource permission","subject":{"type":"tool_call","toolCall":{"toolCallId":" original-call ","name":"desktop_world__exec","kind":"other","rawInput":{"app":"fixture.app","source":"return 'fixture';"}}},"options":[{"optionId":"allow_session","name":"Allow this app","kind":"allow_once"},{"optionId":"cancel","name":"Cancel","kind":"reject_once"}]}`), &req); err != nil {
				t.Fatal(err)
			}
			hint := &session.ApprovalSubject{Kind: "app_access", Tool: session.KnownApprovalField("desktop_world__exec"), Scope: session.KnownApprovalField("reported scope"), SensitiveInput: tc.sensitive, OpaqueInput: tc.opaque}
			if tc.wrongTool {
				hint.Tool = session.KnownApprovalField("different__exec")
			}
			encoded, _ := json.Marshal(hint)
			req.Meta = map[string]json.RawMessage{"caelis/approval_subject": encoded, "builtin_runtime": json.RawMessage("true")}
			if tc.missingHint {
				delete(req.Meta, "caelis/approval_subject")
			}
			if tc.independent {
				req.Subject = nil
			}
			calls := 0
			spawn := tasksubagent.SpawnContext{SessionRef: session.SessionRef{SessionID: "parent"}, TaskID: "original-task", ParentCallID: "original-spawn", ApprovalRequester: subagentApprovalRequesterFunc(func(_ context.Context, request tasksubagent.ApprovalRequest) (tasksubagent.ApprovalResponse, error) {
				calls++
				if request.Origin.SessionID != "child" || request.Origin.ParentSessionID != "parent" || request.Origin.TaskID != "original-task" || request.Origin.ParentCallID != "original-spawn" || request.Approval == nil {
					t.Fatal("native forwarding changed its bound identity")
				}
				if (request.Origin.Endpoint == agent.ApprovalEndpointBuiltin) != tc.builtin {
					t.Fatal("peer metadata changed endpoint provenance")
				}
				canonical := approval.PayloadFromRuntimeRequest(agent.ApprovalRequest{Approval: request.Approval})
				if canonical.Subject.Reported || canonical.Subject.Scope.State != session.ApprovalFieldOpaque {
					t.Fatal("display hint became canonical action or grant authority")
				}
				display := approval.DisplayPayloadFromRuntimeRequest(agent.ApprovalRequest{Approval: request.Approval})
				if (display.RawInput["app"] == "fixture.app" && display.RawInput["source"] == "return 'fixture';") != tc.expose {
					t.Fatal("native/external or sensitive display boundary changed")
				}
				if !tc.independent && (canonical.ToolCallID != " original-call " || canonical.RawInput["app"] != "fixture.app" || len(request.Approval.Options) != 2 || request.Approval.Options[0].ID != "allow_session") {
					t.Fatal("canonical tool identity, evidence or original choices changed")
				}
				if tc.independent && (canonical.ToolCallID != "" || canonical.ToolName != "" || len(display.RawInput) != 0) {
					t.Fatal("independent subject acquired an invented tool")
				}
				return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
			})}
			run := &childRun{anchor: delegation.Anchor{SessionID: "child"}}
			response, err := (&Runner{}).boundDraftPermissionHandler(run, spawn, AgentConfig{Name: "native-child", BuiltinRuntime: tc.builtin}, "native-child")(t.Context(), req)
			if err != nil || calls != 1 || response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "allow_session" {
				t.Fatalf("original native option did not return: %+v %v calls=%d", response, err, calls)
			}
		})
	}
}

func TestDraftChildPermissionIndependentContextAndObservationBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, bound, requested string
		observation, legacy    bool
	}{
		{name: "matched", bound: "child", requested: "child"}, {name: "mismatch", bound: "child", requested: "other"}, {name: "unbound", requested: "child"}, {name: "observation", bound: "child", requested: "child", observation: true}, {name: "stable-only bridge", bound: "child", requested: "child", legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var observed tasksubagent.ApprovalRequest
			spawn := tasksubagent.SpawnContext{SessionRef: session.SessionRef{SessionID: "parent"}, TaskID: "task", ParentCallID: "spawn", ApprovalRequester: subagentApprovalRequesterFunc(func(_ context.Context, req tasksubagent.ApprovalRequest) (tasksubagent.ApprovalResponse, error) {
				calls++
				observed = req
				return agent.ApprovalResponse{Outcome: "selected", OptionID: "native-allow", Approved: true}, nil
			})}
			runner := &Runner{}
			if tc.legacy {
				runner.permissionBridge = &recordingPermissionBridge{}
			}
			run := &childRun{anchor: delegation.Anchor{SessionID: tc.bound}, observationOnly: tc.observation}
			req := v2.RequestPermissionRequest{SessionId: v2.SessionId(tc.requested), Title: "Independent command", Options: []v2.PermissionOption{{OptionId: "native-allow", Name: "Allow", Kind: "allow_once"}}, Subject: &v2.RequestPermissionSubject{Command: &v2.RequestPermissionSubjectCommand{Type: "command", Command: "git fetch", Cwd: "/workspace"}}}
			response, err := runner.boundDraftPermissionHandler(run, spawn, AgentConfig{Name: "helper"}, "helper")(t.Context(), req)
			if tc.bound == "" || tc.bound != tc.requested {
				if !errors.Is(err, errACPPermissionSessionMismatchChild) || calls != 0 {
					t.Fatalf("mismatched child entered approval: %v", err)
				}
				return
			}
			if tc.observation || tc.legacy {
				if err != nil || response.Outcome.Cancelled == nil || calls != 0 {
					t.Fatalf("observer or unsupported callback granted: %+v %v", response, err)
				}
				return
			}
			if err != nil || response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "native-allow" || calls != 1 || observed.Origin.SessionID != "child" || observed.Origin.ParentSessionID != "parent" || observed.TaskID != "task" || observed.Approval == nil || observed.Approval.ToolCall.ID != "" {
				t.Fatalf("child context or resolve changed: %+v %+v %v", observed, response, err)
			}
		})
	}
}
