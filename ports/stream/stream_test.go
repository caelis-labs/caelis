package stream

import (
	"encoding/json"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestFrameEventJSONRoundTrip(t *testing.T) {
	t.Parallel()

	frame := Frame{
		Ref:     Ref{SessionID: "root-session", TaskID: "task-1"},
		Text:    "fallback",
		Running: true,
		Event: &session.Event{
			Type:       session.EventTypeToolCall,
			Visibility: session.VisibilityCanonical,
			Text:       "run tests",
			Protocol: &session.EventProtocol{
				UpdateType: string(session.ProtocolUpdateTypeToolCall),
				ToolCall: &session.ProtocolToolCall{
					ID:       "call-1",
					Name:     "RUN_COMMAND",
					Status:   "pending",
					RawInput: map[string]any{"command": "go test ./...", "limit": 3},
				},
			},
		},
	}

	data, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("json.Marshal(Frame) error = %v", err)
	}
	var decoded Frame
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(Frame) error = %v", err)
	}
	if decoded.Event == nil || decoded.Event.Protocol == nil || decoded.Event.Protocol.ToolCall == nil {
		t.Fatalf("decoded.Event = %#v, want tool call event", decoded.Event)
	}
	if decoded.Event.Type != session.EventTypeToolCall || decoded.Event.Protocol.ToolCall.Name != "RUN_COMMAND" {
		t.Fatalf("decoded.Event = %#v, want RUN_COMMAND tool call", decoded.Event)
	}
	if got := decoded.Event.Protocol.ToolCall.RawInput["command"]; got != "go test ./..." {
		t.Fatalf("decoded command = %#v, want go test ./...", got)
	}
	if got := decoded.Event.Protocol.ToolCall.RawInput["limit"]; got != float64(3) {
		t.Fatalf("decoded limit = %#v, want JSON number 3", got)
	}
}

func TestCloneFrameClonesEvent(t *testing.T) {
	t.Parallel()

	frame := Frame{
		Event: &session.Event{
			Protocol: &session.EventProtocol{
				ToolCall: &session.ProtocolToolCall{
					RawInput: map[string]any{"command": "echo hi"},
				},
			},
		},
	}
	cloned := CloneFrame(frame)
	cloned.Event.Protocol.ToolCall.RawInput["command"] = "changed"
	if got := frame.Event.Protocol.ToolCall.RawInput["command"]; got != "echo hi" {
		t.Fatalf("source command = %#v, want unchanged clone isolation", got)
	}
}
