package chat

import (
	"encoding/json"
	"io"
	"strings"
)

func mustJSON(value map[string]any) json.RawMessage {
	if value == nil {
		value = map[string]any{}
	}
	raw, _ := json.Marshal(value)
	return raw
}

// mustObject decodes model arguments for observation, not execution; retain
// numeric tokens so a later wire validator sees the value the model supplied.
func mustObject(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		return nil
	}
	// A Decoder accepts a single value even when another value follows it.
	// Keep json.Unmarshal's whole-input validation for observation inputs.
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil
	}
	return out
}
