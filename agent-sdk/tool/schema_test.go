package tool

import (
	"testing"
)

func TestMCPArgumentSchemaValidation(t *testing.T) {
	def := Definition{InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"count"}, "properties": map[string]any{"count": map[string]any{"type": "integer", "minimum": 1}}}}
	if err := ValidateMCPArguments(def, []byte(`{"count":2}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{}`, `{"count":0}`, `{"count":"2"}`, `{"count":2,"extra":true}`, `[]`, `null`} {
		if ValidateMCPArguments(def, []byte(raw)) == nil {
			t.Fatalf("accepted arguments %s", raw)
		}
	}
}

func TestMCPArgumentNumbersPreserveIntegersAndRejectOverflow(t *testing.T) {
	def := Definition{InputSchema: map[string]any{"type": "object", "properties": map[string]any{"number": map[string]any{"type": "integer"}}}}
	for _, raw := range []string{`{"number":9007199254740993}`, `{"number":18446744073709551615}`} {
		if err := ValidateMCPArguments(def, []byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateMCPArguments(def, []byte(`{"number":1e999}`)); err == nil {
		t.Fatal("overflow bypassed schema validation")
	}
}
