package client

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDecodeUpdatePreservesUnknownExternalVariant(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"sessionUpdate":"vendor/custom","nested":{"value":42}}`)
	update, err := decodeUpdate(raw)
	if err != nil {
		t.Fatalf("decodeUpdate() error = %v", err)
	}
	unknown, ok := update.(RawUpdate)
	if !ok {
		t.Fatalf("decodeUpdate() = %T, want client.RawUpdate", update)
	}
	if unknown.SessionUpdate != "vendor/custom" || !bytes.Equal(unknown.Raw, raw) {
		t.Fatalf("unknown update = %#v, want exact raw payload", unknown)
	}

	raw[0] = '['
	if unknown.Raw[0] != '{' {
		t.Fatalf("unknown update aliases decoder input: %q", unknown.Raw)
	}
}
