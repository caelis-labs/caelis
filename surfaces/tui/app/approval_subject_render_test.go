package tuiapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/evalharness"
)

func TestApprovalSubjectRenderedDecisionContext(t *testing.T) {
	for _, name := range []string{"known", "opaque", "sensitive", "truncated", "reported", "reported_sensitive"} {
		t.Run(name, func(t *testing.T) {
			known := session.KnownApprovalField
			subject := &session.ApprovalSubject{Kind: "command", Tool: known("RunCommand"), Operation: known("execute"), Targets: []session.ApprovalField{known("git fetch origin"), known("/workspace")}, Effect: known("run the supplied command"), Scope: known("route: host; permission: danger_full_access; network: inherit"), Grants: []session.ApprovalGrant{{OptionID: "native-allow", Scope: known("this invocation")}, {OptionID: "native-reject", Scope: known("no grant")}}}
			switch name {
			case "reported_sensitive":
				field := known("MARKED-SUBJECT-SECRET")
				subject.Title, subject.Description = &field, &field
				subject.Tool, subject.Operation, subject.Effect, subject.Scope = field, field, field, field
				subject.Targets[0] = session.ApprovalField{State: session.ApprovalFieldKnown, Text: field.Text, Sensitive: true}
				subject.Grants[0].Scope = field
				subject.Reported = true
				subject = session.CloneApprovalSubject(subject)
			case "reported":
				subject.Reported = true
			case "opaque":
				subject = session.ApprovalSubjectOf(nil)
			case "sensitive":
				subject.Targets[0] = session.ApprovalField{State: session.ApprovalFieldKnown, Text: "MARKED-SUBJECT-SECRET", Sensitive: true}
				subject.SensitiveInput = true
			case "truncated":
				subject.Targets[0] = known(strings.Repeat("long-command ", 70))
			}
			env := eventstream.Envelope{Kind: eventstream.KindRequestPermission, SessionID: "owner-session", Scope: eventstream.ScopeSubagent, ScopeID: "worker-task", ApprovalRequestID: "native-request", ApprovalSubject: subject,
				Permission: &eventstream.RequestPermissionRequest{ToolCall: eventstream.ToolCallUpdate{ToolCallID: "native-call", Name: new("RunCommand"), Title: new("MODEL-TITLE-MUST-NOT-ESTABLISH-CONTEXT"), RawInput: map[string]any{"command": "RAW-COMMAND-MUST-NOT-DISPLAY", "nested": map[string]any{"credential": "RAW-NESTED-MUST-NOT-DISPLAY"}}}, Options: []acpsdk.PermissionOption{{OptionId: "native-allow", Name: "Allow once", Kind: acpsdk.PermissionOptionKindAllowOnce}, {OptionId: "native-reject", Name: "Reject", Kind: acpsdk.PermissionOptionKindRejectOnce}}}}
			req := approvalPayloadFromACPEvent(env)
			if name == "sensitive" {
				// Render only owner-approved context even when a caller retains
				// derived text from the original private review request.
				req.Reason, req.Justification, req.Risk, req.SandboxPermissions = "MARKED-SUBJECT-SECRET", "MARKED-SUBJECT-SECRET", "MARKED-SUBJECT-SECRET", "MARKED-SUBJECT-SECRET"
			}
			prompt := approvalToPromptRequest(req, make(chan PromptResponse, 1))
			model := NewModel(Config{InitialPrompt: &prompt, NoColor: true, NoAnimation: true})
			model.width, model.height = 88, 44
			frame := evalharness.NormalizeFrame(model.renderPromptModal()) + "\n"
			for _, marker := range []string{"MARKED-SUBJECT-SECRET", "RAW-COMMAND-MUST-NOT-DISPLAY", "RAW-NESTED-MUST-NOT-DISPLAY", "MODEL-TITLE-MUST-NOT-ESTABLISH-CONTEXT"} {
				if strings.Contains(frame, marker) {
					t.Fatalf("input/title leaked or established display context:\n%s", frame)
				}
			}
			if name != "known" && !strings.Contains(frame, "Incomplete or opaque") {
				t.Fatalf("incomplete grant looked informed:\n%s", frame)
			}
			if name != "known" && prompt.DefaultChoice != "native-reject" {
				t.Fatalf("incomplete subject default=%q", prompt.DefaultChoice)
			}
			decision := approvalDecisionFromPrompt(req, PromptResponse{Line: "native-allow"})
			if decision.RequestID != env.ApprovalRequestID || decision.OptionID != "native-allow" || !decision.Approved || req.ToolCallID != "native-call" {
				t.Fatalf("display replaced native authority: %+v", decision)
			}
			path := filepath.Join("testdata", "approval", name+".golden")
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(frame), 0644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(want) != frame {
				t.Fatalf("approval frame differs from %s:\n%s", path, frame)
			}
		})
	}
}
