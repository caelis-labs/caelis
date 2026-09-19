package acpagentbridge

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/control/acppermission"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
)

func TestPermissionToolNameSurvivesLiveAndPersistedWire(t *testing.T) {
	t.Parallel()
	for _, field := range []string{``, `,"name":null`, `,"name":""`, `,"name":"read_file"`} {
		t.Run(field, func(t *testing.T) {
			var incoming eventstream.RequestPermissionRequest
			raw := `{"sessionId":"session-1","toolCall":{"toolCallId":"call-1","title":"Read file","kind":"read"` + field + `},"options":[{"optionId":"allow_once","name":"Allow","kind":"allow_once"}]}`
			if err := json.Unmarshal([]byte(raw), &incoming); err != nil {
				t.Fatal(err)
			}
			approval, err := acppermission.DecodePermissionRequest(incoming)
			if err != nil {
				t.Fatal(err)
			}
			live, err := sdkPermissionRequestFromApproval(session.SessionRef{SessionID: "session-1"}, approval, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertPermissionWireName(t, live, incoming.ToolCall.Name)

			root := t.TempDir()
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
				Type:     session.EventTypeLifecycle,
				Protocol: &session.EventProtocol{Method: session.ProtocolMethodRequestPermission, Permission: approval},
			}})
			if err != nil {
				t.Fatal(err)
			}
			fresh := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			loaded, err := fresh.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Events) != 1 {
				t.Fatalf("loaded events = %d, want 1", len(loaded.Events))
			}
			envelopes := projection.ProjectSessionEventEnvelope(eventstream.Envelope{SessionID: active.SessionID}, loaded.Events[0])
			if len(envelopes) != 1 || envelopes[0].Permission == nil {
				t.Fatalf("replayed permission = %#v", envelopes)
			}
			assertPermissionWireName(t, envelopes[0].Permission, incoming.ToolCall.Name)
		})
	}
}

func assertPermissionWireName(t *testing.T, request any, want *string) {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ToolCall map[string]json.RawMessage `json:"toolCall"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	name, present := decoded.ToolCall["name"]
	if want == nil {
		if present {
			t.Fatalf("absent/null name became a standard update: %s", raw)
		}
	} else if expected, _ := json.Marshal(*want); !present || string(name) != string(expected) {
		t.Fatalf("name = %s (present %t), want %s: %s", name, present, expected, raw)
	}
}
