package client

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDraftOutboundEntityMetadataPatches(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent_message", "agent_thought", "user_message", "tool_call_update"} {
		t.Run(kind, func(t *testing.T) {
			agent := &draftTestAgent{}
			client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
			if _, err := client.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			send := func(meta json.RawMessage) map[string]any {
				t.Helper()
				value := map[string]any{"sessionUpdate": kind}
				if kind == "tool_call_update" {
					value["toolCallId"] = "entity"
				} else {
					value["messageId"] = "entity"
					value["content"] = []any{map[string]any{"type": "text", "text": "retained"}}
				}
				if meta != nil {
					value["_meta"] = meta
				}
				if err := agent.update(ctx, "session", value); err != nil {
					t.Fatal(err)
				}
				switch update := requireDraftUpdate(t, ctx, updates).Update.(type) {
				case ContentChunk:
					return update.Meta
				case ToolCallUpdate:
					return update.Meta
				default:
					t.Fatalf("unexpected update %T", update)
					return nil
				}
			}
			initial := send(json.RawMessage(`{"keep":true,"remove":"old","nested":{"old":true}}`))
			patched := send(json.RawMessage(`{"remove":null,"nested":{"new":true}}`))
			want := map[string]any{"keep": true, "nested": map[string]any{"new": true}}
			if !reflect.DeepEqual(patched, want) {
				t.Fatalf("top-level patch = %#v, want %#v", patched, want)
			}
			if initial["remove"] != "old" || !reflect.DeepEqual(initial["nested"], map[string]any{"old": true}) {
				t.Fatalf("later patch mutated earlier delivery: %#v", initial)
			}
			if got := send(json.RawMessage(`{}`)); !reflect.DeepEqual(got, want) {
				t.Fatalf("empty patch changed metadata: %#v", got)
			}
			if got := send(json.RawMessage(`null`)); got != nil {
				t.Fatalf("null did not clear metadata: %#v", got)
			}
			if got := send(nil); got != nil {
				t.Fatalf("omitted metadata resurrected cleared keys: %#v", got)
			}
		})
	}
}

func TestDraftToolMetadataCountsTowardCacheBound(t *testing.T) {
	t.Parallel()
	d := newDraftClient(nil)
	session, err := d.ensureSessionLocked("session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.observeToolLocked(session, "tool"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.patchToolMetaLocked(session, "tool", json.RawMessage(`{"label":"old"}`)); err != nil {
		t.Fatal(err)
	}
	// Fill the same accounting budget used by real notification ingress.
	if err := d.reserveCacheLocked(session, draftMaxSessionCacheBytes-session.cacheBytes, 0); err != nil {
		t.Fatal(err)
	}
	_, err = d.patchToolMetaLocked(session, "tool", json.RawMessage(`{"label":"`+strings.Repeat("x", 256)+`"}`))
	if err == nil || !strings.Contains(err.Error(), "cache byte limit") {
		t.Fatalf("oversized retained metadata error = %v", err)
	}
	if session.tools["tool"].meta["label"] != "old" {
		t.Fatal("rejected patch changed retained metadata")
	}
}

func TestDraftMessageEntityAndChunkMetadataAreIndependent(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent_message", "agent_thought", "user_message"} {
		t.Run(kind, func(t *testing.T) {
			agent := &draftTestAgent{}
			client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
			if _, err := client.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			send := func(chunk bool, meta json.RawMessage) ContentChunk {
				t.Helper()
				value := map[string]any{"sessionUpdate": kind, "messageId": "entity", "content": []any{map[string]any{"type": "text", "text": "snapshot"}}}
				if chunk {
					value["sessionUpdate"] = kind + "_chunk"
					value["content"] = map[string]any{"type": "text", "text": " suffix"}
				}
				if meta != nil {
					value["_meta"] = meta
				}
				if err := agent.update(ctx, "session", value); err != nil {
					t.Fatal(err)
				}
				return requireDraftUpdate(t, ctx, updates).Update.(ContentChunk)
			}
			initial := send(false, json.RawMessage(`{"stable":"entity"}`))
			for _, meta := range []json.RawMessage{json.RawMessage(`{"transient":"chunk"}`), nil, json.RawMessage(`null`)} {
				chunk := send(true, meta)
				if chunk.MetaPresent {
					t.Fatal("chunk annotation became an entity metadata replacement")
				}
				var want map[string]any
				if meta != nil {
					if err := json.Unmarshal(meta, &want); err != nil {
						t.Fatal(err)
					}
				}
				if !reflect.DeepEqual(chunk.Meta, want) {
					t.Fatalf("chunk metadata = %#v, want only %#v", chunk.Meta, want)
				}
				patched := send(false, json.RawMessage(`{"added":"patch"}`))
				if !reflect.DeepEqual(patched.Meta, map[string]any{"stable": "entity", "added": "patch"}) {
					t.Fatalf("chunk contaminated entity metadata: %#v", patched.Meta)
				}
			}
			if !reflect.DeepEqual(initial.Meta, map[string]any{"stable": "entity"}) {
				t.Fatalf("later frames mutated earlier delivery: %#v", initial.Meta)
			}
		})
	}
}

func TestDraftMetadataPreservesNumericTokens(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent_message", "agent_message_chunk", "agent_thought", "user_message", "tool_call_update"} {
		t.Run(kind, func(t *testing.T) {
			agent := &draftTestAgent{}
			client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
			if _, err := client.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			value := map[string]any{"sessionUpdate": kind, "_meta": json.RawMessage(`{"large":9007199254740993,"nested":{"decimal":1.2500}}`)}
			if kind == "tool_call_update" {
				value["toolCallId"] = "tool"
			} else {
				value["messageId"] = "entity"
				value["content"] = []any{map[string]any{"type": "text", "text": "snapshot"}}
				if strings.HasSuffix(kind, "_chunk") {
					value["content"] = map[string]any{"type": "text", "text": "chunk"}
				}
			}
			if err := agent.update(ctx, "session", value); err != nil {
				t.Fatal(err)
			}
			var meta map[string]any
			switch update := requireDraftUpdate(t, ctx, updates).Update.(type) {
			case ContentChunk:
				meta = update.Meta
			case ToolCallUpdate:
				meta = update.Meta
			}
			if meta["large"] != json.Number("9007199254740993") || meta["nested"].(map[string]any)["decimal"] != json.Number("1.2500") {
				t.Fatalf("numeric metadata lost tokens: %#v", meta)
			}
		})
	}
}
