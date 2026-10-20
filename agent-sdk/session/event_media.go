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

type eventMediaJSON struct {
	*eventJSON
	Media map[string]string `json:"media,omitempty"`
}

// MarshalEventJSON encodes a durable event with repeated large inline media
// shared within that record. Ordinary json.Marshal retains the fully expanded
// representation for semantic comparisons and non-storage consumers. Both forms
// decode to the same Event. No media depends on another record or external file.
func MarshalEventJSON(e *Event) ([]byte, error) {
	if e == nil {
		return []byte("null"), nil
	}
	if e.Schema != EventSchemaVersion {
		return json.Marshal(eventJSON(*e))
	}
	counts := map[string]int{}
	_, err := mapEventMedia(*e, model.MediaSourceInline, func(source model.MediaSource) (model.MediaSource, error) {
		if source.Kind == model.MediaSourceInline && len(source.Data) >= minSharedMediaBytes {
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
		return json.Marshal(eventJSON(*e))
	}
	packed, err := mapEventMedia(*e, model.MediaSourceInline, func(source model.MediaSource) (model.MediaSource, error) {
		if source.Kind == model.MediaSourceInline {
			if id, ok := refs[source.Data]; ok {
				source.Kind, source.Data = eventMediaSourceKind, id
			}
		}
		return source, nil
	})
	if err != nil {
		return nil, err
	}
	alias := eventJSON(packed)
	return json.Marshal(eventMediaJSON{eventJSON: &alias, Media: media})
}

func eventMediaID(data string) string {
	digest := sha256.Sum256([]byte(data))
	return hex.EncodeToString(digest[:])
}

func expandEventMedia(e Event, media map[string]string) (Event, error) {
	if len(media) > 0 && e.Schema != EventSchemaVersion {
		return Event{}, fmt.Errorf("agent-sdk/session: event media requires schema %d", EventSchemaVersion)
	}
	for id, data := range media {
		if eventMediaID(data) != id {
			return Event{}, fmt.Errorf("agent-sdk/session: event media digest mismatch: %s", id)
		}
	}
	used := map[string]bool{}
	out, err := mapEventMedia(e, eventMediaSourceKind, func(source model.MediaSource) (model.MediaSource, error) {
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
		return Event{}, err
	}
	if len(used) != len(media) {
		return Event{}, fmt.Errorf("agent-sdk/session: unreferenced event media")
	}
	return out, nil
}

// mapEventMedia copies only the message and journal branches it visits. The raw
// receipt remains an open JSON value: unrelated fields and numeric tokens are
// preserved rather than decoding through tool.Result or map[string]any.
func mapEventMedia(e Event, kind model.MediaSourceKind, visit func(model.MediaSource) (model.MediaSource, error)) (Event, error) {
	out := e
	if e.Message != nil {
		message := model.CloneMessage(*e.Message)
		if err := visitPartMedia(message.Parts, visit); err != nil {
			return Event{}, err
		}
		out.Message = &message
	}
	if e.Journal != nil && e.Journal.ToolExecution != nil && len(e.Journal.ToolExecution.Result) > 0 {
		result, err := mapReceiptMedia(e.Journal.ToolExecution.Result, kind, visit)
		if err != nil {
			return Event{}, err
		}
		journal := *e.Journal
		execution := *journal.ToolExecution
		execution.Result = result
		journal.ToolExecution = &execution
		out.Journal = &journal
	}
	return out, nil
}

func visitPartMedia(parts []model.Part, visit func(model.MediaSource) (model.MediaSource, error)) error {
	for i := range parts {
		if parts[i].Media != nil {
			source, err := visit(parts[i].Media.Source)
			if err != nil {
				return err
			}
			parts[i].Media.Source = source
		}
		if parts[i].ToolResult != nil {
			if err := visitPartMedia(parts[i].ToolResult.Content, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func mapReceiptMedia(raw json.RawMessage, kind model.MediaSourceKind, visit func(model.MediaSource) (model.MediaSource, error)) (json.RawMessage, error) {
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
	if err := walk(gjson.GetBytes(raw, "content"), "content"); err != nil {
		return nil, err
	}
	return out, nil
}
