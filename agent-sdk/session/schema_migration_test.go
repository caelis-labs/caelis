package session_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestDefaultMigrationsUpgradeEventAndNestedJournalSchemas(t *testing.T) {
	t.Parallel()

	record := session.ExecutionRecord{
		Kind: session.JournalKindRun, SessionID: "session-1", RunID: "run-1",
		Revision: 1, Status: session.ExecutionPrepared,
	}
	record.Identity = session.ExecutionRecordIdentity(record)
	migrated, err := session.MigrateEvent(session.Event{
		Type:       session.EventTypeLifecycle,
		Visibility: session.VisibilityJournal,
		Journal: &session.ExecutionJournalEntry{
			Kind:      session.JournalKindRun,
			Execution: &record,
		},
	})
	if err != nil {
		t.Fatalf("MigrateEvent() error = %v", err)
	}
	if migrated.Schema != session.EventSchemaVersion || migrated.Journal.Schema != session.ExecutionJournalSchemaVersion || migrated.Journal.Execution.Schema != session.RunSchemaVersion {
		t.Fatalf("migrated schemas = event:%d journal:%d run:%d", migrated.Schema, migrated.Journal.Schema, migrated.Journal.Execution.Schema)
	}
	if err := session.ValidateDurableCoreEvent(&migrated); err != nil {
		t.Fatalf("ValidateDurableCoreEvent() error = %v", err)
	}
}

func TestPrepareEventsForAppendAlwaysPersistsCurrentEventSchema(t *testing.T) {
	t.Parallel()

	message := model.NewTextMessage(model.RoleUser, "hello")
	prepared, err := session.PrepareEventsForAppend(session.PrepareEventsForAppendRequest{
		SessionID: "session-1",
		Events: []*session.Event{{
			Type: session.EventTypeUser, Message: &message,
		}},
	})
	if err != nil {
		t.Fatalf("PrepareEventsForAppend() error = %v", err)
	}
	if len(prepared.Persisted) != 1 || prepared.Persisted[0].Schema != session.EventSchemaVersion {
		t.Fatalf("persisted = %#v, want current event schema", prepared.Persisted)
	}
}

func TestMigrationRegistryRejectsGapsDuplicatesAndFutureSchemas(t *testing.T) {
	t.Parallel()

	registry := session.NewMigrationRegistry()
	identity := func(raw json.RawMessage) (json.RawMessage, error) { return raw, nil }
	if err := registry.Register(session.SchemaKindEvent, 0, 2, identity); err == nil {
		t.Fatal("Register(non-adjacent) error = nil")
	}
	if err := registry.Register(session.SchemaKindEvent, 0, 1, identity); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := registry.Register(session.SchemaKindEvent, 0, 1, identity); err == nil {
		t.Fatal("Register(duplicate) error = nil")
	}
	_, err := registry.Migrate(session.SchemaKindEvent, 0, 2, json.RawMessage(`{}`))
	var versionErr *session.SchemaVersionError
	if !errors.As(err, &versionErr) || versionErr.From != 1 {
		t.Fatalf("Migrate(gap) error = %v, want SchemaVersionError at version 1", err)
	}
	_, err = session.MigrateEvent(session.Event{Schema: session.EventSchemaVersion + 1})
	if !errors.As(err, &versionErr) {
		t.Fatalf("MigrateEvent(future) error = %v, want SchemaVersionError", err)
	}
}
