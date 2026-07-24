package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestCrossVersionReplayCorpusMatchesRuntimeProducedModelContext(t *testing.T) {
	t.Parallel()

	liveUser := model.NewTextMessage(model.RoleUser, "inspect")
	liveAssistantCall := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "call-1", Name: "probe", Args: `{"path":"README.md"}`}}, "")
	liveEvents := []*session.Event{
		{Schema: session.EventSchemaVersion, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: &liveUser},
		modelToolCallEvents(liveAssistantCall, nil, "")[0],
		toolResultEvent(model.ToolCall{ID: "call-1", Name: "probe", Args: `{"path":"README.md"}`}, tool.Result{
			ID: "call-1", Name: "probe", Content: []model.Part{model.NewJSONPart(json.RawMessage(`{"value":"ok"}`))},
		}, nil),
		modelResponseEvent(model.NewTextMessage(model.RoleAssistant, "done"), nil, ""),
	}
	for _, event := range liveEvents {
		event.Schema = session.EventSchemaVersion
	}
	want := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: context.Background(), Events: liveEvents}))

	for _, version := range []string{"v0", "v1"} {
		version := version
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			events := loadReplayCorpus(t, filepath.Join("testdata", "replay", version+".json"))
			got := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: context.Background(), Events: events}))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("rebuilt model context = %s, want runtime-produced %s", canonicalMessagesJSON(t, got), canonicalMessagesJSON(t, want))
			}
		})
	}
}

func loadReplayCorpus(t *testing.T, path string) []*session.Event {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var encoded []session.Event
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	out := make([]*session.Event, 0, len(encoded))
	for _, event := range encoded {
		migrated, err := session.MigrateEvent(event)
		if err != nil {
			t.Fatalf("MigrateEvent() error = %v", err)
		}
		if err := session.ValidateDurableCoreEvent(&migrated); err != nil {
			t.Fatalf("ValidateDurableCoreEvent() error = %v", err)
		}
		out = append(out, &migrated)
	}
	return out
}
