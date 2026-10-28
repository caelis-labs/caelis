// Package experimental defines the Host's opt-in Runtime feature catalog.
package experimental

import (
	"encoding/json"
	"fmt"
)

// Stable identifiers accepted by the Control API.
const (
	AsyncCompact = "async_compact"
	IdleCompact  = "idle_compact"
	// MCPRefresh is accepted and ignored for legacy Control clients.
	// Remove when the minimum supported clients no longer send it.
	MCPRefresh = "mcp_catalog_refresh"
	// ToolRetirement is accepted and ignored for legacy Control clients.
	// Remove when the minimum supported clients no longer send it.
	ToolRetirement = "deferred_tool_retirement"
)

// Values is a sparse set of explicit choices. Absent features use catalog defaults.
type Values map[string]bool

// Definition describes a supported independently configurable feature.
type Definition struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Default     bool   `json:"default"`
}

// Definitions returns a detached, stable-order catalog. No feature implies another.
func Definitions() []Definition {
	return []Definition{
		{ID: AsyncCompact, Name: "Async compact", Description: "Prepare a context checkpoint in the background."},
		{ID: IdleCompact, Name: "Idle compact", Description: "Compact completed context while idle."},
	}
}

// Validate rejects unknown names, including unknown false values.
func Validate(values Values) error {
	for id := range values {
		known := id == MCPRefresh || id == ToolRetirement
		for _, feature := range Definitions() {
			known = known || feature.ID == id
		}
		if !known {
			return fmt.Errorf("unknown experimental feature %q", id)
		}
	}
	return nil
}

// Complete expands persisted choices to every supported feature, including false.
func Complete(values Values) Values {
	out := Values{}
	for _, definition := range Definitions() {
		enabled, present := values[definition.ID]
		if !present {
			enabled = definition.Default
		}
		out[definition.ID] = enabled
	}
	return out
}

// Snapshot separates durable desired settings from an optional live activation.
// Effective is nil when no Session Runtime is active; reads never activate it.
type Snapshot struct {
	Revision  string       `json:"revision"`
	Scope     string       `json:"scope"`
	AppliesAt string       `json:"applies_at"`
	Features  []Definition `json:"features"`
	Desired   Values       `json:"desired"`
	SessionID string       `json:"session_id,omitempty"`
	Effective Values       `json:"effective,omitempty"`
}

// UnmarshalJSON preserves explicit false and rejects null or non-boolean choices.
func (v *Values) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	values := Values{}
	for id, value := range fields {
		var enabled *bool
		if err := json.Unmarshal(value, &enabled); err != nil {
			return fmt.Errorf("experimental feature %q must be a boolean: %w", id, err)
		}
		if enabled == nil {
			return fmt.Errorf("experimental feature %q must be a boolean", id)
		}
		values[id] = *enabled
	}
	*v = values
	return nil
}
