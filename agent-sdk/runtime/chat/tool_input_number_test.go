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

func TestToolObservationInputNumbersSurviveSessionFileReplay(t *testing.T) {
	t.Parallel()
	const decimal = "9007199254740991.1"
	const integer = "900719925474099312345678901234567890"
	const exponent = "1e1000"
	call := model.ToolCall{
		ID: "call-1", Name: "probe",
		Args: `{"decimal":9007199254740991.1,"exponent":1e1000,"integer":900719925474099312345678901234567890,"nested":{"value":9007199254740991.1}}`,
	}
	assertInputs := func(t *testing.T, events []*session.Event) {
		t.Helper()
		for _, event := range events {
			if event.Tool == nil {
				t.Fatalf("%s has no tool payload", event.Type)
			}
			input := event.Tool.Input
			for key, want := range map[string]string{"decimal": decimal, "integer": integer, "exponent": exponent} {
				if got, ok := input[key].(json.Number); !ok || string(got) != want {
					t.Fatalf("%s input[%q] = %#v, want exact JSON number %s", event.Type, key, input[key], want)
				}
			}
			nested, ok := input["nested"].(map[string]any)
			if !ok || nested["value"] != json.Number(decimal) {
				t.Fatalf("%s nested input = %#v, want exact JSON number %s", event.Type, input["nested"], decimal)
			}
			encoded, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(encoded); got != call.Args {
				t.Fatalf("%s serialized input = %s, want %s", event.Type, got, call.Args)
			}
		}
	}

	live := []*session.Event{
		modelToolCallEvents(model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, ""), nil, "", nil)[0],
		toolResultEvent(call, tool.Result{ID: call.ID, Name: call.Name, Content: []model.Part{model.NewJSONPart(json.RawMessage(`{"result":"ok"}`))}}, nil),
	}
	assertInputs(t, live)
	ctx := context.Background()
	wantContext := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Events: live}))
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range live {
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	loaded, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != len(live) {
		t.Fatalf("replayed events = %d, want %d", len(loaded.Events), len(live))
	}
	assertInputs(t, loaded.Events)
	gotContext := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Events: loaded.Events}))
	if !reflect.DeepEqual(gotContext, wantContext) {
		t.Fatalf("rebuilt model context = %s, want runtime-produced %s", canonicalMessagesJSON(t, gotContext), canonicalMessagesJSON(t, wantContext))
	}
}

func TestMustObjectRejectsTrailingJSON(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"n":1} {"n":2}`, `{"n":1} garbage`, `{"n":1} 2`} {
		if got := mustObject(raw); got != nil {
			t.Fatalf("mustObject(%q) = %#v, want nil", raw, got)
		}
	}
}
