package acpagentbridge

import (
	"context"
	"reflect"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestEmitControlPermissionEnvelopePreservesStandardWire(t *testing.T) {
	t.Parallel()

	line := 17
	oldText := "before"
	title := "Run command"
	kind := eventstream.ToolKindExecute
	status := eventstream.ToolStatusPending
	permission := eventstream.RequestPermissionRequest{
		SessionID: "session-1",
		ToolCall: eventstream.ToolCallUpdate{
			ToolCallID: "call-1",
			Title:      &title,
			Kind:       &kind,
			Status:     &status,
			RawInput:   []any{"printf", "ok"},
			RawOutput:  "pending",
			Locations:  []eventstream.ToolCallLocation{{Path: "/tmp/file", Line: &line}},
			Content: []eventstream.ToolCallContent{
				{Type: "content", Content: eventstream.TextContent{Type: "text", Text: "detail"}},
				{Type: "diff", Path: "/tmp/file", OldText: &oldText, NewText: "after"},
				{Type: "terminal", TerminalID: "terminal-1"},
			},
			Meta: map[string]any{"vendor": map[string]any{"trace": "tool-only"}},
		},
		Options: []acpsdk.PermissionOption{{OptionId: "allow-once", Name: "Allow once", Kind: acpsdk.PermissionOptionKindAllowOnce}},
		Meta:    map[string]any{"request": "kept"},
	}
	callbacks := &permissionWireCallbacks{}
	turn := &permissionWireTurn{}
	err := (&RuntimeAgent{}).emitControlEnvelope(context.Background(), callbacks, "fallback", turn, eventstream.Envelope{
		Kind: eventstream.KindRequestPermission, ApprovalRequestID: "approval-1", Permission: &permission,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := callbacks.request
	if request.SessionId != "session-1" || request.ToolCall.ToolCallId != "call-1" || len(request.Options) != 1 {
		t.Fatalf("SDK request = %#v", request)
	}
	if !reflect.DeepEqual(request.ToolCall.RawInput, []any{"printf", "ok"}) || request.ToolCall.RawOutput != "pending" {
		t.Fatalf("SDK raw values = input %#v output %#v", request.ToolCall.RawInput, request.ToolCall.RawOutput)
	}
	if len(request.ToolCall.Locations) != 1 || request.ToolCall.Locations[0].Path != "/tmp/file" || request.ToolCall.Locations[0].Line == nil || *request.ToolCall.Locations[0].Line != 17 {
		t.Fatalf("SDK locations = %#v", request.ToolCall.Locations)
	}
	if len(request.ToolCall.Content) != 3 || request.ToolCall.Content[0].Content == nil || request.ToolCall.Content[1].Diff == nil || request.ToolCall.Content[2].Terminal == nil {
		t.Fatalf("SDK content = %#v", request.ToolCall.Content)
	}
	if string(request.Meta["request"]) != `"kept"` || string(request.ToolCall.Meta["vendor"]) != `{"trace":"tool-only"}` {
		t.Fatalf("SDK metadata = request %#v tool %#v", request.Meta, request.ToolCall.Meta)
	}
	if !turn.decision.Approved || turn.decision.RequestID != "approval-1" || turn.decision.OptionID != "allow-once" {
		t.Fatalf("approval decision = %#v", turn.decision)
	}
}

func TestSDKPermissionRequestFromApprovalUsesStrictStandardWire(t *testing.T) {
	t.Parallel()

	request, err := sdkPermissionRequestFromApproval(session.SessionRef{SessionID: "session-1"}, &session.ProtocolApproval{
		ToolCall: session.ProtocolToolCall{
			ID: "call-1", Name: "RunCommand", Kind: eventstream.ToolKindExecute,
			RawInput: map[string]any{"command": "go test ./..."},
			Content: []session.ProtocolToolCallContent{{
				Type: "content", Content: map[string]any{"type": "text", "text": "detail"},
			}},
		},
		Options: []session.ProtocolApprovalOption{{ID: "allow-once", Name: "Allow once", Kind: "allow_once"}},
	}, map[string]any{"request": "kept"})
	if err != nil {
		t.Fatal(err)
	}
	if request.ToolCall.ToolCallId != "call-1" || len(request.ToolCall.Content) != 1 || request.ToolCall.Content[0].Content == nil {
		t.Fatalf("SDK request = %#v", request)
	}
	if string(request.Meta["request"]) != `"kept"` || request.ToolCall.Meta["caelis"] == nil {
		t.Fatalf("SDK metadata = request %#v tool %#v", request.Meta, request.ToolCall.Meta)
	}
}

func TestSDKPermissionRequestFromApprovalRejectsInvalidWire(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		approval session.ProtocolApproval
	}{
		{name: "missing options", approval: session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "call-1"}}},
		{name: "nonstandard content", approval: session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{ID: "call-1", Content: []session.ProtocolToolCallContent{{Type: "vendor"}}},
			Options:  []session.ProtocolApprovalOption{},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := sdkPermissionRequestFromApproval(session.SessionRef{SessionID: "session-1"}, &test.approval, nil); err == nil {
				t.Fatal("sdkPermissionRequestFromApproval() error = nil")
			}
		})
	}
}

type permissionWireCallbacks struct {
	request acpsdk.RequestPermissionRequest
}

func (*permissionWireCallbacks) SessionUpdate(context.Context, eventstream.SessionNotification) error {
	return nil
}

func (c *permissionWireCallbacks) RequestPermission(_ context.Context, request acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	c.request = request
	return acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.NewRequestPermissionOutcomeSelected("allow-once"),
	}, nil
}

type permissionWireTurn struct {
	decision controlprompt.ApprovalDecision
}

func (*permissionWireTurn) HandleID() string { return "handle-1" }
func (*permissionWireTurn) RunID() string    { return "run-1" }
func (*permissionWireTurn) TurnID() string   { return "turn-1" }
func (*permissionWireTurn) Events() <-chan eventstream.Envelope {
	return nil
}
func (t *permissionWireTurn) SubmitApproval(_ context.Context, decision controlprompt.ApprovalDecision) error {
	t.decision = decision
	return nil
}
func (*permissionWireTurn) Cancel()      {}
func (*permissionWireTurn) Close() error { return nil }
