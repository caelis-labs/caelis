package subagent

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestChildReplacementClearAndReloadIdentity(t *testing.T) {
	var run childRun
	for _, f := range []struct {
		text    string
		replace bool
	}{{"stale", false}, {"snapshot", true}, {"", true}, {"current", false}} {
		text, id := run.appendAgentMessageChunkLocked("message", f.text, f.replace)
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
	run.appendAgentMessageChunkLocked("old", "progress", true)
	run.clearFinalAssistantLocked()
	run.appendAgentMessageChunkLocked("current", "final answer", true)
	for _, frame := range []struct {
		text    string
		replace bool
	}{{" late delta", false}, {"repair", true}, {" suffix", false}, {"", true}} {
		delta, id := run.appendAgentMessageChunkLocked("old", frame.text, frame.replace)
		if delta != frame.text || id != "old" || run.agentText != "final answer" || run.result != "final answer" {
			t.Fatalf("old repair stole task result: delta=%q id=%q result=%q", delta, id, run.result)
		}
	}
	run.finalAssistant.Reset()
	run.appendAgentMessageChunkLocked("old", "new producer turn", true)
	if run.result != "new producer turn" {
		t.Fatalf("Turn reset retained old introduction: %q", run.result)
	}
}

func TestChildLatestIdentifiedMessageResumesTaskResultAfterTool(t *testing.T) {
	for _, test := range []struct {
		name, text, want string
		replace          bool
	}{{"delta", " suffix", "progress suffix", false}, {"snapshot", "final answer", "final answer", true}} {
		t.Run(test.name, func(t *testing.T) {
			var run childRun
			run.appendAgentMessageChunkLocked("old", "old progress", true)
			run.appendAgentMessageChunkLocked("latest", "progress", true)
			run.clearFinalAssistantLocked()
			run.appendAgentMessageChunkLocked("old", " late delta", false)
			if run.result != "" {
				t.Fatalf("older repair restored task result: %q", run.result)
			}
			delta, id := run.appendAgentMessageChunkLocked("latest", test.text, test.replace)
			if delta != test.text || id != "latest" || run.agentText != test.want || run.result != test.want {
				t.Fatalf("latest post-tool task result lost: delta=%q id=%q result=%q", delta, id, run.result)
			}
		})
	}
}

func TestChildMetadataOnlyNarrativePatchDoesNotChangeTaskResult(t *testing.T) {
	run := &childRun{running: true}
	runner := &Runner{clock: time.Now}
	run.appendAgentMessageChunkLocked("latest", "progress", true)
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
	run.appendAgentMessageChunkLocked("latest", " suffix", false)
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
