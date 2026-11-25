package chat

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestChildOriginAndToolProjectionSurviveFileReopenAndNextRequest(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	root := t.TempDir()
	parentStore := sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "parent-293" },
	})
	parent, err := parentStore.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "owner"})
	if err != nil {
		t.Fatalf("StartSession(parent) error = %v", err)
	}
	childStore := sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "child-293" },
	})
	child, err := childStore.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "owner"})
	if err != nil {
		t.Fatalf("StartSession(child) error = %v", err)
	}

	childMessage := model.NewTextMessage(model.RoleAssistant, "child completed the delegated lookup")
	childEvent := &session.Event{
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &childMessage,
	}
	if _, err := childStore.AppendEvent(ctx, session.AppendEventRequest{SessionRef: child.SessionRef, Event: childEvent}); err != nil {
		t.Fatalf("AppendEvent(child) error = %v", err)
	}

	userMessage := model.NewTextMessage(model.RoleUser, "delegate a lookup and report the result")
	call := model.ToolCall{ID: "spawn-call-293", Name: "StartThread", Args: `{"task":"lookup"}`}
	assistantCall := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, "")
	result := tool.Result{Content: []tool.ContentBlock{
		tool.StructuredContent{Value: json.RawMessage(`{"child_session_id":"child-293","status":"started"}`)},
	}}
	resultMessage := toolbinding.ResultMessage(call, result, nil)
	nestedParent := session.EventParentTool{
		Kind:      "code_mode",
		Execution: tool.InvocationContext{SessionID: parent.SessionID, TurnID: "turn-293", ItemID: "item-inner-293"},
		CallID:    "script-call-293",
		Name:      "js_exec",
	}
	resultEvent := toolResultEvent(call, result, nil, &resultMessage)
	resultEvent.Tool.Binding = &tool.Binding{Name: "StartThread", Identity: "participants", Version: "v1"}
	resultEvent.Tool.Parent = &nestedParent
	resultEvent.Tool.ProcessingFailure = &session.ToolProcessingFailure{Phase: "projection", Name: "renderer", Error: "recorded failure"}
	resultEvent.Tool.ProposedCall = &call

	childOrigin := &session.EventChildOrigin{
		Scope:         session.EventChildScopeSubagent,
		ScopeID:       "task-293",
		TaskID:        "task-293",
		DelegationID:  "delegation-293",
		ParticipantID: "participant-293",
		ACPSessionID:  "acp-child-293",
		SourceEventID: "child-event-293",
		ParentTool: session.EventParentTool{
			Kind:      "subagent",
			Execution: tool.InvocationContext{SessionID: parent.SessionID, TurnID: "turn-293", ItemID: "item-spawn-293"},
			CallID:    call.ID,
			Name:      call.Name,
		},
	}
	childMirror := &session.Event{
		ID:          "child-mirror-293",
		Type:        session.EventTypeAssistant,
		Visibility:  session.VisibilityMirror,
		ChildOrigin: childOrigin,
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				MessageID:     "child-message-293",
				Content:       session.ProtocolTextContent(childMessage.TextContent()),
			},
		},
	}

	toolCallEvent := modelToolCallEvents(assistantCall, nil, "turn-293", nil)[0]
	nestedParentCopy := nestedParent
	toolCallEvent.Tool.Parent = &nestedParentCopy
	parentEvents := []*session.Event{
		{Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: &userMessage},
		toolCallEvent,
		resultEvent,
		childMirror,
	}
	assertToolProjectionIsolated(t, resultEvent)
	for _, event := range parentEvents {
		if _, err := parentStore.AppendEvent(ctx, session.AppendEventRequest{SessionRef: parent.SessionRef, Event: event}); err != nil {
			t.Fatalf("AppendEvent(parent, %s) error = %v", session.EventTypeOf(event), err)
		}
	}

	liveModel := &recordingModel{}
	runCloneReopenChat(t, ctx, parent, parentEvents, liveModel)

	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	loadedParent, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: parent.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatalf("LoadSession(parent) error = %v", err)
	}
	loadedChild, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: child.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession(child) error = %v", err)
	}
	if len(loadedChild.Events) != 1 || session.EventText(loadedChild.Events[0]) != childMessage.TextContent() {
		t.Fatalf("reopened child events = %#v, want its persisted assistant event", loadedChild.Events)
	}

	var loadedResult *session.Event
	var loadedMirror *session.Event
	for _, event := range loadedParent.Events {
		switch {
		case event.Tool != nil && event.Type == session.EventTypeToolResult:
			loadedResult = event
		case event.ChildOrigin != nil:
			loadedMirror = event
		}
	}
	if loadedResult == nil {
		t.Fatalf("reopened parent events = %#v, missing tool result", loadedParent.Events)
	}
	if loadedMirror == nil || !reflect.DeepEqual(*loadedMirror.ChildOrigin, *childOrigin) {
		t.Fatalf("reopened child origin = %#v, want %#v", loadedMirror, childOrigin)
	}
	assertToolProjectionIsolated(t, loadedResult)

	replayModel := &recordingModel{}
	runCloneReopenChat(t, ctx, loadedParent.Session, loadedParent.Events, replayModel)
	if !reflect.DeepEqual(liveModel.last.Messages, replayModel.last.Messages) {
		t.Fatalf("next model request changed after reopen:\nlive:   %s\nreopen: %s", canonicalMessagesJSON(t, liveModel.last.Messages), canonicalMessagesJSON(t, replayModel.last.Messages))
	}
	if got, want := replayModel.last.Messages, []model.Message{
		userMessage,
		assistantCall,
		resultMessage,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened next model request messages = %s, want %s", canonicalMessagesJSON(t, got), canonicalMessagesJSON(t, want))
	}
}

func runCloneReopenChat(t *testing.T, ctx context.Context, active session.Session, events []*session.Event, model *recordingModel) {
	t.Helper()

	chatAgent, err := New("chat", model, "")
	if err != nil {
		t.Fatalf("New(chat) error = %v", err)
	}
	for _, err := range chatAgent.Run(agent.NewContext(agent.ContextSpec{Context: ctx, Session: active, Events: events})) {
		if err != nil {
			t.Fatalf("Chat.Run() error = %v", err)
		}
	}
}

func assertToolProjectionIsolated(t *testing.T, event *session.Event) {
	t.Helper()

	if event == nil || event.Tool == nil {
		t.Fatal("tool event is nil")
	}
	projection := session.EventToolProjection(event)
	if projection == nil || projection.Result == nil || projection.Binding == nil || projection.Parent == nil || projection.ProcessingFailure == nil || projection.ProposedCall == nil {
		t.Fatalf("EventToolProjection() = %#v, want all durable pointer fields", projection)
	}

	projection.Result.Content[0] = tool.StructuredContent{Value: json.RawMessage(`{"changed":true}`)}
	projection.Binding.Name = "changed-binding"
	projection.Parent.CallID = "changed-parent"
	projection.Parent.Execution.ItemID = "changed-item"
	projection.ProcessingFailure.Name = "changed-renderer"
	projection.ProposedCall.ID = "changed-proposal"

	if got := string(event.Tool.Result.Content[0].(tool.StructuredContent).Value); got != `{"child_session_id":"child-293","status":"started"}` {
		t.Fatalf("source result content = %s, want original JSON", got)
	}
	if event.Tool.Binding.Name != "StartThread" || event.Tool.Parent.CallID != "script-call-293" || event.Tool.Parent.Execution.ItemID != "item-inner-293" || event.Tool.ProcessingFailure.Name != "renderer" || event.Tool.ProposedCall.ID != "spawn-call-293" {
		t.Fatalf("projection mutation changed source tool event: %#v", event.Tool)
	}
}
