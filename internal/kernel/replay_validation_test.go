package kernel

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestValidateReplaySessionEventsRejectsUnboundedToolOutput(t *testing.T) {
	t.Parallel()

	err := validateReplaySessionEvents([]*session.Event{{
		ID:   "tool-1",
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID:   "call-1",
			Name: "BASH",
			Output: map[string]any{
				"stderr": strings.Repeat("permission denied\n", tool.DefaultTruncationPolicy().ByteBudget()),
			},
			Content: []session.EventToolContent{{Type: "terminal", Text: "permission denied"}},
		},
	}})
	if err == nil {
		t.Fatal("validateReplaySessionEvents() error = nil, want non-canonical replay rejection")
	}
	var gatewayErr *Error
	if !errors.As(err, &gatewayErr) || gatewayErr.Kind != KindValidation || gatewayErr.Code != CodeInvalidRequest {
		t.Fatalf("error = %#v, want validation invalid_request", err)
	}
	if !strings.Contains(gatewayErr.Detail, "tool-1") || !strings.Contains(gatewayErr.Detail, "canonical-truncated") {
		t.Fatalf("error detail = %q, want event id and canonical truncation reason", gatewayErr.Detail)
	}
}

func TestValidateReplaySessionEventsRejectsOutputFieldsInMeta(t *testing.T) {
	t.Parallel()

	err := validateReplaySessionEvents([]*session.Event{{
		ID:   "tool-2",
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID:      "call-2",
			Name:    "BASH",
			Output:  map[string]any{"stdout": "ok"},
			Content: []session.EventToolContent{{Type: "terminal", Text: "ok"}},
		},
		Meta: map[string]any{"stderr": "duplicated"},
	}})
	if err == nil {
		t.Fatal("validateReplaySessionEvents() error = nil, want meta output rejection")
	}
	var gatewayErr *Error
	if !errors.As(err, &gatewayErr) || !strings.Contains(gatewayErr.Detail, `field "stderr"`) {
		t.Fatalf("error = %#v, want stderr meta rejection", err)
	}
}

func TestValidateReplaySessionEventsAllowsCanonicalEscapableOutput(t *testing.T) {
	t.Parallel()

	stdout := strings.Repeat("\n", tool.DefaultTruncationPolicy().ByteBudget()-16)
	if _, info := tool.TruncateMap(map[string]any{"stdout": stdout}, tool.DefaultTruncationPolicy()); info.Truncated {
		t.Fatalf("test fixture unexpectedly exceeds truncation estimator: %#v", info)
	}
	err := validateReplaySessionEvents([]*session.Event{{
		ID:   "tool-3",
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID:      "call-3",
			Name:    "BASH",
			Output:  map[string]any{"stdout": stdout},
			Content: []session.EventToolContent{{Type: "terminal", Text: "ok"}},
		},
	}})
	if err != nil {
		t.Fatalf("validateReplaySessionEvents() error = %v, want canonical escapable output accepted", err)
	}
}

func TestValidateReplaySessionEventsRejectsProtocolOnlyToolResult(t *testing.T) {
	t.Parallel()

	err := validateReplaySessionEvents([]*session.Event{{
		ID:   "tool-old",
		Type: session.EventTypeToolResult,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			RawOutput: map[string]any{"stdout": "ok"},
			Content:   []session.ProtocolToolCallContent{{Type: "terminal", Content: session.ProtocolTextContent("ok")}},
		}},
	}})
	if err == nil {
		t.Fatal("validateReplaySessionEvents() error = nil, want protocol-only rejection")
	}
	var gatewayErr *Error
	if !errors.As(err, &gatewayErr) || !strings.Contains(gatewayErr.Detail, "missing durable Event.Tool") {
		t.Fatalf("error = %#v, want durable Event.Tool rejection", err)
	}
}

func TestValidateReplaySessionEventsRejectsProtocolOnlyNarrative(t *testing.T) {
	t.Parallel()

	err := validateReplaySessionEvents([]*session.Event{{
		ID:   "assistant-old",
		Type: session.EventTypeAssistant,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
			Content:       session.ProtocolTextContent("ok"),
		}},
	}})
	if err == nil {
		t.Fatal("validateReplaySessionEvents() error = nil, want protocol-only narrative rejection")
	}
	var gatewayErr *Error
	if !errors.As(err, &gatewayErr) || !strings.Contains(gatewayErr.Detail, "missing durable Event.Message") {
		t.Fatalf("error = %#v, want durable Event.Message rejection", err)
	}
}

func TestValidateReplaySessionEventsRejectsProtocolOnlyToolCall(t *testing.T) {
	t.Parallel()

	err := validateReplaySessionEvents([]*session.Event{{
		ID:   "tool-call-old",
		Type: session.EventTypeToolCall,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
			ToolCallID:    "call-1",
			Kind:          "BASH",
			RawInput:      map[string]any{"command": "echo hi"},
		}},
	}})
	if err == nil {
		t.Fatal("validateReplaySessionEvents() error = nil, want protocol-only tool call rejection")
	}
	var gatewayErr *Error
	if !errors.As(err, &gatewayErr) || !strings.Contains(gatewayErr.Detail, "missing durable Event.Tool") {
		t.Fatalf("error = %#v, want durable Event.Tool rejection", err)
	}
}

func TestValidateReplaySessionEventsAllowsModelToolCallPayload(t *testing.T) {
	t.Parallel()

	args, err := json.Marshal(map[string]any{"command": "echo hi"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	msg := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
		ID:   "call-1",
		Name: "BASH",
		Args: string(args),
	}}, "")
	err = validateReplaySessionEvents([]*session.Event{{
		ID:      "tool-call-message",
		Type:    session.EventTypeToolCall,
		Message: &msg,
	}})
	if err != nil {
		t.Fatalf("validateReplaySessionEvents() error = %v, want canonical model tool-call payload accepted", err)
	}
}
