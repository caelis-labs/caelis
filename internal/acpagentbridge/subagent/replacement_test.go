package subagent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpdraft"
	"github.com/caelis-labs/caelis/internal/acptest/draftpeer"
)

func TestChildReplacementClearAndReloadIdentity(t *testing.T) {
	var run childRun
	for _, f := range []struct {
		text    string
		replace bool
	}{{"stale", false}, {"snapshot", true}, {"", true}, {"current", false}} {
		text, id := run.appendAgentMessageChunkLocked(client.ContentChunk{MessageID: "message", Replacement: f.replace}, f.text)
		if text != f.text || id != "message" {
			t.Fatalf("child frame = %q %q", text, id)
		}
	}
	if run.finalAssistant.FinalText() != "current" {
		t.Fatalf("child final = %q", run.finalAssistant.FinalText())
	}
	c := newHistoryCollector(&Runner{clock: time.Now}, delegation.Anchor{SessionID: "child", TaskID: "task"}, "helper")
	c.openProjection()
	defer c.closeProjection()
	observe := func(kind, id, text string, replace bool) {
		t.Helper()
		raw, _ := json.Marshal(client.TextContent{Type: "text", Text: text})
		c.observe(client.UpdateEnvelope{SessionID: "child", Update: client.ContentChunk{SessionUpdate: kind, MessageID: id, Content: raw, Replacement: replace}})
	}
	observe(client.UpdateUserMessage, "input-one", "old input", true)
	observe(client.UpdateAgentMessage, "answer-one", "old answer", true)
	observe(client.UpdateUserMessage, "input-two", "second input", true)
	observe(client.UpdateAgentMessage, "answer-two", "second answer", true)
	observe(client.UpdateUserMessage, "input-one", "", true)
	observe(client.UpdateAgentMessage, "answer-one", "new answer", true)
	var inputTurns, answerTurns []string
	err := c.streamEvents(t.Context(), func(e *session.Event) error {
		u := session.ProtocolUpdateOf(e)
		if u.MessageID == "input-one" {
			inputTurns = append(inputTurns, e.Scope.TurnID)
		}
		if u.MessageID == "answer-one" {
			answerTurns = append(answerTurns, e.Scope.TurnID)
		}
		return nil
	})
	if err != nil || len(inputTurns) != 2 || inputTurns[0] != inputTurns[1] || len(answerTurns) != 2 || answerTurns[0] != answerTurns[1] {
		t.Fatalf("message identity changed replay owner: %v %#v %#v", err, inputTurns, answerTurns)
	}
}

func TestChildOldMessageRepairDoesNotReplaceCurrentTaskResult(t *testing.T) {
	var run childRun
	run.appendAgentMessageChunkLocked(client.ContentChunk{MessageID: "old", Replacement: true}, "progress")
	run.clearFinalAssistantLocked()
	run.appendAgentMessageChunkLocked(client.ContentChunk{MessageID: "current", Replacement: true}, "final answer")
	for _, frame := range []struct {
		text    string
		replace bool
	}{{" late delta", false}, {"repair", true}, {" suffix", false}, {"", true}} {
		delta, id := run.appendAgentMessageChunkLocked(client.ContentChunk{MessageID: "old", Replacement: frame.replace}, frame.text)
		if delta != frame.text || id != "old" || run.agentText != "final answer" || run.result != "final answer" {
			t.Fatalf("old repair stole task result: delta=%q id=%q result=%q", delta, id, run.result)
		}
	}
	run.finalAssistant.Reset()
	run.appendAgentMessageChunkLocked(client.ContentChunk{MessageID: "old", Replacement: true}, "new producer Session")
	if run.result != "new producer Session" {
		t.Fatalf("Session reset retained old introduction: %q", run.result)
	}
}

func TestDraftChildPreviousActivityRevisionKeepsCurrentResult(t *testing.T) {
	for _, test := range []struct {
		name          string
		before, after bool
		current       bool
	}{
		{name: "before new answer", before: true, current: true},
		{name: "after new answer", after: true, current: true},
		{name: "before and after new answer", before: true, after: true, current: true},
		{name: "previous revision without new answer", before: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			peer := &draftpeer.Peer{SessionID: "child-remote"}
			continueAnswer := make(chan struct{})
			peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
				if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
					return v2.PromptResponse{}, err
				}
				if err := peer.Echo(ctx, req.SessionId, message, "input"); err != nil {
					return v2.PromptResponse{}, err
				}
				emit := func(id, text string) error {
					return peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": "agent_message", "messageId": id, "content": []any{map[string]any{"type": "text", "text": text}}})
				}
				if peer.Prompts.Load() == 1 {
					if err := emit("answer-one", "first activity result"); err != nil {
						return v2.PromptResponse{}, err
					}
				} else {
					if test.before {
						if err := emit("answer-one", "revised first before current"); err != nil {
							return v2.PromptResponse{}, err
						}
						select {
						case <-continueAnswer:
						case <-ctx.Done():
							return v2.PromptResponse{}, ctx.Err()
						}
					}
					if test.current {
						if err := emit("answer-two", "second activity result"); err != nil {
							return v2.PromptResponse{}, err
						}
					}
					if test.after {
						if err := emit("answer-one", "revised first after current"); err != nil {
							return v2.PromptResponse{}, err
						}
					}
				}
				if err := peer.State(ctx, req.SessionId, activity, "idle", "end_turn"); err != nil {
					return v2.PromptResponse{}, err
				}
				return v2.PromptResponse{MessageId: v2.MessageId(message), Meta: acpdraft.ActivityMeta(activity)}, nil
			}
			runner := replacementDraftRunner(t, peer)
			events := make(chan childInputTestEvent, 64)
			spawn := childInputSpawnContext(t, "original-task", events)
			anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "first input"})
			if err != nil {
				t.Fatal(err)
			}
			first := waitChildActivityTerminalFor(t, ctx, events, spawn.ActivityID)
			if first.Result == nil || first.Result.Result != "first activity result" {
				t.Fatalf("first result = %+v", first)
			}
			run, err := runner.lookup(anchor)
			if err != nil {
				t.Fatal(err)
			}
			started, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller"}, Input: "second input"})
			if err != nil || !started.StartedActivity || started.ActivityID == spawn.ActivityID {
				t.Fatalf("new Activity admission = %+v, %v", started, err)
			}
			observedRevisions := 0
			if test.before {
				for observedRevisions == 0 {
					select {
					case event := <-events:
						if event.Frame == nil || event.Frame.Event == nil || event.Frame.Event.Text != "revised first before current" {
							continue
						}
						update := session.ProtocolUpdateOf(event.Frame.Event)
						if update == nil || update.MessageID != "answer-one" || !update.Replacement || !session.IsUIOnly(event.Frame.Event) {
							t.Fatalf("old revision lost its display identity: %+v", event)
						}
						observedRevisions++
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				run.mu.RLock()
				premature := run.result
				run.mu.RUnlock()
				if premature != "" {
					t.Fatalf("old revision became new Activity answer before new content: %q", premature)
				}
				close(continueAnswer)
			}
			frames, second := waitChildActivityFramesUntilTerminalFor(t, ctx, events, started.ActivityID)
			for _, frame := range frames {
				if frame.Event == nil || frame.Event.Text != "revised first after current" {
					continue
				}
				update := session.ProtocolUpdateOf(frame.Event)
				if update == nil || update.MessageID != "answer-one" || !update.Replacement || !session.IsUIOnly(frame.Event) {
					t.Fatalf("old revision lost its display identity: %+v", frame)
				}
				observedRevisions++
			}
			want := ""
			if test.current {
				want = "second activity result"
			}
			wantRevisions := 0
			if test.before {
				wantRevisions++
			}
			if test.after {
				wantRevisions++
			}
			if second.Result == nil || second.Result.State != delegation.StateCompleted || second.Result.TaskID != anchor.TaskID || second.Result.Result != want || observedRevisions != wantRevisions {
				t.Fatalf("old message revision replaced current Activity or vanished: result=%+v revisions=%d", second.Result, observedRevisions)
			}
		})
	}
}

func TestDraftChildNewSessionCanReuseMessageIdentity(t *testing.T) {
	for _, remote := range []string{"first-session", "new-session"} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		peer := &draftpeer.Peer{SessionID: remote}
		peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
			if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
				return v2.PromptResponse{}, err
			}
			if err := peer.Echo(ctx, req.SessionId, message, "input"); err != nil {
				return v2.PromptResponse{}, err
			}
			if err := peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": "agent_message", "messageId": "reused-answer", "content": []any{map[string]any{"type": "text", "text": remote + " answer"}}}); err != nil {
				return v2.PromptResponse{}, err
			}
			if err := peer.State(ctx, req.SessionId, activity, "idle", "end_turn"); err != nil {
				return v2.PromptResponse{}, err
			}
			return v2.PromptResponse{MessageId: v2.MessageId(message), Meta: acpdraft.ActivityMeta(activity)}, nil
		}
		runner := replacementDraftRunner(t, peer)
		events := make(chan childInputTestEvent, 64)
		spawn := childInputSpawnContext(t, "task-"+remote, events)
		anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "input"})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		terminal := waitChildActivityTerminalFor(t, ctx, events, spawn.ActivityID)
		cancel()
		if terminal.Result == nil || terminal.Result.Result != remote+" answer" || anchor.SessionID != remote {
			t.Fatalf("new Session reused identity incorrectly: anchor=%+v result=%+v", anchor, terminal.Result)
		}
	}
}

func replacementDraftRunner(t *testing.T, peer *draftpeer.Peer) *Runner {
	t.Helper()
	registry, err := NewRegistry([]AgentConfig{{Name: "helper", HostedAdapterID: "fixture", ACPDraftSchema: acpdraft.SchemaTag}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry, EndpointResolver: peer})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Quiesce(context.Background()) })
	return runner
}

func TestDraftChildPreviousThoughtRevisionKeepsCurrentResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	peer := &draftpeer.Peer{SessionID: "thought-revisions"}
	peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
		if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := peer.Echo(ctx, req.SessionId, message, "input"); err != nil {
			return v2.PromptResponse{}, err
		}
		frames := []struct{ kind, id, text string }{{"agent_thought", "shared-id", "first thought"}, {"agent_message", "answer-one", "first answer"}}
		if peer.Prompts.Load() == 2 {
			// Message kinds have separate identity namespaces. This assistant ID
			// must not admit the old thought as a current-Activity barrier.
			frames = []struct{ kind, id, text string }{{"agent_message", "shared-id", "second answer"}, {"agent_thought", "shared-id", "revised first thought"}}
		}
		for _, frame := range frames {
			if err := peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": frame.kind, "messageId": frame.id, "content": []any{map[string]any{"type": "text", "text": frame.text}}}); err != nil {
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
	spawn := childInputSpawnContext(t, "thought-task", events)
	anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "first input"})
	if err != nil {
		t.Fatal(err)
	}
	first := waitChildActivityTerminalFor(t, ctx, events, spawn.ActivityID)
	if first.Result == nil || first.Result.Result != "first answer" {
		t.Fatalf("first result = %+v", first.Result)
	}
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	started, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{Target: run.slot.target, Source: session.ParentCommunicationActor(), Input: "second input"})
	if err != nil {
		t.Fatal(err)
	}
	frames, second := waitChildActivityFramesUntilTerminalFor(t, ctx, events, started.ActivityID)
	observedRevision := false
	for _, frame := range frames {
		if frame.Event == nil || frame.Event.Text != "revised first thought" {
			continue
		}
		update := session.ProtocolUpdateOf(frame.Event)
		observedRevision = update != nil && update.MessageID == "shared-id" && update.PreviouslyObserved && update.Replacement && session.IsUIOnly(frame.Event)
	}
	if second.Result == nil || second.Result.Result != "second answer" || !observedRevision {
		t.Fatalf("old thought revision cleared current answer or display: result=%+v observed=%t", second.Result, observedRevision)
	}
}

func TestChildLatestIdentifiedMessageResumesTaskResultAfterTool(t *testing.T) {
	for _, test := range []struct {
		name, text, want string
		replace          bool
	}{{"delta", " suffix", "progress suffix", false}, {"snapshot", "final answer", "final answer", true}} {
		t.Run(test.name, func(t *testing.T) {
			var run childRun
			run.appendAgentMessageChunkLocked(client.ContentChunk{MessageID: "old", Replacement: true}, "old progress")
			run.appendAgentMessageChunkLocked(client.ContentChunk{MessageID: "latest", Replacement: true}, "progress")
			run.clearFinalAssistantLocked()
			run.appendAgentMessageChunkLocked(client.ContentChunk{MessageID: "old", Replacement: false}, " late delta")
			if run.result != "" {
				t.Fatalf("older repair restored task result: %q", run.result)
			}
			delta, id := run.appendAgentMessageChunkLocked(client.ContentChunk{MessageID: "latest", Replacement: test.replace}, test.text)
			if delta != test.text || id != "latest" || run.agentText != test.want || run.result != test.want {
				t.Fatalf("latest post-tool task result lost: delta=%q id=%q result=%q", delta, id, run.result)
			}
		})
	}
}

func TestChildMetadataOnlyNarrativePatchDoesNotChangeTaskResult(t *testing.T) {
	run := &childRun{running: true}
	runner := &Runner{clock: time.Now}
	run.appendAgentMessageChunkLocked(client.ContentChunk{MessageID: "latest", Replacement: true}, "progress")
	patch := client.ContentChunk{SessionUpdate: client.UpdateAgentThought, MessageID: "thought", Content: json.RawMessage(`{"type":"text","text":""}`), MetaPresent: true}
	runner.handleUpdate(run, client.UpdateEnvelope{Update: patch})
	if run.result != "progress" {
		t.Fatalf("metadata-only thought changed task result: %q", run.result)
	}
	run.clearFinalAssistantLocked()
	patch.SessionUpdate, patch.MessageID = client.UpdateAgentMessage, "latest"
	runner.handleUpdate(run, client.UpdateEnvelope{Update: patch})
	if run.result != "" {
		t.Fatalf("metadata-only latest message restored task result: %q", run.result)
	}
	event := run.acpUpdateEvent(client.UpdateEnvelope{Update: patch}, time.Now())
	if event == nil || !slices.Contains(event.Protocol.Update.ReplacedFields, "_meta") || event.Protocol.Update.Meta != nil {
		t.Fatalf("child metadata clear was dropped: %#v", event)
	}
	run.appendAgentMessageChunkLocked(client.ContentChunk{MessageID: "latest", Replacement: false}, " suffix")
	if run.result != "progress suffix" {
		t.Fatalf("metadata clear erased latest retained body: %q", run.result)
	}
}

func TestChildParentCommunicationPreservesObservedMessagePatch(t *testing.T) {
	run := &childRun{}
	for _, source := range []session.ActorRef{session.ParentCommunicationActor(), {Kind: session.ActorKindSystem, Name: "coordinator"}} {
		for _, replacement := range []bool{false, true} {
			patch := client.ContentChunk{SessionUpdate: client.UpdateUserMessage, MessageID: "mail", Content: json.RawMessage(`{"type":"text","text":""}`), Replacement: replacement, MetaPresent: true}
			event := run.acpUpdateEvent(client.UpdateEnvelope{Update: patch}, time.Now())
			markSubagentInputEvent(event, source)
			if event == nil || event.Actor != source || event.Type != session.EventTypeContext || !session.IsUIOnly(event) || event.Protocol.Update.MessageID != "mail" || event.Protocol.Update.Replacement != replacement || !slices.Contains(event.Protocol.Update.ReplacedFields, "_meta") {
				t.Fatalf("observed source or message patch lost: %#v", event)
			}
			communication := session.ProtocolAgentCommunicationOf(event)
			if communication == nil || communication.Text != "" || session.IsInvocationVisibleEvent(event) {
				t.Fatalf("empty observed mail lost or became model input: %#v", event)
			}
		}
	}
}

func TestChildTrustedCommunicationReplacementKeepsDisplayBody(t *testing.T) {
	source := session.ActorRef{Kind: session.ActorKindParticipant, ID: "reviewer", Role: "delegated", Name: "reviewer"}
	sink := &recordingStreams{}
	run := &childRun{
		anchor: delegation.Anchor{TaskID: "task", SessionID: "child", AgentID: "child-agent"},
		taskID: "task", state: delegation.StateRunning, running: true, output: sink, inputActor: source,
	}
	runner := &Runner{clock: time.Now}
	for _, body := range []string{" revised body ", ""} {
		text := body
		if body != "" {
			text += session.AgentCommunicationPromptFooter(source)
		}
		raw, _ := json.Marshal(client.TextContent{Type: "text", Text: text})
		handleTestUpdate(runner, run, client.UpdateEnvelope{Update: client.ContentChunk{SessionUpdate: client.UpdateUserMessage, MessageID: "mail", Content: raw, Replacement: true}})
	}
	if len(sink.frames) != 2 {
		t.Fatalf("observed communication frames = %#v", sink.frames)
	}
	for index, frame := range sink.frames {
		communication := session.ProtocolAgentCommunicationOf(frame.Event)
		want := " revised body "
		if index == 1 {
			want = ""
		}
		if communication == nil || communication.Text != want || frame.Event.Actor != source || !frame.Event.Protocol.Update.Replacement || session.IsInvocationVisibleEvent(frame.Event) {
			t.Fatalf("trusted communication display body lost: communication=%#v event=%#v", communication, frame.Event)
		}
	}
}
