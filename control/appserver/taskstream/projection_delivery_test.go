package taskstream

import (
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

func TestProjectTaskFrameSeparatesDelegatedSemanticsFromCommandTerminalOutput(t *testing.T) {
	t.Parallel()

	taskRequest := spawnProjectionRequestForTest()
	taskRequest.CallID = "task-call-1"
	taskRequest.ToolName = "Task"
	taskRequest.DisplayTerminalID = "task-call-1"
	commandRequest := taskFrameProjectionRequest{
		SessionID:         "root-session",
		CallID:            "command-call-1",
		ToolName:          "RunCommand",
		TaskID:            "command-task-1",
		TurnID:            "command-terminal-1",
		DisplayTerminalID: "command-call-1",
		Scope:             eventstream.ScopeMain,
	}
	tests := []struct {
		name  string
		req   taskFrameProjectionRequest
		frame controltaskstream.Frame
		want  []streamFrameEnvelopeExpectation
	}{
		{
			name: "task child event with materialized text",
			req:  taskRequest,
			frame: controltaskstream.Frame{
				TerminalID: taskRequest.TurnID,
				Text:       "child message\n",
				Running:    true,
				Event:      childMessageEventForStreamTest("child message"),
			},
			want: []streamFrameEnvelopeExpectation{
				{parentCallID: "task-call-1", parentTool: "Task", transient: true},
			},
		},
		{
			name: "spawn text only",
			req:  spawnProjectionRequestForTest(),
			frame: controltaskstream.Frame{
				TerminalID: spawnProjectionRequestForTest().TurnID,
				Text:       "child text only\n",
				Running:    true,
			},
		},
		{
			name: "spawn final result has no terminal replay",
			req:  spawnProjectionRequestForTest(),
			frame: controltaskstream.Frame{
				TerminalID: spawnProjectionRequestForTest().TurnID,
				Text:       "final child result\n",
				Closed:     true,
				State:      "completed",
			},
			want: []streamFrameEnvelopeExpectation{{parentCallID: "spawn-call-1", parentTool: "Spawn", transient: true}},
		},
		{
			name: "run command running",
			req:  commandRequest,
			frame: controltaskstream.Frame{
				TerminalID: commandRequest.TurnID,
				Text:       "running output\n",
				Running:    true,
			},
			want: []streamFrameEnvelopeExpectation{
				{transient: true, terminalOutput: "running output\n", hasTerminalOutput: true},
			},
		},
		{
			name: "run command final",
			req:  commandRequest,
			frame: controltaskstream.Frame{
				TerminalID: commandRequest.TurnID,
				Text:       "final output\n",
				Closed:     true,
				State:      "completed",
			},
			want: []streamFrameEnvelopeExpectation{
				{transient: true, terminalOutput: "final output\n", hasTerminalOutput: true},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			events := projectTaskStreamFrame(tt.req, tt.frame)
			if len(events) != len(tt.want) {
				t.Fatalf("projectTaskStreamFrame() returned %d events, want %d: %#v", len(events), len(tt.want), events)
			}
			for i, want := range tt.want {
				assertStreamFrameEnvelopeExpectation(t, events[i], want)
			}
		})
	}
}
