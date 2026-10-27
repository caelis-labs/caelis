package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpingress"
	"github.com/caelis-labs/caelis/internal/acpdraft"
	"github.com/caelis-labs/caelis/internal/acptest/draftpeer"
)

func TestDraftMetadataSurvivesClientCanonicalAndLiveProjection(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent_message", "agent_thought", "user_message", "tool_call_update"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close()
			peer := &draftpeer.Peer{}
			conn, err := peer.Serve(right, right)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			updates := make(chan client.UpdateEnvelope, 8)
			outbound, err := client.NewStreamClient(left, left, client.Config{ACPDraftSchema: acpdraft.SchemaTag, OnUpdate: func(update client.UpdateEnvelope) { updates <- update }})
			if err != nil {
				t.Fatal(err)
			}
			defer outbound.Close(context.Background())
			if _, err := outbound.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			assertMeta := func(label string, meta map[string]any, want string) {
				t.Helper()
				raw, err := json.Marshal(meta)
				if err != nil {
					t.Fatal(err)
				}
				var actual, expected any
				for _, test := range []struct {
					raw    []byte
					target *any
				}{{raw, &actual}, {[]byte(want), &expected}} {
					decoder := json.NewDecoder(bytes.NewReader(test.raw))
					decoder.UseNumber()
					if err := decoder.Decode(test.target); err != nil {
						t.Fatal(err)
					}
				}
				if !reflect.DeepEqual(actual, expected) {
					t.Fatalf("%s metadata = %s, want %s", label, raw, want)
				}
			}
			frames := []struct {
				chunk, empty bool
				meta, want   string
			}{
				{meta: `{"stable":9007199254740993,"overflow":1e400}`, want: `{"stable":9007199254740993,"overflow":1e400}`},
				{chunk: true, meta: `{"transient":9007199254740995}`, want: `{"transient":9007199254740995}`},
				{chunk: true, empty: true, meta: `{"annotation":9007199254740997}`, want: `{"annotation":9007199254740997}`},
				{meta: `{"added":{"decimal":1.2500}}`, want: `{"stable":9007199254740993,"overflow":1e400,"added":{"decimal":1.2500}}`},
			}
			for _, frame := range frames {
				if kind == "tool_call_update" && frame.chunk {
					continue
				}
				value := map[string]any{"sessionUpdate": kind, "_meta": json.RawMessage(frame.meta)}
				if kind == "tool_call_update" {
					value["toolCallId"] = "tool"
					value["status"] = "in_progress"
				} else {
					value["messageId"] = "message"
					value["content"] = []any{map[string]any{"type": "text", "text": "snapshot"}}
					if frame.chunk {
						value["sessionUpdate"] = kind + "_chunk"
						text := " suffix"
						if frame.empty {
							text = ""
						}
						value["content"] = map[string]any{"type": "text", "text": text}
					}
				}
				if err := peer.Update(ctx, "remote", value); err != nil {
					t.Fatal(err)
				}
				var observed client.UpdateEnvelope
				select {
				case observed = <-updates:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				env := acpEnvelopeFromUpdate(observed, nil, nil)
				if env == nil {
					t.Fatal("client update did not project")
				}
				assertMeta("live update", eventstream.UpdateMeta(env.Update), frame.want)
				if chunk, ok := env.Update.(eventstream.ContentChunk); ok && frame.chunk && slices.Contains(chunk.ReplacedFields, "_meta") {
					t.Fatal("live chunk annotation became an entity replacement")
				}
				wire, err := json.Marshal(env)
				if err != nil {
					t.Fatal(err)
				}
				var cached eventstream.Envelope
				if err := json.Unmarshal(wire, &cached); err != nil {
					t.Fatal(err)
				}
				assertMeta("cached update", eventstream.UpdateMeta(cached.Update), frame.want)
				assertMeta("cached envelope", cached.Meta, frame.want)

				event := acpingress.NormalizeUpdate(observed.Update, acpingress.Options{Visibility: acpingress.UIOnlyVisibility})
				if event == nil || event.Protocol == nil || event.Protocol.Update == nil {
					t.Fatal("missing canonical protocol update")
				}
				assertMeta("canonical protocol", event.Protocol.Update.Meta, frame.want)
				if frame.chunk && slices.Contains(event.Protocol.Update.ReplacedFields, "_meta") {
					t.Fatal("canonical chunk annotation became an entity replacement")
				}
				wire, err = json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				var restored session.Event
				if err := json.Unmarshal(wire, &restored); err != nil {
					t.Fatal(err)
				}
				assertMeta("restored protocol", restored.Protocol.Update.Meta, frame.want)
				projected, err := projection.ProjectEvent(session.CloneEvent(&restored))
				if err != nil || len(projected) != 1 {
					t.Fatalf("replay projection: %#v, %v", projected, err)
				}
				assertMeta("replay update", eventstream.UpdateMeta(projected[0]), frame.want)
			}
		})
	}
}
