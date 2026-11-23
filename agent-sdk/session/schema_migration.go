package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

const (
	// EventSchemaVersion is the current durable canonical event schema.
	EventSchemaVersion = 4
)

// SchemaKind identifies one independently migrated durable contract.
type SchemaKind string

const (
	SchemaKindEvent SchemaKind = "event"
)

// SchemaMigration transforms one JSON object from FromVersion to the next
// registered version. Migrations must be deterministic and preserve unknown
// fields.
type SchemaMigration func(json.RawMessage) (json.RawMessage, error)

// SchemaVersionError reports a missing, future, or invalid migration path.
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

type migrationStep struct {
	to      int
	migrate SchemaMigration
}

// MigrationRegistry owns ordered schema migrations. Registration and reads
// are safe for concurrent host initialization/replay.
type MigrationRegistry struct {
	mu    sync.RWMutex
	steps map[SchemaKind]map[int]migrationStep
}

// NewMigrationRegistry returns an empty registry.
func NewMigrationRegistry() *MigrationRegistry {
	return &MigrationRegistry{steps: map[SchemaKind]map[int]migrationStep{}}
}

// Register adds one adjacent version migration and rejects duplicates.
func (r *MigrationRegistry) Register(kind SchemaKind, from, to int, migration SchemaMigration) error {
	if r == nil {
		return fmt.Errorf("agent-sdk/session: migration registry is nil")
	}
	if kind == "" || from < 0 || to != from+1 || migration == nil {
		return &SchemaVersionError{Kind: kind, From: from, To: to, Detail: "migration must be a non-nil adjacent version step"}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.steps == nil {
		r.steps = map[SchemaKind]map[int]migrationStep{}
	}
	if r.steps[kind] == nil {
		r.steps[kind] = map[int]migrationStep{}
	}
	if _, exists := r.steps[kind][from]; exists {
		return &SchemaVersionError{Kind: kind, From: from, To: to, Detail: "migration step is already registered"}
	}
	r.steps[kind][from] = migrationStep{to: to, migrate: migration}
	return nil
}

// Migrate applies every adjacent step from source to target.
func (r *MigrationRegistry) Migrate(kind SchemaKind, source, target int, raw json.RawMessage) (json.RawMessage, error) {
	if r == nil {
		return nil, fmt.Errorf("agent-sdk/session: migration registry is nil")
	}
	if source < 0 || target < 0 || source > target {
		return nil, &SchemaVersionError{Kind: kind, From: source, To: target, Current: target, Detail: "invalid version range"}
	}
	out := append(json.RawMessage(nil), raw...)
	for version := source; version < target; version++ {
		r.mu.RLock()
		step, ok := r.steps[kind][version]
		r.mu.RUnlock()
		if !ok || step.to != version+1 {
			return nil, &SchemaVersionError{Kind: kind, From: version, To: version + 1, Current: target, Detail: "migration step is not registered"}
		}
		var err error
		out, err = step.migrate(out)
		if err != nil {
			return nil, &SchemaVersionError{Kind: kind, From: version, To: version + 1, Current: target, Detail: err.Error()}
		}
	}
	return out, nil
}

func newDefaultMigrationRegistry() *MigrationRegistry {
	registry := NewMigrationRegistry()
	_ = registry.Register(SchemaKindEvent, 0, 1, migrateEventV0ToV1)
	_ = registry.Register(SchemaKindEvent, 1, 2, migrateEventV1ToV2)
	_ = registry.Register(SchemaKindEvent, 2, 3, func(raw json.RawMessage) (json.RawMessage, error) {
		var o map[string]json.RawMessage
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, err
		}
		o["schema"] = json.RawMessage(`3`)
		return json.Marshal(o)
	})
	_ = registry.Register(SchemaKindEvent, 3, 4, func(raw json.RawMessage) (json.RawMessage, error) {
		var o map[string]json.RawMessage
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, err
		}
		o["schema"] = json.RawMessage(`4`)
		return json.Marshal(o)
	})
	registerRetiredExecutionSchemas(registry)
	return registry
}

var builtInMigrationRegistry = newDefaultMigrationRegistry()

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

// MigrateEventJSON upgrades raw durable event JSON before typed decoding so
// every migration step can preserve fields introduced by newer writers.
// Current schemas return an independent copy without re-encoding the payload;
// nested legacy and unsupported schemas still pass through version checks.
func MigrateEventJSON(raw json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	for _, key := range []string{"user_message", "assistant_message", "system_context", "tool_call", "tool_result"} {
		if value := object[key]; len(value) != 0 && string(value) != "null" {
			return nil, fmt.Errorf("%w: legacy semantic field %q", ErrUnsupportedLegacyFormat, key)
		}
	}
	source, err := rawSchemaVersion(SchemaKindEvent, object, EventSchemaVersion)
	if err != nil {
		return nil, err
	}
	registry := builtInMigrationRegistry
	migrated, err := registry.Migrate(SchemaKindEvent, source, EventSchemaVersion, raw)
	if err != nil {
		return nil, err
	}
	if source != EventSchemaVersion {
		if err := json.Unmarshal(migrated, &object); err != nil {
			return nil, err
		}
	}
	journalRaw := object["journal"]
	if len(journalRaw) == 0 || string(journalRaw) == "null" {
		return migrated, nil
	}
	journal, err := migrateJournalJSON(registry, journalRaw)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(journal, journalRaw) {
		return migrated, nil
	}
	object["journal"] = journal
	return json.Marshal(object)
}

func rawSchemaVersion(kind SchemaKind, object map[string]json.RawMessage, current int) (int, error) {
	if object == nil {
		return 0, &SchemaVersionError{Kind: kind, To: current, Current: current, Detail: "durable record must be a JSON object"}
	}
	raw := object["schema"]
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var version int
	if err := json.Unmarshal(raw, &version); err != nil || version < 0 {
		return 0, &SchemaVersionError{Kind: kind, To: current, Current: current, Detail: "schema must be a non-negative integer"}
	}
	if version > current {
		return 0, &SchemaVersionError{Kind: kind, From: version, To: current, Current: current, Detail: "future schema is unsupported"}
	}
	return version, nil
}

func stampCurrentEventSchemas(event *Event) {
	if event == nil || event.Schema != 0 {
		return
	}
	event.Schema = EventSchemaVersion

}

func migrateEventV0ToV1(raw json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	schema, _ := json.Marshal(1)
	object["schema"] = schema
	if legacyPluginContextObject(object) {
		kind, _ := json.Marshal(EventTypeContext)
		object["type"] = kind
	}
	return json.Marshal(object)
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
	var meta map[string]any
	if err := json.Unmarshal(object["_meta"], &meta); err != nil {
		return false
	}
	source, _ := meta["source"].(string)
	return source == "plugin_hook"
}

// Schema 1 remains readable until the supported upgrade floor reaches the first
// schema-2 writer. Migration changes no semantics; only schema 2 can encode
// event-local media references.
func migrateEventV1ToV2(raw json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if err := rejectLegacyEventMedia(raw, object); err != nil {
		return nil, err
	}
	object["schema"] = json.RawMessage(`2`)
	return json.Marshal(object)
}
