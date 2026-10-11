package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

// Retained protocol mirrors exercise the same normalization, physical Session
// JSON and Control replay projection as live provider display observations.
func TestOpaqueProtocolIDsSurviveLivePhysicalStoreAndReplay(t *testing.T) {
	for _, kind := range []string{client.UpdateAgentMessage, client.UpdateAgentThought, client.UpdateToolCallState} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "owner"})
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{" identity ", "identity", " "}
			for _, id := range ids {
				var update client.Update = client.ContentChunk{SessionUpdate: kind, MessageID: id, Content: json.RawMessage(`{"type":"text","text":"display"}`), Replacement: true}
				if kind == client.UpdateToolCallState {
					update = client.ToolCallUpdate{SessionUpdate: kind, ToolCallID: id, Status: testStringPtr("completed")}
				}
				canonical := normalizeACPUpdateEvent(time.Now, session.ControllerBinding{ControllerID: "remote", Label: "remote"}, "remote-session", "turn", update)
				if canonical == nil {
					t.Fatal("missing normalized observation")
				}
				live := acpEnvelopeFromUpdate(client.UpdateEnvelope{SessionID: "remote-session", Update: update}, canonical, nil)
				if live == nil {
					t.Fatal("missing live projection")
				}
				assertOpaqueProjectionID(t, *live, id)
				canonical.Visibility = session.VisibilityMirror
				if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: canonical}); err != nil {
					t.Fatal(err)
				}
			}
			loaded, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Events) != len(ids) {
				t.Fatalf("distinct physical observations = %d, want %d", len(loaded.Events), len(ids))
			}
			for i, event := range loaded.Events {
				p := session.ProtocolUpdateOf(event)
				got := session.EventMessageID(event)
				if kind == client.UpdateToolCallState {
					got = p.ToolCallID
				}
				if got != ids[i] || session.IsMainInvocationVisibleEvent(event) {
					t.Fatalf("physical identity/model authority changed: got %q, want %q; %#v", got, ids[i], event)
				}
				updates, err := projection.ProjectEvent(event)
				if err != nil || len(updates) != 1 {
					t.Fatalf("replay projection = %#v, %v", updates, err)
				}
				assertOpaqueProjectionID(t, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: active.SessionID, Update: updates[0]}, ids[i])
			}
		})
	}
}

func assertOpaqueProjectionID(t *testing.T, envelope eventstream.Envelope, want string) {
	t.Helper()
	raw, err := wirev1.MarshalEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := wirev1.UnmarshalEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	switch update := decoded.Update.(type) {
	case eventstream.ContentChunk:
		got = update.MessageID
	case eventstream.ToolCall:
		got = update.ToolCallID
	case eventstream.ToolCallUpdate:
		got = update.ToolCallID
	default:
		t.Fatalf("projection = %T", decoded.Update)
	}
	if got != want {
		t.Fatalf("wire identity = %q, want %q; %s", got, want, raw)
	}
}
