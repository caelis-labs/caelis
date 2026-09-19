package chat

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestToolNamePersistencePreservesRuntimeModelContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	user := model.NewTextMessage(model.RoleUser, "inspect")
	call := model.ToolCall{ID: "call-1", Name: "probe", Args: `{"path":"README.md"}`}
	live := []*session.Event{
		{Type: session.EventTypeUser, Message: &user},
		modelToolCallEvents(model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, ""), nil, "", nil)[0],
		toolResultEvent(call, tool.Result{ID: call.ID, Name: call.Name, Content: []model.Part{model.NewJSONPart(json.RawMessage(`{"value":"ok"}`))}}, nil),
		modelResponseEvent(model.NewTextMessage(model.RoleAssistant, "done"), nil, "", nil),
	}
	want := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Events: live}))
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	name := "external_display_tool"
	live = append(live, &session.Event{
		Type: session.EventTypeToolCall, Visibility: session.VisibilityMirror,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: "tool_call", ToolCallID: "remote-1", Name: &name, Title: "Display only", Kind: "other",
		}},
	})
	for _, event := range live {
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	fresh := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	loaded, err := fresh.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	got := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Events: loaded.Events}))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rebuilt context = %s, want %s", canonicalMessagesJSON(t, got), canonicalMessagesJSON(t, want))
	}
	update := session.ProtocolUpdateOf(loaded.Events[len(loaded.Events)-1])
	if update == nil || update.Name == nil || *update.Name != name {
		t.Fatalf("replayed display name = %#v", update)
	}
}
