package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAcceptsTypedEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caelis.jsonl")
	raw := `{"schema_version":"caelis.headless/v1","type":"envelope","envelope":{"kind":"caelis/notice","notice":"ready"}}` + "\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validate(path); err != nil {
		t.Fatalf("validate() error = %v", err)
	}
}

func TestValidateRejectsMalformedTypedUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caelis.jsonl")
	raw := `{"schema_version":"caelis.headless/v1","type":"envelope","envelope":{"kind":"session/update","update":{"sessionUpdate":"usage_update","size":"bad","used":"1"}}}` + "\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validate(path); err == nil {
		t.Fatal("validate() error = nil, want malformed usage error")
	}
}
