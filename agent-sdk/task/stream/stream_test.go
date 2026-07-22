package stream

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestValidateRefRequiresSessionAndTaskIdentity(t *testing.T) {
	t.Parallel()

	if err := ValidateRef(Ref{SessionID: "session-1", TaskID: "task-1"}); err != nil {
		t.Fatalf("ValidateRef(valid) error = %v", err)
	}
	for _, test := range []struct {
		name string
		ref  Ref
		want string
	}{
		{name: "bare task", ref: Ref{TaskID: "task-1"}, want: "session_id"},
		{name: "bare session", ref: Ref{SessionID: "session-1"}, want: "task_id"},
		{name: "terminal only", ref: Ref{TerminalID: "terminal-1"}, want: "session_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateRef(test.ref); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateRef(%#v) error = %v, want %s requirement", test.ref, err, test.want)
			}
		})
	}
}

func TestTaskStreamTerminalStatesRequireExplicitEvidence(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"completed", "failed", "cancelled", "interrupted", "terminated", "unknown_outcome"} {
		if !IsTerminalState(state) {
			t.Errorf("IsTerminalState(%q) = false, want true", state)
		}
	}
	for _, state := range []string{"", "prepared", "running", "waiting_input", "waiting_approval"} {
		if IsTerminalState(state) {
			t.Errorf("IsTerminalState(%q) = true, want false", state)
		}
		frames := FramesForSnapshot(Snapshot{
			Ref: Ref{SessionID: "session-1", TaskID: "task-1"}, State: state, Running: false,
		})
		if len(frames) != 0 {
			t.Errorf("FramesForSnapshot(%q) = %#v, want no inferred completion", state, frames)
		}
	}
}

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
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
					ToolCallID:    "call-1",
					Kind:          "RUN_COMMAND",
					Status:        "pending",
					RawInput:      map[string]any{"command": "go test ./...", "limit": 3},
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
	update := session.ProtocolUpdateOf(decoded.Event)
	if decoded.Event == nil || decoded.Event.Protocol == nil || update == nil {
		t.Fatalf("decoded.Event = %#v, want tool call event", decoded.Event)
	}
	if decoded.Event.Type != session.EventTypeToolCall || update.Kind != "RUN_COMMAND" {
		t.Fatalf("decoded.Event = %#v, want RUN_COMMAND tool call", decoded.Event)
	}
	if got := update.RawInput["command"]; got != "go test ./..." {
		t.Fatalf("decoded command = %#v, want go test ./...", got)
	}
	if got := update.RawInput["limit"]; got != float64(3) {
		t.Fatalf("decoded limit = %#v, want JSON number 3", got)
	}
}

func TestCloneFrameClonesEvent(t *testing.T) {
	t.Parallel()

	exitCode := 7
	frame := Frame{
		ExitCode: &exitCode,
		Event: &session.Event{
			Protocol: &session.EventProtocol{
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
					RawInput:      map[string]any{"command": "echo hi"},
				},
			},
		},
	}
	cloned := CloneFrame(frame)
	cloned.Event.Protocol.Update.RawInput["command"] = "changed"
	*cloned.ExitCode = 9
	if got := frame.Event.Protocol.Update.RawInput["command"]; got != "echo hi" {
		t.Fatalf("source command = %#v, want unchanged clone isolation", got)
	}
	if *frame.ExitCode != exitCode {
		t.Fatalf("source exit code = %d, want unchanged %d", *frame.ExitCode, exitCode)
	}
}

func TestFramesForSnapshotBuildsTerminalFrame(t *testing.T) {
	t.Parallel()

	exitCode := 7
	frames := FramesForSnapshot(Snapshot{
		Ref:       Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "term-1"},
		Cursor:    Cursor{Output: 12, Events: 3},
		FinalText: "ignored for command exit",
		State:     "failed",
		Running:   false,
		ExitCode:  &exitCode,
	})
	if len(frames) != 1 {
		t.Fatalf("frames = %#v, want one generated close frame", frames)
	}
	close := frames[0]
	if !close.Closed || close.Running || close.State != "failed" || close.Text != "" {
		t.Fatalf("close frame = %#v, want failed contentless close", close)
	}
	if close.ExitCode == nil || *close.ExitCode != exitCode {
		t.Fatalf("close exit code = %#v, want %d", close.ExitCode, exitCode)
	}
	*close.ExitCode = 9
	if *frames[0].ExitCode != 9 || exitCode != 7 {
		t.Fatalf("close exit clone = %#v, source exit code = %d; want isolated source", frames[0].ExitCode, exitCode)
	}
}

func TestFramesForSnapshotNormalizesExistingClose(t *testing.T) {
	t.Parallel()

	frames := FramesForSnapshot(Snapshot{
		Ref:       Ref{SessionID: "session-1", TaskID: "task-1"},
		Cursor:    Cursor{Output: 4},
		FinalText: "child result",
		State:     "completed",
		Running:   false,
		Frames:    []Frame{{Closed: true}},
	})
	if len(frames) != 1 {
		t.Fatalf("frames = %#v, want one normalized close frame", frames)
	}
	if got := frames[0]; !got.Closed || got.Running || got.Text != "child result" || got.State != "completed" || got.Ref.TaskID != "task-1" {
		t.Fatalf("normalized close = %#v, want snapshot terminal semantics", got)
	}
}
