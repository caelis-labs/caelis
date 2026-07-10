package session

import (
	"encoding/json"
	"fmt"
	"sync"
)

const (
	// EventSchemaVersion is the current durable canonical event schema.
	EventSchemaVersion = 1
	// RunSchemaVersion is the current durable Run/Turn/Step record schema.
	RunSchemaVersion = ExecutionJournalSchemaVersion
)

// SchemaKind identifies one independently migrated durable contract.
type SchemaKind string

const (
	SchemaKindEvent         SchemaKind = "event"
	SchemaKindRun           SchemaKind = "run"
	SchemaKindToolExecution SchemaKind = "tool_execution"
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

// DefaultMigrationRegistry returns the built-in pre-v1 migration set.
func DefaultMigrationRegistry() *MigrationRegistry {
	registry := NewMigrationRegistry()
	_ = registry.Register(SchemaKindEvent, 0, EventSchemaVersion, migrateEventV0ToV1)
	_ = registry.Register(SchemaKindRun, 0, RunSchemaVersion, migrateSchemaFieldV0ToV1)
	_ = registry.Register(SchemaKindToolExecution, 0, ToolExecutionSchemaVersion, migrateSchemaFieldV0ToV1)
	return registry
}

// MigrateEvent upgrades one event and nested execution journal records.
func MigrateEvent(in Event) (Event, error) {
	if in.Schema == EventSchemaVersion {
		return *CloneEvent(&in), nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return Event{}, err
	}
	if in.Schema > EventSchemaVersion {
		return Event{}, &SchemaVersionError{Kind: SchemaKindEvent, From: in.Schema, To: EventSchemaVersion, Current: EventSchemaVersion, Detail: "future schema is unsupported"}
	}
	raw, err = DefaultMigrationRegistry().Migrate(SchemaKindEvent, in.Schema, EventSchemaVersion, raw)
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

func stampCurrentEventSchemas(event *Event) {
	if event == nil || event.Schema != 0 {
		return
	}
	event.Schema = EventSchemaVersion
	if event.Journal == nil {
		return
	}
	event.Journal.Schema = ExecutionJournalSchemaVersion
	if event.Journal.Execution != nil && event.Journal.Execution.Schema == 0 {
		event.Journal.Execution.Schema = RunSchemaVersion
	}
	if event.Journal.ToolExecution != nil && event.Journal.ToolExecution.Schema == 0 {
		event.Journal.ToolExecution.Schema = ToolExecutionSchemaVersion
	}
	if event.Journal.PauseToken != nil && event.Journal.PauseToken.Schema == 0 {
		event.Journal.PauseToken.Schema = ExecutionJournalSchemaVersion
	}
}

// MigrateExecutionRecord upgrades one durable Run/Turn/Step record.
func MigrateExecutionRecord(in ExecutionRecord) (ExecutionRecord, error) {
	raw, err := migrateTypedSchema(SchemaKindRun, in.Schema, RunSchemaVersion, in)
	if err != nil {
		return ExecutionRecord{}, err
	}
	var out ExecutionRecord
	if err := json.Unmarshal(raw, &out); err != nil {
		return ExecutionRecord{}, err
	}
	return out, nil
}

// MigrateToolExecution upgrades one durable tool execution record.
func MigrateToolExecution(in ToolExecution) (ToolExecution, error) {
	raw, err := migrateTypedSchema(SchemaKindToolExecution, in.Schema, ToolExecutionSchemaVersion, in)
	if err != nil {
		return ToolExecution{}, err
	}
	var out ToolExecution
	if err := json.Unmarshal(raw, &out); err != nil {
		return ToolExecution{}, err
	}
	return out, nil
}

func migrateTypedSchema(kind SchemaKind, source, target int, value any) (json.RawMessage, error) {
	if source > target {
		return nil, &SchemaVersionError{Kind: kind, From: source, To: target, Current: target, Detail: "future schema is unsupported"}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return DefaultMigrationRegistry().Migrate(kind, source, target, raw)
}

func migrateSchemaFieldV0ToV1(raw json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	schema, _ := json.Marshal(1)
	object["schema"] = schema
	return json.Marshal(object)
}

func migrateEventV0ToV1(raw json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	schema, _ := json.Marshal(EventSchemaVersion)
	object["schema"] = schema
	if legacyPluginContextObject(object) {
		kind, _ := json.Marshal(EventTypeContext)
		object["type"] = kind
	}
	if journalRaw := object["journal"]; len(journalRaw) > 0 && string(journalRaw) != "null" {
		migrated, err := migrateJournalV0ToV1(journalRaw)
		if err != nil {
			return nil, err
		}
		object["journal"] = migrated
	}
	return json.Marshal(object)
}

func migrateJournalV0ToV1(raw json.RawMessage) (json.RawMessage, error) {
	var journal map[string]json.RawMessage
	if err := json.Unmarshal(raw, &journal); err != nil {
		return nil, err
	}
	schema, _ := json.Marshal(ExecutionJournalSchemaVersion)
	journal["schema"] = schema
	for _, key := range []string{"execution", "tool_execution", "pause_token"} {
		if nestedRaw := journal[key]; len(nestedRaw) > 0 && string(nestedRaw) != "null" {
			nested, err := migrateSchemaFieldV0ToV1(nestedRaw)
			if err != nil {
				return nil, err
			}
			journal[key] = nested
		}
	}
	return json.Marshal(journal)
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
