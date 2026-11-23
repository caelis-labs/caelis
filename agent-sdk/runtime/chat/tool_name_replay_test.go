package chat

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestRecordedToolReplySurvivesDifferentOutputBudget(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cfg := sessionfile.Config{RootDir: t.TempDir()}
	store := sessionfile.NewStore(cfg)
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}

	call := model.ToolCall{ID: "call-1", Name: "read_record", Args: `{"key":"row-7"}`}
	assistant := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, "")
	result := textToolResult(strings.Repeat("x", tool.DefaultTruncationPolicy().ByteBudget()*2))
	reply := toolResultMessageFromCanonical(call, result, nil)
	events := append(modelToolCallEvents(assistant, nil, "", nil), toolResultEvent(call, result, nil, &reply))
	for _, event := range events {
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatal(err)
		}
	}

	reopened := sessionfile.NewStore(cfg)
	loaded, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	got := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Events: loaded.Events}))
	if !reflect.DeepEqual(got, []model.Message{assistant, reply}) {
		t.Fatal("reopened context differs from the original assistant call and tool reply")
	}
}

func TestToolNamePersistencePreservesRuntimeModelContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	user := model.NewTextMessage(model.RoleUser, "inspect")
	call := model.ToolCall{ID: "call-1", Name: "probe", Args: `{"path":"README.md"}`}
	live := []*session.Event{
		{Type: session.EventTypeUser, Message: &user},
		modelToolCallEvents(model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, ""), nil, "", nil)[0],
		toolResultEvent(call, tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: json.RawMessage(`{"value":"ok"}`)}}}, nil, nil),
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
	subject := session.CloneApprovalSubject(&session.ApprovalSubject{Kind: "file", Tool: session.KnownApprovalField("probe"), Operation: session.KnownApprovalField("read"), Targets: []session.ApprovalField{session.KnownApprovalField("README.md")}, Effect: session.KnownApprovalField("read the exact path"), Scope: session.KnownApprovalField("this invocation")})
	live = append(live, &session.Event{
		Type: session.EventTypeToolCall, Visibility: session.VisibilityMirror,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: "tool_call", ToolCallID: "remote-1", Name: &name, Title: "Display only", Kind: "other",
		}},
	})
	for _, present := range []bool{true, false} {
		live = append(live, &session.Event{
			Type: session.EventTypeLifecycle,
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodRequestPermission,
				Permission: &session.ProtocolApproval{Subject: subject, ToolCall: session.ProtocolToolCall{
					ID: "remote-1", NamePresent: new(present),
				}},
			},
		})
	}
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
	update := session.ProtocolUpdateOf(loaded.Events[len(live)-3])
	if update == nil || update.Name == nil || *update.Name != name {
		t.Fatalf("replayed display name = %#v", update)
	}
	for i, present := range []bool{true, false} {
		approval := session.ProtocolPermissionOf(loaded.Events[len(live)-2+i])
		if approval == nil || approval.ToolCall.NamePresent == nil || *approval.ToolCall.NamePresent != present {
			t.Fatalf("replayed approval = %#v, want name presence %t", approval, present)
		}
		if !reflect.DeepEqual(approval.Subject, subject) {
			t.Fatalf("approval display facts changed during model-context round trip: %+v", approval.Subject)
		}
	}
}
