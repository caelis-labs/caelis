package chat

import (
	"context"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestCommandConversationReplayMatchesRuntimeProducedModelContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	user := model.NewTextMessage(model.RoleUser, "hello")
	live := []*session.Event{
		{ID: "command-message", MessageID: "command-message", Type: session.EventTypeInteraction, Visibility: session.VisibilityMirror,
			Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeUserMessage), MessageID: "command-message", Content: session.ProtocolTextContent("/status")}}},
		{Type: session.EventTypeUser, Message: &user},
	}
	for _, event := range live {
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	first := &recordingModel{}
	chat, err := New("chat", first, "")
	if err != nil {
		t.Fatal(err)
	}
	for event, err := range chat.Run(agent.NewContext(agent.ContextSpec{Context: ctx, Session: active, Events: live})) {
		if err != nil {
			t.Fatal(err)
		}
		if session.IsCanonicalHistoryEvent(event) {
			live = append(live, event)
			if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(first.last.Messages) != 1 || first.last.Messages[0].TextContent() != "hello" {
		t.Fatalf("command entered runtime request: %+v", first.last.Messages)
	}
	liveModel := &recordingModel{}
	liveChat, err := New("chat", liveModel, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range liveChat.Run(agent.NewContext(agent.ContextSpec{Context: ctx, Session: active, Events: live})) {
		if err != nil {
			t.Fatal(err)
		}
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	loaded, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	replayModel := &recordingModel{}
	replayChat, err := New("chat", replayModel, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range replayChat.Run(agent.NewContext(agent.ContextSpec{Context: ctx, Session: loaded.Session, Events: loaded.Events})) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(liveModel.last.Messages, replayModel.last.Messages) {
		t.Fatalf("recovery changed runtime-produced model context: %s / %s", canonicalMessagesJSON(t, liveModel.last.Messages), canonicalMessagesJSON(t, replayModel.last.Messages))
	}
}
