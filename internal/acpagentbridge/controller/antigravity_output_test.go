package controller

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestAntigravityCommandLiveAndStoredReplayPreserveResult(t *testing.T) {
	data, err := os.ReadFile("../client/testdata/antigravity-command.json")
	if err != nil {
		t.Fatal(err)
	}
	var updates []json.RawMessage
	if err := json.Unmarshal(data, &updates); err != nil {
		t.Fatal(err)
	}
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	var want []eventstream.Update
	for i, raw := range updates {
		var update client.Update
		if i == 0 {
			var call client.ToolCall
			if err := json.Unmarshal(raw, &call); err != nil {
				t.Fatal(err)
			}
			update = call
		} else {
			var patch client.ToolCallUpdate
			if err := json.Unmarshal(raw, &patch); err != nil {
				t.Fatal(err)
			}
			update = patch
		}
		canonical := normalizeACPUpdateEvent(time.Now, session.ControllerBinding{ControllerID: "antigravity", Label: "Antigravity"}, "remote", "turn-1", update)
		live := acpEnvelopeFromUpdate(client.UpdateEnvelope{SessionID: "remote", Update: update}, canonical, nil)
		if live == nil || canonical == nil {
			t.Fatal("missing result projection")
		}
		encoded, err := wirev1.MarshalEnvelope(*live)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := wirev1.UnmarshalEnvelope(encoded)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, decoded.Update)
		participant := acpEnvelopeFromUpdate(client.UpdateEnvelope{SessionID: "remote", Update: update}, canonical, &acpEnvelopeParticipantScope{binding: session.ParticipantBinding{ID: "peer", Label: "@peer"}, agent: "antigravity", turnID: "child-turn"})
		if !reflect.DeepEqual(participant.Update, live.Update) {
			t.Fatal("participant output differs from controller output")
		}
		canonical.Visibility = session.VisibilityMirror
		if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: canonical}); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != len(want) {
		t.Fatalf("replayed %d events", len(loaded.Events))
	}
	for i, event := range loaded.Events {
		if _, ok := session.ModelMessageOf(event); ok || session.IsMainInvocationVisibleEvent(event) {
			t.Fatal("display result entered model context")
		}
		replay, err := projection.ProjectEvent(event)
		if err != nil || len(replay) != 1 {
			t.Fatalf("replay failed: %#v, %v", replay, err)
		}
		gotJSON, _ := json.Marshal(replay[0])
		wantJSON, _ := json.Marshal(want[i])
		var gotValue, wantValue any
		if err := json.Unmarshal(gotJSON, &gotValue); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(wantJSON, &wantValue); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotValue, wantValue) {
			t.Fatalf("live/replay update differs:\n%s\n%s", gotJSON, wantJSON)
		}
	}
	final := want[2].(eventstream.ToolCallUpdate)
	if len(final.Content) != 1 || final.Content[0].Content.(eventstream.TextContent).Text != "AGY_OUTPUT_OK\n" {
		t.Fatalf("missing final command output: %#v", final)
	}
}
