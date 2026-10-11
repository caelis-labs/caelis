package chat

import (
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestAgentCommunicationDisplayRepairKeepsRuntimeAndReplayInput(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	var produced []*session.Event
	var producedMessages []model.Message
	accepted, err := (&Agent{}).drainPendingSubmissions(agent.NewContext(agent.ContextSpec{Context: ctx, DrainSubmissions: func() []agent.Submission {
		return []agent.Submission{{Kind: agent.SubmissionKindAgentCommunication, Text: "admitted message", Actor: session.ParentCommunicationActor()}}
	}}), &producedMessages, func(event *session.Event) bool {
		produced = append(produced, session.CloneEvent(event))
		return true
	})
	if err != nil || !accepted || len(produced) != 1 {
		t.Fatalf("native mail production failed: %v %#v", err, produced)
	}
	admitted := produced[0]
	admitted.Protocol.Update.MessageID = "mail"
	live := []*session.Event{admitted}
	for _, text := range []string{"edited display", ""} {
		observed := session.CloneEvent(admitted)
		observed.Visibility, observed.Text = session.VisibilityMirror, text
		body := model.NewTextMessage(model.RoleUser, text)
		observed.Message = &body
		observed.Protocol.Update.Replacement = true
		observed.Protocol.Update.Content = map[string]any{"type": "text", "text": text}
		live = append(live, observed)
	}
	for _, event := range live {
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	invoke := func(events []*session.Event) []model.Message {
		t.Helper()
		provider := &recordingModel{}
		chat, err := New("chat", provider, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, err := range chat.Run(agent.NewContext(agent.ContextSpec{Context: ctx, Session: active, Events: events})) {
			if err != nil {
				t.Fatal(err)
			}
		}
		return provider.last.Messages
	}
	actual := invoke(live)
	loaded, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	replayed := invoke(loaded.Events)
	if len(actual) != 1 || !strings.Contains(actual[0].TextContent(), "admitted message") || strings.Contains(actual[0].TextContent(), "edited display") || !reflect.DeepEqual(actual, replayed) || !reflect.DeepEqual(actual, producedMessages) {
		t.Fatalf("observed repair changed runtime/replay input: %#v / %#v", actual, replayed)
	}
	page, err := store.EventsPage(ctx, session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay})
	if err != nil || len(page.Events) != 3 {
		t.Fatalf("observed mail repair missing from replay: %v %#v", err, page)
	}
	for i, want := range []string{"admitted message", "edited display", ""} {
		communication := session.ProtocolAgentCommunicationOf(page.Events[i])
		if communication == nil || communication.Text != want || session.EventMessageID(page.Events[i]) != "mail" || page.Events[i].Actor != admitted.Actor {
			t.Fatalf("replayed message patch lost body/identity/source: %#v", page.Events[i])
		}
	}
}
