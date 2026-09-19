package controller

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestACPToolNamesSurviveLiveAndPersistedReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	for i, raw := range []string{
		`{"sessionUpdate":"tool_call","toolCallId":"call-1","title":"Inspect file","name":"read_file","kind":"read","status":"in_progress"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","name":"read_document"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","name":null}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","status":"completed"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","name":""}`,
	} {
		var update client.Update
		if i == 0 {
			var call client.ToolCall
			if err := json.Unmarshal([]byte(raw), &call); err != nil {
				t.Fatal(err)
			}
			update = call
		} else {
			var patch client.ToolCallUpdate
			if err := json.Unmarshal([]byte(raw), &patch); err != nil {
				t.Fatal(err)
			}
			update = patch
		}
		canonical := normalizeACPUpdateEvent(time.Now, session.ControllerBinding{ControllerID: "remote", Label: "remote"}, "remote-session", "turn-1", update)
		if canonical == nil {
			t.Fatal("missing canonical event")
		}
		want := canonical.Protocol.Update.Name
		live := acpEnvelopeFromUpdate(client.UpdateEnvelope{SessionID: "remote-session", Update: update}, canonical, nil)
		if live == nil {
			t.Fatal("missing live envelope")
		}
		assertToolNameWire(t, *live, want)
		// Retained protocol mirrors use Session storage; live controller tool
		// updates remain transient and do not gain model-context authority.
		canonical.Visibility = session.VisibilityMirror
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: canonical}); err != nil {
			t.Fatal(err)
		}
	}
	fresh := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	loaded, err := fresh.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	wants := []*string{testStringPtr("read_file"), testStringPtr("read_document"), nil, nil, testStringPtr("")}
	if len(loaded.Events) != len(wants) {
		t.Fatalf("events = %d, want %d", len(loaded.Events), len(wants))
	}
	for i, event := range loaded.Events {
		if !reflect.DeepEqual(event.Protocol.Update.Name, wants[i]) {
			t.Fatalf("replayed name %d = %#v, want %#v", i, event.Protocol.Update.Name, wants[i])
		}
		if _, visible := session.ModelMessageOf(event); visible || session.IsMainInvocationVisibleEvent(event) {
			t.Fatal("ACP display name entered model context")
		}
		updates, err := projection.ProjectEvent(event)
		if err != nil || len(updates) != 1 {
			t.Fatalf("projection = %#v, %v", updates, err)
		}
		assertToolNameWire(t, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: active.SessionID, Update: updates[0]}, wants[i])
	}
}

func assertToolNameWire(t *testing.T, envelope eventstream.Envelope, want *string) {
	t.Helper()
	raw, err := wirev1.MarshalEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := wirev1.UnmarshalEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got *string
	switch update := decoded.Update.(type) {
	case eventstream.ToolCall:
		got = update.Name
	case eventstream.ToolCallUpdate:
		got = update.Name
	default:
		t.Fatalf("update = %T", update)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire name = %#v, want %#v; %s", got, want, raw)
	}
}

func TestTranslateApprovalRequestStandardNameWinsFallbacks(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"read_file", "Read file", "read", ""} {
		t.Run(name, func(t *testing.T) {
			request, err := translateApprovalRequest(session.Session{}, "remote", "default", client.RequestPermissionRequest{
				SessionId: "remote-1",
				ToolCall: acpsdk.ToolCallUpdate{
					ToolCallId: "call-1", Name: &name, Title: testStringPtr("Read file"), Kind: acpsdk.Ptr(acpsdk.ToolKindRead),
					RawInput: map[string]any{"name": "input_fallback"}, RawOutput: map[string]any{"name": "output_fallback"},
					Meta: map[string]json.RawMessage{"caelis": json.RawMessage(`{"runtime":{"tool":{"name":"meta_fallback"}}}`)},
				},
				Options: []acpsdk.PermissionOption{{OptionId: "allow_once", Name: "Allow", Kind: acpsdk.PermissionOptionKindAllowOnce}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if request.ToolCall.Name != name {
				t.Fatalf("name = %q, want %q", request.ToolCall.Name, name)
			}
			if request.ToolCall.NamePresent == nil || !*request.ToolCall.NamePresent {
				t.Fatal("standard name presence was lost in the controller bridge")
			}
		})
	}
}
