package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Event-local media shares repeated inline payloads between a canonical message
// and its execution receipt. The table lives in the same JSON record (including
// the WAL), so it has exactly the event's atomicity and retention. References
// never escape decoding into the model or recovery contracts. Schema 2 prevents
// older readers from silently interpreting references as actual media bytes.
const eventMediaSourceKind model.MediaSourceKind = "event_media"

const minSharedMediaBytes = 4096

type eventJSON Event

// MarshalEventJSON encodes a durable event with repeated large inline media
// shared within that record. Ordinary json.Marshal retains expanded values.
func MarshalEventJSON(e *Event) ([]byte, error) {
	if e == nil {
		return []byte("null"), nil
	}
	raw, err := json.Marshal(eventJSON(*e))
	if err != nil || e.Schema != EventSchemaVersion {
		return raw, err
	}
	counts := map[string]int{}
	_, err = mapEventMedia(raw, model.MediaSourceInline, func(source model.MediaSource) (model.MediaSource, error) {
		if len(source.Data) >= minSharedMediaBytes {
			counts[source.Data]++
		}
		return source, nil
	})
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	media := map[string]string{}
	for data, count := range counts {
		if count > 1 {
			id := eventMediaID(data)
			refs[data] = id
			media[id] = data
		}
	}
	if len(media) == 0 {
		return raw, nil
	}
	packed, err := mapEventMedia(raw, model.MediaSourceInline, func(source model.MediaSource) (model.MediaSource, error) {
		if id, ok := refs[source.Data]; ok {
			source.Kind, source.Data = eventMediaSourceKind, id
		}
		return source, nil
	})
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(media)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(packed, "media", encoded)
}

func eventMediaID(data string) string {
	digest := sha256.Sum256([]byte(data))
	return hex.EncodeToString(digest[:])
}

func expandEventMedia(raw []byte) ([]byte, error) {
	var media map[string]string
	table := gjson.GetBytes(raw, "media")
	if table.Exists() {
		if err := json.Unmarshal([]byte(table.Raw), &media); err != nil {
			return nil, err
		}
	}
	schema := gjson.GetBytes(raw, "schema").Int()
	if media != nil && schema != 2 && schema != 3 && schema != EventSchemaVersion {
		return nil, fmt.Errorf("agent-sdk/session: event media requires schema %d", EventSchemaVersion)
	}
	for id, data := range media {
		if eventMediaID(data) != id {
			return nil, fmt.Errorf("agent-sdk/session: event media digest mismatch: %s", id)
		}
	}
	used := map[string]bool{}
	out, err := mapEventMedia(raw, eventMediaSourceKind, func(source model.MediaSource) (model.MediaSource, error) {
		if source.Kind != eventMediaSourceKind {
			return source, nil
		}
		data, ok := media[source.Data]
		if !ok {
			return source, fmt.Errorf("agent-sdk/session: missing event media: %s", source.Data)
		}
		used[source.Data] = true
		source.Kind, source.Data = model.MediaSourceInline, data
		return source, nil
	})
	if err != nil {
		return nil, err
	}
	if len(used) != len(media) {
		// A schema-2 table can have entries referenced only by a retired
		// journal. Count those entries without expanding the discarded body.
		_, err := mapRawMedia(raw, "journal.tool_execution.result.content", eventMediaSourceKind, func(source model.MediaSource) (model.MediaSource, error) {
			if _, exists := media[source.Data]; exists {
				used[source.Data] = true
			}
			return source, nil
		})
		if err != nil {
			return nil, err
		}
		if len(used) != len(media) {
			return nil, fmt.Errorf("agent-sdk/session: unreferenced event media")
		}
	}
	return out, nil
}

func mapEventMedia(raw json.RawMessage, kind model.MediaSourceKind, visit func(model.MediaSource) (model.MediaSource, error)) (json.RawMessage, error) {
	for _, path := range []string{"input_evidence.message.parts", "message.parts", "tool.result.content"} {
		var err error
		raw, err = mapRawMedia(raw, path, kind, visit)
		if err != nil {
			return nil, err
		}
	}
	return raw, nil
}

func mapRawMedia(raw json.RawMessage, contentPath string, kind model.MediaSourceKind, visit func(model.MediaSource) (model.MediaSource, error)) (json.RawMessage, error) {
	// Inspect only tool content paths, never similarly named application JSON.
	// gjson/sjson preserve the original receipt's field order, numeric tokens,
	// unknown fields and escapes; changing them would break full-payload retries.
	out := raw
	var walk func(gjson.Result, string) error
	walk = func(content gjson.Result, path string) error {
		if !content.IsArray() {
			return nil
		}
		for i, part := range content.Array() {
			prefix := path + "." + strconv.Itoa(i)
			if part.Get("type").String() == "image" {
				field := "data"
				if kind == eventMediaSourceKind {
					field = "media_ref"
				}
				value := part.Get(field)
				if !value.Exists() {
					continue
				}
				if kind == eventMediaSourceKind && (value.Type != gjson.String || part.Get("data").Exists()) {
					return fmt.Errorf("agent-sdk/session: invalid image media reference")
				}
				source := model.MediaSource{Kind: kind, Data: value.String()}
				next, err := visit(source)
				if err != nil {
					return err
				}
				if next != source {
					out, err = sjson.DeleteBytes(out, prefix+"."+field)
					if err != nil {
						return err
					}
					field = "data"
					if next.Kind == eventMediaSourceKind {
						field = "media_ref"
					}
					out, err = sjson.SetBytes(out, prefix+"."+field, next.Data)
					if err != nil {
						return err
					}
				}
				continue
			}
			switch part.Get("kind").String() {
			case string(model.PartKindMedia):
				sourceRaw := part.Get("media.source")
				if sourceRaw.Get("kind").String() != string(kind) {
					continue
				}
				var source model.MediaSource
				if err := json.Unmarshal([]byte(sourceRaw.Raw), &source); err != nil {
					return err
				}
				// Only canonical string encodings are packed. Historical receipts
				// with alternate escape spellings remain byte-for-byte inline.
				if kind == model.MediaSourceInline {
					encodedKind, _ := json.Marshal(source.Kind)
					encodedData, _ := json.Marshal(source.Data)
					if !bytes.Equal(encodedKind, []byte(sourceRaw.Get("kind").Raw)) || !bytes.Equal(encodedData, []byte(sourceRaw.Get("data").Raw)) {
						continue
					}
				}
				next, err := visit(source)
				if err != nil {
					return err
				}
				if next != source {
					encodedKind, _ := json.Marshal(next.Kind)
					encodedData, _ := json.Marshal(next.Data)
					out, err = sjson.SetRawBytes(out, prefix+".media.source.kind", encodedKind)
					if err != nil {
						return err
					}
					out, err = sjson.SetRawBytes(out, prefix+".media.source.data", encodedData)
					if err != nil {
						return err
					}
				}
			case string(model.PartKindToolResult):
				if err := walk(part.Get("tool_result.content"), prefix+".tool_result.content"); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(gjson.GetBytes(raw, contentPath), contentPath); err != nil {
		return nil, err
	}
	return out, nil
}

// Validate before stamping the new schema: a legacy record cannot acquire
// permission to use schema-2-only encoding merely by passing through migration.
func rejectLegacyEventMedia(raw json.RawMessage, object map[string]json.RawMessage) error {
	if media, ok := object["media"]; ok && !bytes.Equal(bytes.TrimSpace(media), []byte("null")) {
		return fmt.Errorf("legacy event cannot contain an event media table")
	}
	_, err := mapEventMedia(raw, eventMediaSourceKind, func(source model.MediaSource) (model.MediaSource, error) {
		return source, fmt.Errorf("legacy event cannot contain an event media reference")
	})
	return err
}
