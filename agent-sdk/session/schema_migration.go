package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/tidwall/gjson"
)

const (
	// EventSchemaVersion is the current durable canonical event schema.
	EventSchemaVersion = 4
)

// SchemaKind names the durable contract that rejected a version.
type SchemaKind string

const (
	SchemaKindEvent SchemaKind = "event"
)

// SchemaVersionError reports an unsupported durable schema.
type SchemaVersionError struct {
	Kind    SchemaKind
	From    int
	To      int
	Current int
	Detail  string
}

func (e *SchemaVersionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("agent-sdk/session: %s schema migration %d -> %d (current %d): %s", e.Kind, e.From, e.To, e.Current, e.Detail)
}

func (e *SchemaVersionError) ErrorCode() errorcode.Code { return errorcode.Unsupported }

// MigrateEvent upgrades supported durable event encodings at the Session boundary.
func MigrateEvent(in Event) (Event, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return Event{}, err
	}
	raw, err = MigrateEventJSON(raw)
	if err != nil {
		return Event{}, err
	}
	var out Event
	if err := json.Unmarshal(raw, &out); err != nil {
		return Event{}, err
	}
	// Text is an in-memory display override and intentionally excluded from the
	// durable JSON schema. Preserve it while migrating the durable fields.
	out.Text = in.Text
	return out, nil
}

// UnmarshalEventJSON reads one durable event written by a supported Session
// schema. It upgrades the record before decoding and checks its durable fields.
func UnmarshalEventJSON(raw []byte) (*Event, error) {
	migrated, err := MigrateEventJSON(raw)
	if err != nil {
		return nil, err
	}
	var event Event
	if err := json.Unmarshal(migrated, &event); err != nil {
		return nil, err
	}
	if err := ValidateDurableCoreEvent(&event); err != nil {
		return nil, err
	}
	return &event, nil
}

// MigrateEventJSON upgrades the event envelope, preserves unknown JSON fields,
// and returns a buffer independent of the input.
func MigrateEventJSON(raw json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	source, err := eventSchemaVersion(object)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"user_message", "assistant_message", "system_context", "tool_call", "tool_result"} {
		if value := object[key]; len(value) != 0 && string(value) != "null" {
			return nil, fmt.Errorf("%w: legacy semantic field %q", ErrUnsupportedLegacyFormat, key)
		}
	}
	if source == EventSchemaVersion {
		return bytes.Clone(raw), nil
	}
	if source < 2 {
		if err := rejectLegacyEventMedia(raw, object); err != nil {
			return nil, &SchemaVersionError{Kind: SchemaKindEvent, From: source, To: EventSchemaVersion, Current: EventSchemaVersion, Detail: err.Error()}
		}
	}
	if source == 0 && legacyPluginContextObject(object) {
		object["type"], _ = json.Marshal(EventTypeContext)
	}
	object["schema"] = json.RawMessage(strconv.Itoa(EventSchemaVersion))
	return json.Marshal(object)
}

func eventSchemaVersion(object map[string]json.RawMessage) (int, error) {
	if object == nil {
		return 0, &SchemaVersionError{Kind: SchemaKindEvent, To: EventSchemaVersion, Current: EventSchemaVersion, Detail: "durable record must be a JSON object"}
	}
	raw := object["schema"]
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var version int
	if err := json.Unmarshal(raw, &version); err != nil || version < 0 {
		return 0, &SchemaVersionError{Kind: SchemaKindEvent, To: EventSchemaVersion, Current: EventSchemaVersion, Detail: "schema must be a non-negative integer"}
	}
	if version > EventSchemaVersion {
		return 0, &SchemaVersionError{Kind: SchemaKindEvent, From: version, To: EventSchemaVersion, Current: EventSchemaVersion, Detail: "future schema is unsupported"}
	}
	return version, nil
}

func stampCurrentEventSchemas(event *Event) {
	if event == nil || event.Schema != 0 {
		return
	}
	event.Schema = EventSchemaVersion
}

func legacyPluginContextObject(object map[string]json.RawMessage) bool {
	var eventType EventType
	_ = json.Unmarshal(object["type"], &eventType)
	if eventType != "" && eventType != EventTypeCustom {
		return false
	}
	if len(object["message"]) == 0 || string(object["message"]) == "null" {
		return false
	}
	return gjson.GetBytes(object["_meta"], "source").String() == "plugin_hook"
}
