package stream

import (
	"encoding/json"
	"testing"

	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestFrameEventJSONRoundTrip(t *testing.T) {
	t.Parallel()

	frame := Frame{
		Ref:     Ref{SessionID: "root-session", TaskID: "task-1"},
		Stream:  "stdout",
		Text:    "fallback",
		Running: true,
		Event: &sdksession.Event{
			Type:       sdksession.EventTypeToolCall,
			Visibility: sdksession.VisibilityCanonical,
			Text:       "run tests",
			Protocol: &sdksession.EventProtocol{
				UpdateType: string(sdksession.ProtocolUpdateTypeToolCall),
				ToolCall: &sdksession.ProtocolToolCall{
					ID:       "call-1",
					Name:     "BASH",
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
	if decoded.Event.Type != sdksession.EventTypeToolCall || decoded.Event.Protocol.ToolCall.Name != "BASH" {
		t.Fatalf("decoded.Event = %#v, want BASH tool call", decoded.Event)
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
		Event: &sdksession.Event{
			Protocol: &sdksession.EventProtocol{
				ToolCall: &sdksession.ProtocolToolCall{
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
