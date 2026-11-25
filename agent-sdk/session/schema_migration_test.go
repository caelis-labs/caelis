package session_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

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

func TestEventEnvelopeKeepsOpaqueFields(t *testing.T) {
	for version := 0; version <= session.EventSchemaVersion; version++ {
		raw := json.RawMessage(fmt.Sprintf(` { "schema":%d, "type":"lifecycle", "visibility":"journal", "extra":9007199254740993123456789, "journal":{"schema":899,"pause_token":{"schema":100},"tool_execution":{"result":true}} } `, version))
		migrated, err := session.MigrateEventJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(migrated, &fields); err != nil {
			t.Fatal(err)
		}
		if string(fields["schema"]) != fmt.Sprint(session.EventSchemaVersion) || string(fields["extra"]) != "9007199254740993123456789" || !bytes.Contains(fields["journal"], []byte(`"schema":899`)) {
			t.Fatalf("schema %d changed opaque fields: %s", version, migrated)
		}
		event, err := session.UnmarshalEventJSON(raw)
		if err != nil || event.Tool != nil || event.Approval != nil {
			t.Fatalf("schema %d produced retired facts: %+v, %v", version, event, err)
		}
		if version == session.EventSchemaVersion && !bytes.Equal(raw, migrated) {
			t.Fatal("current schema rewrote its input")
		}
		migrated[0] = 'x'
		if raw[0] == 'x' {
			t.Fatal("migration aliases its caller's input")
		}
	}
}

func TestEventEnvelopeRejectsUnsupportedActiveSchemas(t *testing.T) {
	for _, raw := range []string{"null", `{"schema":-1}`, `{"schema":1.5}`, `{"schema":"1"}`, `{"schema":99}`, `{"schema":2,"approval":{"schema":99},"journal":{}}`} {
		var versionErr *session.SchemaVersionError
		if _, err := session.UnmarshalEventJSON([]byte(raw)); !errors.As(err, &versionErr) {
			t.Fatalf("%s: got %v, want SchemaVersionError", raw, err)
		}
	}
}

func TestEventDecoderRequiresCanonicalToolPayload(t *testing.T) {
	for _, kind := range []string{"tool_call", "tool_result"} {
		raw := fmt.Sprintf(`{"schema":2,"type":%q,"journal":{"tool_execution":{"schema":1}}}`, kind)
		if _, err := session.UnmarshalEventJSON([]byte(raw)); !errors.Is(err, session.ErrInvalidEvent) {
			t.Fatalf("%s: expected missing canonical payload error, got %v", kind, err)
		}
	}
}
