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
	"github.com/caelis-labs/caelis/internal/acpdraft"
	"github.com/caelis-labs/caelis/internal/acptest/draftpeer"
)

func TestDraftDistinctOpaqueMessageIDsCannotAliasAcrossActivities(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	peer := &draftpeer.Peer{SessionID: "loaded-child"}
	peer.OnResume = func(req v2.ResumeSessionRequest) (v2.ResumeSessionResponse, error) {
		err := peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": "agent_message", "messageId": " answer ", "content": []any{map[string]any{"type": "text", "text": "previous answer"}}})
		return v2.ResumeSessionResponse{}, err
	}
	peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
		if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := peer.Echo(ctx, req.SessionId, message, "current input"); err != nil {
			return v2.PromptResponse{}, err
		}
		for _, frame := range []struct{ id, text string }{{"answer", "current answer"}, {" answer ", "revised previous answer"}} {
			if err := peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": "agent_message", "messageId": frame.id, "content": []any{map[string]any{"type": "text", "text": frame.text}}}); err != nil {
				return v2.PromptResponse{}, err
			}
		}
		if err := peer.State(ctx, req.SessionId, activity, "idle", "end_turn"); err != nil {
			return v2.PromptResponse{}, err
		}
		return v2.PromptResponse{MessageId: v2.MessageId(message), Meta: acpdraft.ActivityMeta(activity)}, nil
	}
	runner := replacementDraftRunner(t, peer)
	events := make(chan childInputTestEvent, 64)
	spawn := childInputSpawnContext(t, "loaded-task", events)
	anchor := delegation.Anchor{TaskID: spawn.TaskID, SessionID: peer.SessionID, AgentID: "loaded-agent"}
	loaded, err := runner.LoadHistory(ctx, tasksubagent.HistoryRequest{Anchor: anchor, Reconnect: tasksubagent.ReconnectRequest{Spawn: spawn, Target: delegation.AgentTarget("helper")}})
	if err != nil {
		t.Fatal(err)
	}
	oldID := " answer "
	kind := string(session.ProtocolUpdateTypeAgentMessage)
	loadedIdentity := false
	for _, event := range loaded.Events {
		if update := session.ProtocolUpdateOf(event); update != nil && update.SessionUpdate == kind && update.MessageID == oldID {
			loadedIdentity = true
		}
	}
	if !loadedIdentity {
		t.Fatalf("LoadHistory lost raw identity %q: %#v", oldID, loaded.Events)
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
	observedRevision := false
	for _, frame := range frames {
		if update := session.ProtocolUpdateOf(frame.Event); update != nil && update.SessionUpdate == kind && update.MessageID == oldID {
			observedRevision = update.PreviouslyObserved && update.Replacement && session.IsUIOnly(frame.Event)
		}
	}
	if !observedRevision {
		t.Fatalf("live revision lost raw identity %q: %#v", oldID, frames)
	}
	if terminal.Result == nil || terminal.Result.Result != "current answer" {
		t.Fatalf("replayed Session revision took current result: %+v", terminal.Result)
	}
}

func TestDraftDistinctOpaqueThoughtIDsCannotInvalidateCurrentResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	peer := &draftpeer.Peer{SessionID: "loaded-child"}
	peer.OnResume = func(req v2.ResumeSessionRequest) (v2.ResumeSessionResponse, error) {
		err := peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": "agent_thought", "messageId": " thought ", "content": []any{map[string]any{"type": "text", "text": "previous thought"}}})
		if err != nil {
			return v2.ResumeSessionResponse{}, err
		}
		err = peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": "agent_message", "messageId": "answer-one", "content": []any{map[string]any{"type": "text", "text": "previous answer"}}})
		return v2.ResumeSessionResponse{}, err
	}
	peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
		if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := peer.Echo(ctx, req.SessionId, message, "current input"); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": "agent_thought", "messageId": "thought", "content": []any{map[string]any{"type": "text", "text": "current thought"}}}); err != nil {
			return v2.PromptResponse{}, err
		}
		for _, frame := range []struct{ id, text string }{{"answer-two", "current answer"}, {"answer-one", "revised previous answer"}} {
			if err := peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": "agent_message", "messageId": frame.id, "content": []any{map[string]any{"type": "text", "text": frame.text}}}); err != nil {
				return v2.PromptResponse{}, err
			}
		}
		if err := peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": "agent_thought", "messageId": " thought ", "content": []any{map[string]any{"type": "text", "text": "revised previous thought"}}}); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := peer.State(ctx, req.SessionId, activity, "idle", "end_turn"); err != nil {
			return v2.PromptResponse{}, err
		}
		return v2.PromptResponse{MessageId: v2.MessageId(message), Meta: acpdraft.ActivityMeta(activity)}, nil
	}
	runner := replacementDraftRunner(t, peer)
	events := make(chan childInputTestEvent, 64)
	spawn := childInputSpawnContext(t, "loaded-task", events)
	anchor := delegation.Anchor{TaskID: spawn.TaskID, SessionID: peer.SessionID, AgentID: "loaded-agent"}
	loaded, err := runner.LoadHistory(ctx, tasksubagent.HistoryRequest{Anchor: anchor, Reconnect: tasksubagent.ReconnectRequest{Spawn: spawn, Target: delegation.AgentTarget("helper")}})
	if err != nil {
		t.Fatal(err)
	}
	oldID := " thought "
	kind := string(session.ProtocolUpdateTypeAgentThought)
	loadedIdentity := false
	for _, event := range loaded.Events {
		if update := session.ProtocolUpdateOf(event); update != nil && update.SessionUpdate == kind && update.MessageID == oldID {
			loadedIdentity = true
		}
	}
	if !loadedIdentity {
		t.Fatalf("LoadHistory lost raw identity %q: %#v", oldID, loaded.Events)
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
	observedRevision := false
	for _, frame := range frames {
		if update := session.ProtocolUpdateOf(frame.Event); update != nil && update.SessionUpdate == kind && update.MessageID == oldID {
			observedRevision = update.PreviouslyObserved && update.Replacement && session.IsUIOnly(frame.Event)
		}
	}
	if !observedRevision {
		t.Fatalf("live revision lost raw identity %q: %#v", oldID, frames)
	}
	if terminal.Result == nil || terminal.Result.Result != "current answer" {
		t.Fatalf("replayed Session revision took current result: %+v", terminal.Result)
	}
}
