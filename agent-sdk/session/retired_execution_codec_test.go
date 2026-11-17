package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestRetiredCodecNestedVersionsAtEveryEventSchema(t *testing.T) {
	for _, eventSchema := range []int{0, 1, 2, 3} {
		for _, journalSchema := range []int{0, 1} {
			for _, kind := range []string{"execution", "tool_execution", "pause_token"} {
				for _, version := range []string{"0", "1", "2", "999", "-1", "1.5", `"1"`, "true"} {
					t.Run(fmt.Sprintf("event=%d/journal=%d/%s=%s", eventSchema, journalSchema, kind, version), func(t *testing.T) {
						raw := json.RawMessage(fmt.Sprintf(`{"schema":%d,"type":"lifecycle","visibility":"journal","journal":{"schema":%d,"%s":{"schema":%s,"sentinel":"keep"}}}`, eventSchema, journalSchema, kind, version))
						migrated, err := MigrateEventJSON(raw)
						if version != "0" && version != "1" {
							var schemaErr *SchemaVersionError
							if !errors.As(err, &schemaErr) {
								t.Fatalf("future/invalid nested version accepted: %s, %v", migrated, err)
							}
							return
						}
						if err != nil {
							t.Fatal(err)
						}
						var event map[string]json.RawMessage
						if err = json.Unmarshal(migrated, &event); err != nil {
							t.Fatal(err)
						}
						var journal map[string]json.RawMessage
						if err = json.Unmarshal(event["journal"], &journal); err != nil {
							t.Fatal(err)
						}
						var record map[string]json.RawMessage
						if err = json.Unmarshal(journal[kind], &record); err != nil {
							t.Fatal(err)
						}
						if string(record["schema"]) != "1" || string(record["sentinel"]) != `"keep"` {
							t.Fatalf("known nested payload changed: %s", journal[kind])
						}
					})
				}
			}
		}
	}
}

func TestRetiredCodecRejectsInvalidOuterVersions(t *testing.T) {
	for _, version := range []string{"999", "-1", "1.5", `"1"`, "true"} {
		for _, outer := range []string{"event", "journal"} {
			t.Run(outer+version, func(t *testing.T) {
				raw := fmt.Sprintf(`{"schema":%s,"journal":{"schema":0,"execution":{"schema":0}}}`, version)
				if outer == "journal" {
					raw = fmt.Sprintf(`{"schema":0,"journal":{"schema":%s,"execution":{"schema":0}}}`, version)
				}
				var schemaErr *SchemaVersionError
				if _, err := MigrateEventJSON([]byte(raw)); !errors.As(err, &schemaErr) {
					t.Fatalf("unsupported %s schema accepted: %v", outer, err)
				}
			})
		}
	}
}
