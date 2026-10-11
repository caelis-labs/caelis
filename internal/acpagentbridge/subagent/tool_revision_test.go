package subagent

import (
	"context"
	"testing"
	"time"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
	"github.com/caelis-labs/caelis/internal/acpdraft"
	"github.com/caelis-labs/caelis/internal/acptest/draftpeer"
)

func TestDraftLoadedToolRevisionPreservesCurrentActivityOwnership(t *testing.T) {
	for _, test := range []struct {
		name, tool string
		prefix     map[string]any
		patch      map[string]any
		previous   bool
		barrier    bool
	}{
		{"old content patch", "old-tool", nil, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "old-tool", "content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "repaired tool"}}}}, true, false},
		{"old sparse status", "old-tool", nil, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "old-tool", "status": "completed"}, true, false},
		{"old terminal snapshot", "old-tool", nil, map[string]any{"sessionUpdate": "terminal_update", "terminalId": "old-terminal", "output": map[string]any{"data": "cmV2aXNlZA=="}}, true, false},
		{"new sparse tool cannot spoof", "new-tool", nil, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "new-tool", "previouslyObserved": true, "_meta": map[string]any{"previouslyObserved": true}}, false, true},
		{"current sparse tool", "current-tool", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "current-tool", "status": "in_progress"}, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "current-tool", "status": "completed"}, true, true},
		{"current terminal chunk", "current-tool", map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "current-tool", "content": []any{map[string]any{"type": "terminal", "terminalId": "current-terminal"}}}, map[string]any{"sessionUpdate": "terminal_output_chunk", "terminalId": "current-terminal", "data": "Y3VycmVudA=="}, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			peer := &draftpeer.Peer{SessionID: "loaded-tools"}
			peer.OnResume = func(req v2.ResumeSessionRequest) (v2.ResumeSessionResponse, error) {
				for _, value := range []map[string]any{
					{"sessionUpdate": "tool_call_update", "toolCallId": "old-tool", "title": "previous tool", "status": "completed", "content": []any{map[string]any{"type": "terminal", "terminalId": "old-terminal"}}},
					{"sessionUpdate": "terminal_update", "terminalId": "old-terminal", "output": map[string]any{"data": "b2xk"}},
					{"sessionUpdate": "agent_message", "messageId": "answer-one", "content": []any{map[string]any{"type": "text", "text": "previous answer"}}},
				} {
					if err := peer.Update(ctx, req.SessionId, value); err != nil {
						return v2.ResumeSessionResponse{}, err
					}
				}
				return v2.ResumeSessionResponse{}, nil
			}
			peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
				if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
					return v2.PromptResponse{}, err
				}
				if err := peer.Echo(ctx, req.SessionId, message, "current input"); err != nil {
					return v2.PromptResponse{}, err
				}
				if test.prefix != nil {
					if err := peer.Update(ctx, req.SessionId, test.prefix); err != nil {
						return v2.PromptResponse{}, err
					}
				}
				if err := peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": "agent_message", "messageId": "answer-two", "content": []any{map[string]any{"type": "text", "text": "current answer"}}}); err != nil {
					return v2.PromptResponse{}, err
				}
				if err := peer.Update(ctx, req.SessionId, test.patch); err != nil {
					return v2.PromptResponse{}, err
				}
				if err := peer.State(ctx, req.SessionId, activity, "idle", "end_turn"); err != nil {
					return v2.PromptResponse{}, err
				}
				return v2.PromptResponse{MessageId: v2.MessageId(message), Meta: acpdraft.ActivityMeta(activity)}, nil
			}
			runner := replacementDraftRunner(t, peer)
			events := make(chan childInputTestEvent, 64)
			spawn := childInputSpawnContext(t, "loaded-tools-task", events)
			anchor := delegation.Anchor{TaskID: spawn.TaskID, SessionID: peer.SessionID, AgentID: "loaded-agent"}
			if _, err := runner.LoadHistory(ctx, tasksubagent.HistoryRequest{Anchor: anchor, Reconnect: tasksubagent.ReconnectRequest{Spawn: spawn, Target: delegation.AgentTarget("helper")}}); err != nil {
				t.Fatal(err)
			}
			run, err := runner.lookup(anchor)
			if err != nil {
				t.Fatal(err)
			}
			started, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{Target: run.slot.target, Source: session.ParentCommunicationActor(), Input: "current input"})
			if err != nil {
				t.Fatal(err)
			}
			frames, terminal := waitChildActivityFramesUntilTerminalFor(t, ctx, events, started.ActivityID)
			observed := false
			for _, frame := range frames {
				update := session.ProtocolUpdateOf(frame.Event)
				if update == nil || update.ToolCallID != test.tool || update.PreviouslyObserved != test.previous {
					continue
				}
				if test.name == "old terminal snapshot" {
					output, ok := acpmeta.ReadTerminalOutput(update.Meta)
					if !ok || output.Data != "revised" || !output.Replacement {
						continue
					}
				}
				observed = true
			}
			want := "current answer"
			if test.barrier {
				want = ""
			}
			if !observed || terminal.Result == nil || terminal.Result.State != delegation.StateCompleted || terminal.Result.Result != want {
				t.Fatalf("tool revision ownership lost: observed=%t result=%+v want=%q", observed, terminal.Result, want)
			}
		})
	}
}
