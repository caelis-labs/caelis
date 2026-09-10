package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestRegressionSubagentOverlaySeparatesNoticesAndShowsMCPArguments(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"
	model.taskStreamIDsByCallID["spawn-1"] = "task-1"
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle, view.actor = "orbit", "orbit[codex]"
	at := time.Unix(100, 0)
	envelope := func(update eventstream.Update) eventstream.Envelope {
		return subagentMailboxEnvelope(t, "activity-1", at, update)
	}
	thought := func(text string) eventstream.Envelope {
		return envelope(eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentThought, Content: eventstream.TextContent{Type: "text", Text: text}})
	}
	first := envelope(nil)
	first.Kind, first.Notice = eventstream.KindNotice, "Automatic approval review approved: first command"
	second := first
	second.Notice = "Automatic approval review approved: second command"
	tool := eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "send-1", Kind: "other", Status: "in_progress", Title: "caelis-collaboration / SendMessage", RawInput: map[string]any{"to": "parent", "message": "Found the cause", "reply_to": "mail-1"}}
	next, _ := model.handleTaskStreamBatch(taskStreamBatchMsg{sessionID: "session-1", taskID: "task-1", token: 7, events: []eventstream.Envelope{thought("Inspecting tool results"), first, second, thought("Sending discoveries to parent"), envelope(tool)}})
	model = next.(*Model)
	view.prepareVisibleRender()
	if len(view.block.Events) != 5 || view.block.Events[0].Kind != SEReasoning || view.block.Events[1].Kind != SENotice || view.block.Events[2].Kind != SENotice || view.block.Events[3].Kind != SEReasoning || view.block.Events[4].Kind != SEToolCall {
		t.Fatalf("notice boundaries lost: %#v", view.block.Events)
	}
	plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 110, 40)), "\n")
	for _, want := range []string{first.Notice, second.Notice, "Sending discoveries to parent", "caelis-collaboration / SendMessage", "Found the cause", "parent"} {
		if strings.Count(plain, want) == 0 {
			t.Fatalf("missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(view.block.Events[0].Text, "approved") || strings.Contains(view.block.Events[3].Text, "approved") || participantTurnHasFooter(view.block) {
		t.Fatalf("notice became reasoning or completed the Turn:\n%s", plain)
	}
	t.Logf("rendered running overlay:\n%s", plain)
	// Host-normalized exact display identity enables the native message preview;
	// title and standard rawInput stay unchanged.
	tool.ToolCallID = "send-2"
	tool.Meta = map[string]any{"caelis": map[string]any{"runtime": map[string]any{"tool": map[string]any{"name": "SendMessage"}}}}
	projected := ProjectACPEventToTranscriptEvents(envelope(tool))
	if len(projected) != 1 || projected[0].ToolArgs != "@parent: Found the cause" || projected[0].ToolMessageTarget != "@parent" {
		t.Fatalf("message profile = %#v", projected)
	}
}

func TestMCPStandardArgumentsRemainExpandableAndSurviveSparseCompletion(t *testing.T) {
	message := strings.Repeat("long message ", 30)
	tool := eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "generic-1", Title: "server / SendMessage", Kind: "other", Status: "in_progress", RawInput: map[string]any{"to": "parent", "message": message, "reply_to": "mail-1"}}
	projected := ProjectACPEventToTranscriptEvents(eventstream.Envelope{Kind: eventstream.KindSessionUpdate, Update: tool})
	if len(projected) != 1 || !strings.Contains(projected[0].ToolFullArgs, message) || !strings.Contains(projected[0].ToolFullArgs, "reply_to") || projected[0].ToolArgs == tool.Title {
		t.Fatalf("generic arguments lost: %#v", projected)
	}
	block := NewParticipantTurnBlock("turn-1", "orbit")
	applyTranscriptEventToParticipantTurn(block, projected[0], participantTurnTranscriptPolicy{})
	completed := "completed"
	patch := ProjectACPEventToTranscriptEvents(eventstream.Envelope{Kind: eventstream.KindSessionUpdate, Update: eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: tool.ToolCallID, Status: &completed}})
	for _, event := range patch {
		applyTranscriptEventToParticipantTurn(block, event, participantTurnTranscriptPolicy{})
	}
	if len(block.Events) != 1 || !strings.Contains(block.Events[0].FullArgs, message) {
		t.Fatalf("sparse completion erased arguments: %#v", block.Events)
	}
}
