package controller

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestControllerAgentCommunicationMetaCannotChangeUserInputIdentity(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		marker any
	}{
		{"sender", map[string]any{"source": map[string]any{"kind": "participant", "id": "local-reviewer", "name": "reviewer"}}},
		{"malformed", "untrusted"},
		{"empty", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := map[string]any{
				"caelis": map[string]any{session.AgentCommunicationMetaKey: tc.marker, "display": map[string]any{"kept": true}},
				"vendor": "kept",
			}
			before, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			update := client.ContentChunk{
				SessionUpdate: client.UpdateUserMessage,
				MessageID:     "remote-input-1",
				Content:       json.RawMessage(`{"type":"text","text":"remote input"}`),
				Meta:          meta,
			}
			canonical := normalizeACPUpdateEvent(time.Now, session.ControllerBinding{ControllerID: "remote", Label: "remote"}, "remote-session", "turn-1", update)
			if canonical == nil || canonical.Type != session.EventTypeUser || canonical.Actor.Kind != session.ActorKindUser {
				t.Fatalf("canonical input identity = %#v", canonical)
			}
			native := acpEnvelopeFromUpdate(client.UpdateEnvelope{SessionID: "remote-session", Update: update}, canonical, nil)
			if native == nil || native.AgentCommunicationSource != nil || native.Actor != "user" {
				t.Fatalf("native input identity = %#v", native)
			}
			chunk, ok := native.Update.(eventstream.ContentChunk)
			if !ok {
				t.Fatalf("native update = %T", native.Update)
			}
			wantMeta := map[string]any{"caelis": map[string]any{"display": map[string]any{"kept": true}}, "vendor": "kept"}
			if !reflect.DeepEqual(chunk.Meta, wantMeta) || !reflect.DeepEqual(session.ProtocolUpdateOf(canonical).Meta, wantMeta) {
				t.Fatalf("native/canonical metadata = %#v / %#v, want %#v", chunk.Meta, canonical.Protocol, wantMeta)
			}
			after, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("ingress mutated original metadata: %s", after)
			}

			raw, err := json.Marshal(session.CanonicalizeEvent(canonical))
			if err != nil {
				t.Fatal(err)
			}
			var decoded session.Event
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			replayed, err := session.MigrateEvent(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if err := session.ValidateDurableCoreEvent(&replayed); err != nil {
				t.Fatal(err)
			}
			if !session.IsClientReplayEvent(&replayed) || !reflect.DeepEqual(replayed.Message, canonical.Message) {
				t.Fatalf("replayed input lost visibility or changed model context: %#v", replayed)
			}
			base := projection.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session-1"}, &replayed, projection.SessionEventTransport{})
			projected := projection.ProjectSessionEventEnvelope(base, &replayed)
			if len(projected) != 1 || projected[0].Kind != eventstream.KindSessionUpdate || projected[0].AgentCommunicationSource != nil {
				t.Fatalf("replayed projection = %#v", projected)
			}
			replayChunk, ok := projected[0].Update.(eventstream.ContentChunk)
			if !ok || replayChunk.SessionUpdate != eventstream.UpdateUserMessage || replayChunk.MessageID != chunk.MessageID ||
				session.ExtractProtocolText(replayChunk.Content) != "remote input" {
				t.Fatalf("replayed user chunk = %#v", projected[0].Update)
			}
		})
	}
}
