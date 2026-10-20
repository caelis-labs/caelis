package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func sharedMediaEvent(t testing.TB, size int) Event {
	t.Helper()
	image := model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: strings.Repeat("YWJj", size/4)}, "image/png", "screen")
	message := model.Message{Role: model.RoleTool, Parts: []model.Part{{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{ToolUseID: "call", Name: "screen", Content: []model.Part{image}}}}}
	raw, err := json.Marshal(struct {
		Content []model.Part `json:"content"`
	}{[]model.Part{image}})
	if err != nil {
		t.Fatal(err)
	}
	// Open receipt fields, including exact numeric tokens and future media
	// source fields, must survive packing and expansion.
	raw = bytes.Replace(raw, []byte(`"kind":"inline"`), []byte(`"future_source":{"x":1},"kind":"inline"`), 1)
	raw = append(raw[:len(raw)-1], []byte(`,"integer":9007199254740993123456789,"decimal":1.00,"future":{"x":true}}`)...)
	return Event{Schema: EventSchemaVersion, Type: EventTypeToolResult, Message: &message, Journal: &ExecutionJournalEntry{ToolExecution: &ToolExecution{Result: raw}}}
}

func assertMediaEventEqual(t testing.TB, want, got Event) {
	t.Helper()
	if !reflect.DeepEqual(want.Message, got.Message) {
		t.Fatal("canonical model message changed")
	}
	if !bytes.Equal(want.Journal.ToolExecution.Result, got.Journal.ToolExecution.Result) {
		t.Fatal("execution receipt bytes changed")
	}
}

func TestEventMediaSharesPayloadAndExpandsCompleteReceipt(t *testing.T) {
	original := sharedMediaEvent(t, 64<<10)
	before, _ := json.Marshal(original)
	raw, err := MarshalEventJSON(&original)
	if err != nil {
		t.Fatal(err)
	}
	data := original.Message.Parts[0].ToolResult.Content[0].Media.Source.Data
	if bytes.Count(raw, []byte(data)) != 1 || !bytes.Contains(raw, []byte(`"media":{`)) {
		t.Fatal("media bytes must occur only once in the event")
	}
	var decoded Event
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	assertMediaEventEqual(t, original, decoded)
	after, _ := json.Marshal(original)
	if !bytes.Equal(after, before) {
		t.Fatal("encoding mutated the caller's event")
	}
	again, err := MarshalEventJSON(&decoded)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("packed encoding must remain stable after decoding")
	}
}

func TestEventMediaLegacyAndUnsharedValuesRemainInline(t *testing.T) {
	for _, name := range []string{"legacy", "small", "unique", "url", "opaque", "escaped receipt"} {
		t.Run(name, func(t *testing.T) {
			event := sharedMediaEvent(t, 8192)
			switch name {
			case "legacy":
				event.Schema = 1
			case "small":
				event = sharedMediaEvent(t, 128)
			case "unique":
				event.Journal = nil
			case "url":
				event.Message.Parts[0].ToolResult.Content[0].Media.Source.Kind = model.MediaSourceURL
			case "escaped receipt":
				event.Journal.ToolExecution.Result = bytes.Replace(event.Journal.ToolExecution.Result, []byte(`"inline"`), []byte(`"\u0069nline"`), 1)
			case "opaque":
				event.Journal.ToolExecution.Result = json.RawMessage(`{"content":[{"media":"opaque application value"}]}`)
			}
			raw, err := MarshalEventJSON(&event)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte(`"event_media"`)) {
				t.Fatal("unexpected shared media reference")
			}
			migrated, err := MigrateEventJSON(raw)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Event
			if err := json.Unmarshal(migrated, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(event.Message, decoded.Message) {
				t.Fatal("inline message changed")
			}
		})
	}
}

func TestEventMediaRejectsBrokenReferences(t *testing.T) {
	event := sharedMediaEvent(t, 8192)
	raw, err := MarshalEventJSON(&event)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "digest", "unused", "legacy"} {
		t.Run(name, func(t *testing.T) {
			var root map[string]json.RawMessage
			if err := json.Unmarshal(raw, &root); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing":
				delete(root, "media")
			case "digest":
				root["media"] = json.RawMessage(`{"incorrect":"payload"}`)
			case "unused":
				var table map[string]string
				_ = json.Unmarshal(root["media"], &table)
				table[eventMediaID("unused")] = "unused"
				root["media"], _ = json.Marshal(table)
			case "legacy":
				root["schema"] = json.RawMessage(`1`)
			}
			broken, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Event
			if json.Unmarshal(broken, &decoded) == nil {
				t.Fatal("corrupt media reference accepted")
			}
		})
	}
}

func BenchmarkEventMediaCodec(b *testing.B) {
	for _, schema := range []int{1, EventSchemaVersion} {
		event := sharedMediaEvent(b, 1<<20)
		event.Schema = schema
		raw, err := MarshalEventJSON(&event)
		if err != nil {
			b.Fatal(err)
		}
		name := "shared"
		if schema == 1 {
			name = "legacy"
		}
		b.Run(name+"/encode", func(b *testing.B) {
			b.ReportMetric(float64(len(raw)), "bytes/event")
			b.ReportAllocs()
			for range b.N {
				if _, err := MarshalEventJSON(&event); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/decode", func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				var decoded Event
				if err := json.Unmarshal(raw, &decoded); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestEventMediaSharesNestedJournalOnlyPayloads(t *testing.T) {
	event := sharedMediaEvent(t, 8192)
	image := event.Message.Parts[0].ToolResult.Content[0]
	audio := model.NewMediaPart(model.MediaModalityAudio, image.Media.Source, "audio/wav", "audio")
	event.Message = nil
	content := []model.Part{{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{Content: []model.Part{image, audio}}}}
	raw, err := json.Marshal(struct {
		Content []model.Part `json:"content"`
	}{content})
	if err != nil {
		t.Fatal(err)
	}
	event.Journal.ToolExecution.Result = raw
	packed, err := MarshalEventJSON(&event)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(packed, []byte(image.Media.Source.Data)) != 1 {
		t.Fatal("nested journal media was not shared")
	}
	var restored Event
	if err := json.Unmarshal(packed, &restored); err != nil {
		t.Fatal(err)
	}
	assertMediaEventEqual(t, event, restored)
	var root map[string]json.RawMessage
	if err := json.Unmarshal(packed, &root); err != nil {
		t.Fatal(err)
	}
	delete(root, "media")
	broken, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(broken, &restored) == nil {
		t.Fatal("dangling nested journal reference accepted")
	}
}

func TestLegacyEventMediaRejectedBeforeMigration(t *testing.T) {
	event := sharedMediaEvent(t, 8192)
	packed, err := MarshalEventJSON(&event)
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []int{0, 1} {
		for _, variant := range []string{"shared", "empty table", "message reference", "receipt reference"} {
			t.Run(fmt.Sprintf("schema%d/%s", schema, variant), func(t *testing.T) {
				var root map[string]json.RawMessage
				if err := json.Unmarshal(packed, &root); err != nil {
					t.Fatal(err)
				}
				root["schema"], _ = json.Marshal(schema)
				switch variant {
				case "empty table":
					root["media"] = json.RawMessage(`{}`)
					delete(root, "message")
					delete(root, "journal")
				case "message reference":
					delete(root, "media")
					delete(root, "journal")
				case "receipt reference":
					delete(root, "media")
					delete(root, "message")
				}
				raw, err := json.Marshal(root)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := MigrateEventJSON(raw); err == nil {
					t.Fatal("migration accepted schema-2 media in a legacy record")
				}
				var direct Event
				if json.Unmarshal(raw, &direct) == nil {
					t.Fatal("direct decoder accepted legacy media")
				}
			})
		}
	}
}

func BenchmarkEventMediaEncodeScenarios(b *testing.B) {
	for _, scenario := range []string{"text", "single", "shared"} {
		event := sharedMediaEvent(b, 1<<20)
		switch scenario {
		case "text":
			message := model.NewTextMessage(model.RoleAssistant, "ordinary text event")
			event = Event{Schema: EventSchemaVersion, Message: &message}
		case "single":
			event.Journal = nil
		}
		for _, packed := range []bool{false, true} {
			codec := "expanded"
			if packed {
				codec = "storage"
			}
			b.Run(scenario+"/"+codec, func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					var err error
					if packed {
						_, err = MarshalEventJSON(&event)
					} else {
						_, err = json.Marshal(event)
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
