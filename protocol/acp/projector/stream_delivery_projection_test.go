package projector

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestProjectStreamFrameSeparatesDelegatedSemanticsFromCommandTerminalOutput(t *testing.T) {
	t.Parallel()

	taskRequest := spawnStreamRequestForTest()
	taskRequest.CallID = "task-call-1"
	taskRequest.ToolName = "TASK"
	taskRequest.DisplayTerminalID = "task-call-1"
	commandRequest := StreamRequest{
		SessionRef:        session.SessionRef{SessionID: "root-session"},
		CallID:            "command-call-1",
		ToolName:          "RUN_COMMAND",
		Ref:               stream.Ref{SessionID: "root-session", TaskID: "command-task-1", TerminalID: "command-terminal-1"},
		DisplayTerminalID: "command-call-1",
		Scope:             eventstream.ScopeMain,
	}
	tests := []struct {
		name  string
		req   StreamRequest
		frame stream.Frame
		want  []streamFrameEnvelopeExpectation
	}{
		{
			name: "task child event with materialized text",
			req:  taskRequest,
			frame: stream.Frame{
				Ref:     taskRequest.Ref,
				Text:    "child message\n",
				Running: true,
				Event:   childMessageEventForStreamTest("child message"),
			},
			want: []streamFrameEnvelopeExpectation{
				{parentCallID: "task-call-1", parentTool: "TASK", transient: true},
			},
		},
		{
			name: "spawn text only",
			req:  spawnStreamRequestForTest(),
			frame: stream.Frame{
				Ref:     spawnStreamRequestForTest().Ref,
				Text:    "child text only\n",
				Running: true,
			},
		},
		{
			name: "spawn final result has no terminal replay",
			req:  spawnStreamRequestForTest(),
			frame: stream.Frame{
				Ref:    spawnStreamRequestForTest().Ref,
				Text:   "final child result\n",
				Closed: true,
				State:  "completed",
			},
			want: []streamFrameEnvelopeExpectation{{transient: true}},
		},
		{
			name: "run command running",
			req:  commandRequest,
			frame: stream.Frame{
				Ref:     commandRequest.Ref,
				Text:    "running output\n",
				Running: true,
			},
			want: []streamFrameEnvelopeExpectation{
				{transient: true, terminalOutput: "running output\n", hasTerminalOutput: true},
			},
		},
		{
			name: "run command final",
			req:  commandRequest,
			frame: stream.Frame{
				Ref:    commandRequest.Ref,
				Text:   "final output\n",
				Cursor: stream.Cursor{Output: 0},
				Closed: true,
				State:  "completed",
			},
			want: []streamFrameEnvelopeExpectation{
				{transient: true, terminalOutput: "final output\n", hasTerminalOutput: true},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			events := ProjectStreamFrame(tt.req, tt.frame)
			if len(events) != len(tt.want) {
				t.Fatalf("ProjectStreamFrame() returned %d events, want %d: %#v", len(events), len(tt.want), events)
			}
			for i, want := range tt.want {
				assertStreamFrameEnvelopeExpectation(t, events[i], want)
			}
		})
	}
}
