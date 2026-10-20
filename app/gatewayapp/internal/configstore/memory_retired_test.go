package configstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRetiredMemoryConfigurationLoadsWithoutValidationOrRewrite(t *testing.T) {
	for _, memory := range []string{`null`, `{"default_binding_ref":"missing","bindings":[{"binding_ref":"duplicate"},{"binding_ref":"duplicate"}]}`, `{"enabled":true,"old_issuer":"missing","future":{"nested":[1,2]}}`, `"legacy-value"`} {
		t.Run(memory, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config.json")
			raw := []byte(`{"schema_version":2,"memory":` + memory + `,"agent_bindings":{"bindings":[{"handle":"steward","profile_id":"missing"},{"handle":"memory-verifier","profile_id":"missing"},{"handle":"steward"}],"sets":[{"name":"old","bindings":[{"handle":"steward"},{"handle":"memory-verifier"}]}]}}`)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			store := New(root)
			doc, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatalf("load rewrote legacy file: %v", err)
			}
			if len(doc.AgentBindings.Bindings) != 0 || len(doc.AgentBindings.Sets) != 1 || len(doc.AgentBindings.Sets[0].Bindings) != 0 {
				t.Fatalf("retired bindings remain active: %#v", doc.AgentBindings)
			}
			detached := Normalize(doc)
			if len(detached.Memory) > 0 {
				detached.Memory[0] = 'X'
				if bytes.Equal(detached.Memory, doc.Memory) {
					t.Fatal("opaque Memory aliases normalized copy")
				}
			}
			doc.Runtime.ApprovalMode = "manual"
			if err := store.Save(doc); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			var before, after any
			if err := json.Unmarshal([]byte(memory), &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(loaded.Memory, &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("opaque Memory changed: %#v != %#v", before, after)
			}
		})
	}
}
