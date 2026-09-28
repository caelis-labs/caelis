package sandbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// UnmarshalJSON keeps a null collection as no overrides, but rejects null
// elements: an individual Set value must be a string, not a deletion marker.
func (cfg *EnvironmentConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		Inherit *bool                      `json:"inherit"`
		Set     map[string]json.RawMessage `json:"set"`
		Unset   []json.RawMessage          `json:"unset"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return fmt.Errorf("sandbox environment: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("sandbox environment: unexpected trailing value")
	}
	out := EnvironmentConfig{Inherit: raw.Inherit}
	if raw.Set != nil {
		out.Set = make(map[string]string, len(raw.Set))
		for key, encoded := range raw.Set {
			if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
				return fmt.Errorf("sandbox environment: set value for %q cannot be null", key)
			}
			var value string
			if err := json.Unmarshal(encoded, &value); err != nil {
				return fmt.Errorf("sandbox environment: set value for %q must be a string: %w", key, err)
			}
			out.Set[key] = value
		}
	}
	if raw.Unset != nil {
		out.Unset = make([]string, 0, len(raw.Unset))
		for _, encoded := range raw.Unset {
			if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
				return fmt.Errorf("sandbox environment: unset name cannot be null")
			}
			var name string
			if err := json.Unmarshal(encoded, &name); err != nil {
				return fmt.Errorf("sandbox environment: unset name must be a string: %w", err)
			}
			out.Unset = append(out.Unset, name)
		}
	}
	*cfg = out
	return nil
}
