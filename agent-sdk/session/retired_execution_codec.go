package session

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Retired execution journal compatibility belongs only to the Session codec.
// Older writers used schema 0/1 nested records in event schema 0-2.
// Remove this adapter when that upgrade floor is no longer supported. It never
// reconstructs execution, Run state, tool bindings or live approval waiters.
func decodeRetiredJournal(e *Event) error {
	if len(e.legacyJournal) == 0 || string(e.legacyJournal) == "null" {
		return nil
	}
	var old map[string]json.RawMessage
	if err := json.Unmarshal(e.legacyJournal, &old); err != nil {
		return err
	}
	if _, err := rawSchemaVersion(schemaRetiredJournal, old, 1); err != nil {
		return err
	}
	for _, key := range []string{"execution", "tool_execution", "pause_token"} {
		raw := old[key]
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var record map[string]json.RawMessage
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if _, err := rawSchemaVersion(SchemaKind(key), record, 1); err != nil {
			return err
		}
	}
	if raw := old["pause_token"]; len(raw) > 0 && string(raw) != "null" {
		if e.Approval != nil {
			return fmt.Errorf("event contains both retired and current approval facts")
		}
		var record map[string]json.RawMessage
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		record["request_id"] = record["token_id"]
		record["schema"] = json.RawMessage(`1`)
		value, err := json.Marshal(record)
		if err != nil {
			return err
		}
		var approval ApprovalRecord
		if err := json.Unmarshal(value, &approval); err != nil {
			return err
		}
		e.Approval = &approval
	}
	if raw := old["tool_execution"]; len(raw) > 0 && string(raw) != "null" {
		var record struct {
			ToolCallID string
			ToolName   string `json:"tool_name"`
			Status     string `json:"status"`
			Key        struct {
				ToolCallID string `json:"tool_call_id"`
			} `json:"key"`
			Parent *tool.ParentCall `json:"parent"`
			Result json.RawMessage  `json:"result"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if len(record.Result) > 0 && string(record.Result) != "null" {
			var result tool.Result
			decoder := json.NewDecoder(bytes.NewReader(record.Result))
			decoder.UseNumber()
			if err := decoder.Decode(&result); err != nil {
				return err
			}
			if e.Tool == nil {
				e.Tool = &EventTool{ID: record.Key.ToolCallID, Name: record.ToolName}
				if record.Parent != nil {
					e.Tool.Parent = &EventParentTool{CallID: record.Parent.ID, Name: record.Parent.Name}
				}
			}
			if e.Tool.Result == nil {
				e.Tool.Result = &result
			}
			switch record.Status {
			case "succeeded":
				e.Tool.Outcome = ToolOutcomeSucceeded
			case "failed", "cancelled":
				e.Tool.Outcome = ToolOutcomeFailed
			default:
				e.Tool.Outcome = ToolOutcomeUnknown
			}
		}
	}

	return nil
}

const (
	schemaRetiredJournal SchemaKind = "journal"
	schemaRetiredRun     SchemaKind = "run"
	schemaRetiredTool    SchemaKind = "tool_execution"
	schemaRetiredPause   SchemaKind = "pause_token"
)

func registerRetiredExecutionSchemas(registry *MigrationRegistry) {
	_ = registry.Register(schemaRetiredJournal, 0, 1, migrateJournalV0ToV1)
	_ = registry.Register(schemaRetiredRun, 0, 1, migrateSchemaFieldV0ToV1)
	_ = registry.Register(schemaRetiredTool, 0, 1, migrateSchemaFieldV0ToV1)
	_ = registry.Register(schemaRetiredPause, 0, 1, migrateSchemaFieldV0ToV1)
}
func migrateJournalJSON(registry *MigrationRegistry, raw json.RawMessage) (json.RawMessage, error) {
	var journal map[string]json.RawMessage
	if err := json.Unmarshal(raw, &journal); err != nil {
		return nil, err
	}
	source, err := rawSchemaVersion(schemaRetiredJournal, journal, 1)
	if err != nil {
		return nil, err
	}
	migrated, err := registry.Migrate(schemaRetiredJournal, source, 1, raw)
	if err != nil {
		return nil, err
	}
	changed := source != 1
	if changed {
		if err := json.Unmarshal(migrated, &journal); err != nil {
			return nil, err
		}
	}
	nested := []struct {
		key    string
		kind   SchemaKind
		target int
	}{
		{key: "execution", kind: schemaRetiredRun, target: 1},
		{key: "tool_execution", kind: schemaRetiredTool, target: 1},
		{key: "pause_token", kind: schemaRetiredPause, target: 1},
	}
	for _, candidate := range nested {
		nestedRaw := journal[candidate.key]
		if len(nestedRaw) == 0 || string(nestedRaw) == "null" {
			continue
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(nestedRaw, &object); err != nil {
			return nil, err
		}
		source, err := rawSchemaVersion(candidate.kind, object, candidate.target)
		if err != nil {
			return nil, err
		}
		if source == candidate.target {
			continue
		}
		nestedRaw, err = registry.Migrate(candidate.kind, source, candidate.target, nestedRaw)
		if err != nil {
			return nil, err
		}
		journal[candidate.key] = nestedRaw
		changed = true
	}
	if !changed {
		return migrated, nil
	}
	return json.Marshal(journal)
}

func migrateSchemaFieldV0ToV1(raw json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	version, err := rawSchemaVersion(SchemaKind("retired_record"), object, 1)
	if err != nil {
		return nil, err
	}
	if version == 1 {
		return append(json.RawMessage(nil), raw...), nil
	}
	object["schema"] = json.RawMessage(`1`)
	return json.Marshal(object)
}

// The journal migration owns only its version. Each nested record is validated
// and migrated independently by migrateJournalJSON, including under v0 events.
func migrateJournalV0ToV1(raw json.RawMessage) (json.RawMessage, error) {
	return migrateSchemaFieldV0ToV1(raw)
}
