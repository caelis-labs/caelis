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
)

func TestNativeStructuredDiffModelContextSurvivesFileStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	call := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "edit", Name: "Edit", Args: `{}`}}, "")
	changes := []session.ProtocolDiffChange{{Operation: "modify", Path: " exact file.txt "}}
	patch := &session.ProtocolDiffPatch{Format: "git_patch", Text: "-old\r\n+new\r\n"}
	diff := session.EventToolContent{Type: "diff", Changes: &changes, Patch: patch, Meta: map[string]any{"display": "exact"}}
	native := &session.Event{Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: "edit", Name: "Edit", Status: "completed", Content: []session.EventToolContent{diff}}}
	mirror := &session.Event{Type: session.EventTypeToolResult, Visibility: session.VisibilityMirror, Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{SessionUpdate: "tool_call_update", ToolCallID: "remote", Content: []session.ProtocolToolCallContent{{Type: "diff", Changes: &changes, Patch: &session.ProtocolDiffPatch{Format: "git_patch", Text: "UNTRUSTED-MIRROR"}}}}}}
	live := []*session.Event{modelToolCallEvents(call, nil, "", nil)[0], native, mirror}
	for _, event := range live {
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	want := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Session: active, Events: live}))
	loaded, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	got := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Session: loaded.Session, Events: loaded.Events}))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native recovery context changed: %s / %s", canonicalMessagesJSON(t, got), canonicalMessagesJSON(t, want))
	}
	if len(got) != 2 || len(got[1].ToolResults()) != 1 {
		t.Fatalf("mirror entered model context: %s", canonicalMessagesJSON(t, got))
	}
	var payload struct {
		Content []session.EventToolContent `json:"content"`
	}
	if err := json.Unmarshal(got[1].ToolResults()[0].Content[0].JSON.Value, &payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload.Content, []session.EventToolContent{diff}) {
		t.Fatalf("native structured result lost: %#v", payload.Content)
	}
	native.Tool.Output = map[string]any{"result": "authoritative native output"}
	if got := toolResultContextPayload(native.Tool); !reflect.DeepEqual(got, native.Tool.Output) {
		t.Fatalf("display diff displaced native output: %#v", got)
	}
}
