package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/internal/evalharness"
)

func TestNativeCommandApprovalTitleAndRenderedSubject(t *testing.T) {
	ref := session.SessionRef{SessionID: "fixture"}
	input := policy.ToolContext{
		Session: session.Session{SessionRef: ref, CWD: t.TempDir()},
		Tool:    (&shell.RunCommandTool{}).Definition(),
		Call: tool.Call{
			ID: "command-call", Name: shell.RunCommandToolName,
			Input: []byte(`{"command":"printf fixture","sandbox_permissions":"require_escalated","justification":"Fixture requires the Host route."}`),
		},
	}
	decision, err := presets.WorkspaceWriteMode().DecideTool(t.Context(), input)
	if err != nil || decision.Action != policy.ActionAskApproval || decision.Approval == nil {
		t.Fatalf("expected native approval decision, got %#v, %v", decision, err)
	}
	display := approval.ProtocolApprovalFromPayload(approval.DisplayPayloadFromRuntimeRequest(approval.Request{
		SessionRef: ref, Tool: input.Tool, Call: input.Call, Approval: decision.Approval, Metadata: decision.Metadata,
	}))
	envelopes := projection.ProjectSessionEventEnvelope(eventstream.Envelope{SessionID: ref.SessionID}, &session.Event{
		ID: "approval-event", Seq: 1, SessionID: ref.SessionID, Type: session.EventTypeLifecycle,
		Protocol: &session.EventProtocol{Method: session.ProtocolMethodRequestPermission, Permission: display},
	})
	if len(envelopes) != 1 || envelopes[0].Permission == nil {
		t.Fatalf("expected projected approval, got %#v", envelopes)
	}
	call := envelopes[0].Permission.ToolCall
	if call.Title == nil || *call.Title != shell.RunCommandToolName+" printf fixture" || call.Kind == nil || *call.Kind != "execute" {
		t.Fatalf("wire tool call = %#v, want original native approval title and kind", call)
	}
	req := approvalPayloadFromACPEvent(envelopes[0])
	prompt := approvalToPromptRequest(req, make(chan PromptResponse, 1))
	model := NewModel(Config{InitialPrompt: &prompt, NoColor: true, NoAnimation: true})
	model.width, model.height = 120, 40
	frame := evalharness.NormalizeFrame(model.renderPromptModal())
	for _, want := range []string{"Approval Required", "printf fixture", "run the supplied command", "Allow once", "Reject once"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("approval frame missing %q:\n%s", want, frame)
		}
	}
}
