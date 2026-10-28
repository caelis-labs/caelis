package runtime

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestAsyncCompactionStartsWithNormalizedToolHistory(t *testing.T) {
	h := newAsyncHarness(t, "")
	call := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "invalid", Name: "work", Args: `{"input":`}}, "")
	result := model.NewMessage(model.RoleTool, model.NewToolResultJSONPart("invalid", "work", map[string]any{"error": "invalid arguments"}, true))
	for _, event := range []*session.Event{
		{Type: session.EventTypeToolCall, Message: &call, Tool: &session.EventTool{ID: "invalid", Name: "work", Status: "pending"}},
		{Type: session.EventTypeToolResult, Message: &result, Tool: &session.EventTool{ID: "invalid", Name: "work", Status: "error"}},
	} {
		if _, err := h.store.AppendEvent(h.ctx, session.AppendEventRequest{
			SessionRef: h.active.SessionRef, MutationGuard: session.RuntimeMutationGuard(h.ctx), Event: event,
		}); err != nil {
			t.Fatal(err)
		}
	}
	run := h.run(t)
	foreground := receiveSnapshotValue(t, h.llm.foreground)
	if len(foreground.request.Messages) != 2 {
		t.Fatalf("messages = %#v, want history and current user input without the invalid tool batch", foreground.request.Messages)
	}
	if foreground.request.Source == nil {
		t.Fatal("normalized request has no source binding")
	}
	// Speculation requires model/tool progress after the current user input.
	h.llm.responses <- asyncToolResponse("first")
	foreground = receiveSnapshotValue(t, h.llm.foreground)
	if foreground.request.Source == nil {
		t.Fatal("normalized history lost its source binding after tool progress")
	}
	background := receiveSnapshotValue(t, h.llm.background)
	want := model.CloneRequest(foreground.request)
	want.Messages = append(want.Messages, model.NewTextMessage(model.RoleUser, inContextCompactionPrompt))
	if !reflect.DeepEqual(background, want) {
		t.Fatal("candidate did not reuse the normalized foreground request")
	}
	h.completeCandidate(t, foreground.scope)
	h.llm.responses <- asyncTextResponse("done", 500)
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
}
