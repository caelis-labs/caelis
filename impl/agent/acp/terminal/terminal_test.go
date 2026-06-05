package terminal

import (
	"context"
	"iter"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
)

func TestLocalTerminalAdapterOutputUsesCumulativeRead(t *testing.T) {
	t.Parallel()

	service := &recordingTerminalService{
		snapshot: stream.Snapshot{
			Frames: []stream.Frame{{Text: "one\ntwo\n"}},
			Cursor: stream.Cursor{Output: 8},
		},
	}
	adapter := LocalTerminalAdapter{Streams: service}

	resp, err := adapter.Output(context.Background(), TerminalOutputRequest{
		SessionID:  "session-1",
		TerminalID: "terminal-1",
	})
	if err != nil {
		t.Fatalf("Output() error = %v", err)
	}
	if resp.Output != "one\ntwo\n" {
		t.Fatalf("Output() = %q, want cumulative terminal output", resp.Output)
	}
	if service.readReq.Cursor.Output != 0 {
		t.Fatalf("Read cursor = %+v, want zero cursor for ACP cumulative output", service.readReq.Cursor)
	}
}

func TestLocalTerminalAdapterOutputSuppressesNoOutputPlaceholder(t *testing.T) {
	t.Parallel()

	adapter := LocalTerminalAdapter{Streams: &recordingTerminalService{
		snapshot: stream.Snapshot{
			FinalText: "(no output)",
			ExitCode:  intPtr(0),
		},
	}}

	resp, err := adapter.Output(context.Background(), TerminalOutputRequest{
		SessionID:  "session-1",
		TerminalID: "terminal-1",
	})
	if err != nil {
		t.Fatalf("Output() error = %v", err)
	}
	if resp.Output != "" {
		t.Fatalf("Output() = %q, want empty captured terminal output", resp.Output)
	}
	if resp.ExitStatus == nil || resp.ExitStatus.ExitCode == nil || *resp.ExitStatus.ExitCode != 0 {
		t.Fatalf("ExitStatus = %#v, want exit code 0", resp.ExitStatus)
	}
}

func TestRefFromEventUsesSemanticToolResultTaskMetadata(t *testing.T) {
	t.Parallel()

	event := session.CanonicalizeEvent(&session.Event{
		SessionID:  "root-session",
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical,
		Tool: &session.EventTool{
			ID:     "spawn-1",
			Name:   "SPAWN",
			Status: "running",
			Output: map[string]any{"task_id": "reya", "state": "running"},
			Content: []session.EventToolContent{{
				Type:       "terminal",
				TerminalID: "subagent-task-1",
			}},
		},
		Meta: map[string]any{
			"caelis": map[string]any{
				"version": 1,
				"runtime": map[string]any{
					"tool": map[string]any{"name": "SPAWN"},
					"task": map[string]any{
						"task_id":     "reya",
						"terminal_id": "subagent-task-1",
						"running":     true,
					},
				},
			},
		},
	})
	if event.Meta != nil || event.Tool != nil || event.Protocol != nil {
		t.Fatalf("canonical event kept legacy projections: tool=%#v protocol=%#v meta=%#v", event.Tool, event.Protocol, event.Meta)
	}
	ref, ok := RefFromEvent(event)
	if !ok {
		t.Fatal("RefFromEvent() ok = false, want semantic ref")
	}
	if ref.SessionID != "root-session" || ref.TaskID != "reya" || ref.TerminalID != "subagent-task-1" {
		t.Fatalf("RefFromEvent() = %#v, want root-session/reya/subagent-task-1", ref)
	}
}

func intPtr(value int) *int {
	return &value
}

type recordingTerminalService struct {
	readReq  stream.ReadRequest
	snapshot stream.Snapshot
}

func (s *recordingTerminalService) Read(_ context.Context, req stream.ReadRequest) (stream.Snapshot, error) {
	s.readReq = req
	return s.snapshot, nil
}

func (s *recordingTerminalService) Subscribe(context.Context, stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	return func(func(*stream.Frame, error) bool) {}
}
