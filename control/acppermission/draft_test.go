package acppermission_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	v2 "github.com/caelis-labs/acp-go-sdk/experimental/v2"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/acppermission"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
)

func TestDraftPermissionIndependentSubjectsPreserveCanonicalIdentity(t *testing.T) {
	for _, subject := range []string{"", `,"subject":{"type":"command","command":"git fetch","cwd":"/workspace"}`, `,"subject":{"type":"_vendor","private":"OPAQUE-RAW-MUST-NOT-DISPLAY"}`, `,"subject":{"type":"tool_call","toolCall":{"toolCallId":"actual-call","name":"custom","rawInput":{"credential":"OPAQUE-RAW-MUST-NOT-DISPLAY"}}}`} {
		t.Run(subject, func(t *testing.T) {
			var req v2.RequestPermissionRequest
			if err := json.Unmarshal([]byte(`{"sessionId":"remote","title":"Original prompt title","description":"Original explanation","options":[{"optionId":"native-allow","name":" Allow original ","kind":"allow_once"},{"optionId":"native-reject","name":"Reject","kind":"reject_once"}]`+subject+`}`), &req); err != nil {
				t.Fatal(err)
			}
			permission, err := acppermission.RequestFromDraft(req, json.RawMessage(`9007199254740991`))
			if err != nil {
				t.Fatal(err)
			}
			if permission.Context.Title != req.Title || *permission.Context.Description != *req.Description || string(permission.Context.NativeRequestID) != "9007199254740991" || permission.Options[0].Name != " Allow original " {
				t.Fatalf("native request changed: %+v", permission)
			}
			if req.Subject == nil || req.Subject.ToolCall == nil {
				if permission.ToolCall.ID != "" || permission.ToolCall.Name != "" {
					t.Fatalf("independent subject acquired fake tool: %+v", permission.ToolCall)
				}
			}
			canonical := approval.PayloadFromRuntimeRequest(agent.ApprovalRequest{Approval: permission})
			if canonical.Context.Title != req.Title || !reflect.DeepEqual(canonical.Context.RawSubject, permission.Context.RawSubject) {
				t.Fatal("machine context was replaced by public facts")
			}
			display := approval.ProtocolApprovalFromPayload(approval.DisplayPayloadFromRuntimeRequest(agent.ApprovalRequest{Approval: permission}))
			event := &session.Event{Type: session.EventTypeLifecycle, Protocol: &session.EventProtocol{Method: session.ProtocolMethodRequestPermission, Permission: display}}
			envs := projection.ProjectSessionEventEnvelope(eventstream.Envelope{SessionID: "owner", ApprovalRequestID: "control-admission"}, event)
			if len(envs) != 1 {
				t.Fatalf("projection = %+v", envs)
			}
			raw, err := wirev1.MarshalEnvelope(envs[0])
			if err != nil || strings.Contains(string(raw), "OPAQUE-RAW-MUST-NOT-DISPLAY") || strings.Contains(string(raw), "raw_subject") || strings.Contains(string(raw), "9007199254740991") {
				t.Fatalf("private draft context escaped: %s %v", raw, err)
			}
			if permission.ToolCall.ID == "" && strings.Contains(string(raw), `"toolCall"`) {
				t.Fatalf("public projection fabricated a tool call: %s", raw)
			}
			decoded, err := wirev1.UnmarshalEnvelope(raw)
			if err != nil || !reflect.DeepEqual(decoded.ApprovalSubject, display.Subject) || decoded.Permission.Options[0].Name != " Allow original " {
				t.Fatalf("stable public roundtrip lost facts or original options: %+v %v", decoded, err)
			}
			if req.Subject != nil && req.Subject.Command != nil {
				if display.Subject.Targets[0].Text != "git fetch" || display.Subject.Targets[1].Text != "/workspace" || display.Subject.Scope.State != session.ApprovalFieldOpaque || display.Subject.Grants[0].Scope.State != session.ApprovalFieldOpaque {
					t.Fatal("command fields were lost or scope invented")
				}
			}
			for _, choice := range permission.Options {
				response, err := acppermission.ResponseToDraft(req, agent.ApprovalResponse{Outcome: "selected", OptionID: choice.ID})
				if err != nil || response.Outcome.Selected == nil || string(response.Outcome.Selected.OptionId) != choice.ID {
					t.Fatalf("native resolution = %+v %v", response, err)
				}
			}
			if _, err := acppermission.ResponseToDraft(req, agent.ApprovalResponse{Outcome: "selected", OptionID: "invented"}); err == nil {
				t.Fatal("unoffered choice granted")
			}
			cancelled, err := acppermission.ResponseToDraft(req, agent.ApprovalResponse{})
			if err != nil || cancelled.Outcome.Cancelled == nil {
				t.Fatal("no decision was interpreted as a grant")
			}
		})
	}
}

func TestDraftPermissionRejectsAmbiguousNativeOptions(t *testing.T) {
	for _, options := range [][]v2.PermissionOption{nil, {{OptionId: "allow", Kind: "unknown"}}, {{OptionId: "same", Kind: "allow_once"}, {OptionId: "same", Kind: "reject_once"}}} {
		_, err := acppermission.RequestFromDraft(v2.RequestPermissionRequest{SessionId: "remote", Title: "title", Options: options}, nil)
		if err == nil {
			t.Fatal("invalid native options accepted")
		}
	}
}

func TestDraftPermissionActualToolIdentityRemainsExact(t *testing.T) {
	var req v2.RequestPermissionRequest
	if err := json.Unmarshal([]byte(`{"sessionId":"remote","title":"title","subject":{"type":"command","command":"go test","cwd":"/workspace","toolCallId":" exact-actual-id "},"options":[{"optionId":"reject","name":"Reject","kind":"reject_once"}]}`), &req); err != nil {
		t.Fatal(err)
	}
	original, err := acppermission.RequestFromDraft(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	canonical := approval.PayloadFromRuntimeRequest(agent.ApprovalRequest{Approval: original})
	if original.ToolCall.ID != " exact-actual-id " || canonical.ToolCallID != original.ToolCall.ID || approval.ProtocolApprovalFromPayload(canonical).ToolCall.ID != original.ToolCall.ID {
		t.Fatal("actual endpoint tool identity was normalized")
	}
}

func TestDraftPermissionReportedDisplayCannotChangeCanonicalEvidenceOrOptions(t *testing.T) {
	var req v2.RequestPermissionRequest
	if err := json.Unmarshal([]byte(`{"sessionId":"remote","title":"Original native title","subject":{"type":"command","command":"git fetch","cwd":"/workspace"},"options":[{"optionId":"native-reject","name":"Reject","kind":"reject_once"},{"optionId":"native-allow","name":"Allow","kind":"allow_once"}],"_meta":{"caelis/approval_subject":{"kind":"command","tool":{"state":"known","text":"remote tool"},"operation":{"state":"known","text":"remote claimed operation"},"targets":[{"state":"known","text":"SECRET-MUST-NOT-DISPLAY","sensitive":true}],"effect":{"state":"truncated","text":"bounded description..."},"scope":{"state":"known","text":"reported narrow scope"},"grants":[{"option_id":"invented","scope":{"state":"known","text":"unlimited"}},{"option_id":"native-allow","scope":{"state":"known","text":"this invocation"}}]}}}`), &req); err != nil {
		t.Fatal(err)
	}
	permission, err := acppermission.RequestFromDraft(req, json.RawMessage(`"actual-native-request"`))
	if err != nil {
		t.Fatal(err)
	}
	canonical := approval.PayloadFromRuntimeRequest(agent.ApprovalRequest{Approval: permission})
	if canonical.Subject.Reported || canonical.Subject.Scope.State != session.ApprovalFieldOpaque || canonical.Subject.Operation.Text != "execute" || canonical.Context.Title != req.Title || string(canonical.Context.NativeRequestID) != `"actual-native-request"` || !strings.Contains(string(canonical.Context.RawSubject), "git fetch") {
		t.Fatalf("remote display claims became authoritative: %+v", canonical)
	}
	display := approval.DisplayPayloadFromRuntimeRequest(agent.ApprovalRequest{Approval: permission})
	if !display.Subject.Reported || display.Subject.Scope.State != session.ApprovalFieldOpaque || !display.Subject.SensitiveInput || display.Subject.Targets[0].Text != "" || len(display.Subject.Grants) != 2 || display.Subject.Grants[0].OptionID != "native-reject" || display.Subject.Grants[0].Scope.State != session.ApprovalFieldUnavailable || display.Subject.Grants[1].OptionID != "native-allow" {
		t.Fatalf("bounded presentation or original choices lost: %+v", display.Subject)
	}
	if _, err := acppermission.ResponseToDraft(req, agent.ApprovalResponse{Outcome: "selected", OptionID: "invented"}); err == nil {
		t.Fatal("display hint manufactured permission")
	}
	if display.Context.RawSubject != nil || display.Context.NativeRequestID != nil {
		t.Fatal("display hint exposed private evidence")
	}
	// The same public snapshot survives a durable permission round trip.
	encoded, err := json.Marshal(approval.ProtocolApprovalFromPayload(display))
	if err != nil || strings.Contains(string(encoded), "SECRET-MUST-NOT-DISPLAY") || strings.Contains(string(encoded), "actual-native-request") {
		t.Fatalf("unsafe durable presentation: %s, %v", encoded, err)
	}
	var restored session.ProtocolApproval
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(session.ApprovalDisplaySubjectOf(&restored), display.Subject) {
		t.Fatal("live and recovered reported context diverged")
	}
}

func TestDraftPermissionIgnoresMalformedOrOversizedDisplayClaims(t *testing.T) {
	for _, hint := range []any{nil, "not a subject", map[string]any{"title": 42}, map[string]any{"description": map[string]any{"state": "known", "text": strings.Repeat("x", 33<<10)}}} {
		raw, err := json.Marshal(hint)
		if err != nil {
			t.Fatal(err)
		}
		request := v2.RequestPermissionRequest{SessionId: "remote", Title: "Native original title", Options: []v2.PermissionOption{{OptionId: "reject", Name: "Reject", Kind: "reject_once"}}, Meta: map[string]json.RawMessage{"caelis/approval_subject": raw}}
		permission, err := acppermission.RequestFromDraft(request, json.RawMessage(`123`))
		if err != nil || permission.Context.DisplaySubjectHint != nil || session.ApprovalDisplaySubjectOf(permission).Reported || permission.Options[0].ID != "reject" || string(permission.Context.NativeRequestID) != "123" {
			t.Fatalf("invalid optional hint changed native request: %+v, %v", permission, err)
		}
	}
}
