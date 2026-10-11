package wirev1

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/control/appserver/wirev1/generated"
)

func TestStructuredDiffSurvivesCanonicalAndPublicWire(t *testing.T) {
	t.Parallel()
	oldPath, fileType, mimeType := " old.txt ", "file", "text/plain"
	changes := []session.ProtocolDiffChange{{Operation: "move", Path: " new.txt ", OldPath: &oldPath, FileType: &fileType, MimeType: &mimeType, Meta: map[string]any{"source": "native"}}}
	content := session.ProtocolToolCallContent{Type: "diff", Changes: &changes, Patch: &session.ProtocolDiffPatch{Format: "git_patch", Text: "diff --git a/old.txt b/new.txt\r\n-old\r\n+new\r\n"}, Meta: map[string]any{"label": "files"}}
	for _, protocol := range []bool{false, true} {
		name := "native"
		if protocol {
			name = "protocol"
		}
		t.Run(name, func(t *testing.T) {
			event := session.Event{Type: session.EventTypeToolResult}
			if protocol {
				event.Protocol = &session.EventProtocol{Update: &session.ProtocolUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "tool", Content: []session.ProtocolToolCallContent{content}}}
			} else {
				event.Tool = &session.EventTool{ID: "tool", Name: "edit", Status: "completed", Content: []session.EventToolContent{{Type: "diff", Changes: &changes, Patch: content.Patch, Meta: content.Meta}}}
			}
			raw, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			var restored session.Event
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			for _, source := range []*session.Event{&event, &restored} {
				updates, err := projection.ProjectEvent(source)
				if err != nil || len(updates) != 1 {
					t.Fatalf("projection = %#v, %v", updates, err)
				}
				env := baseEnvelope(eventstream.KindSessionUpdate)
				env.Update = updates[0]
				wire, err := MarshalEnvelope(env)
				if err != nil {
					t.Fatal(err)
				}
				if err := openAPIValidator(t, "Envelope").Validate(decodeJSONWithNumbers(t, wire)); err != nil {
					t.Fatalf("schema: %v; %s", err, wire)
				}
				decoded, err := UnmarshalEnvelope(wire)
				if err != nil {
					t.Fatal(err)
				}
				update := decoded.Update.(eventstream.ToolCallUpdate)
				got := update.Content[0]
				if !reflect.DeepEqual(got.Changes, &changes) || !reflect.DeepEqual(got.Patch, content.Patch) || !reflect.DeepEqual(got.Meta, content.Meta) || got.NewText != "" || got.Path != "" {
					t.Fatalf("diff changed: %#v", got)
				}
			}
		})
	}
}

func TestGeneratedStructuredDiffPreservesEmptyChanges(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{"type":"diff","changes":[]}`, `{"type":"diff","changes":[],"patch":null}`, `{"type":"diff","path":"a","oldText":"old","newText":"new"}`} {
		var content generated.ToolCallContent
		if err := json.Unmarshal([]byte(body), &content); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		if err := openAPIValidator(t, "ToolCallContent").Validate(decodeJSONWithNumbers(t, raw)); err != nil {
			t.Fatal(err)
		}
		if content.Changes != nil {
			var out map[string]json.RawMessage
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			if string(out["changes"]) != "[]" {
				t.Fatalf("empty changes lost: %s", raw)
			}
		}
	}
}
