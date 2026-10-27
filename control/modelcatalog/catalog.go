package modelcatalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed catalog.json
var catalogJSON []byte

type catalogEntry struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Recommended *bool  `json:"recommended,omitempty"`
	ModelCapabilities
}

var catalog = loadCatalog()

func loadCatalog() []catalogEntry {
	var entries []catalogEntry
	decoder := json.NewDecoder(bytes.NewReader(catalogJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entries); err != nil {
		panic(err)
	}
	for i := range entries {
		normalizeModelCapabilitiesReasoning(&entries[i].ModelCapabilities)
	}
	return entries
}

// LookupModelCapabilities resolves only the maintained native connection's
// namespace. Gateway and custom endpoints never inherit an upstream catalog.
// The longest model prefix wins, allowing dated IDs within a maintained line.
func LookupModelCapabilities(provider, name string) (ModelCapabilities, bool) {
	provider, name = strings.ToLower(strings.TrimSpace(provider)), strings.ToLower(strings.TrimSpace(name))
	var best *catalogEntry
	for i := range catalog {
		e := &catalog[i]
		if e.Provider == provider && strings.HasPrefix(name, e.Model) && (best == nil || len(e.Model) > len(best.Model)) {
			best = e
		}
	}
	if best == nil {
		return DefaultModelCapabilities(), false
	}
	caps := best.ModelCapabilities
	caps.ReasoningEfforts = append([]string(nil), caps.ReasoningEfforts...)
	caps.SpeedModes = append([]SpeedMode(nil), caps.SpeedModes...)
	return caps, true
}

// ListRecommendedModels returns the maintained connection's models in file
// order. Updating catalog.json updates recommendations and capabilities together.
func ListRecommendedModels(provider string) []string {
	var models []string
	for _, e := range catalog {
		if e.Provider == strings.ToLower(strings.TrimSpace(provider)) && (e.Recommended == nil || *e.Recommended) {
			models = append(models, e.Model)
		}
	}
	return models
}

// ReasoningLevelsForModel returns the maintained model's selectable controls.
func ReasoningLevelsForModel(provider, name string) []string {
	caps, ok := LookupModelCapabilities(provider, name)
	if !ok {
		return nil
	}
	return reasoningLevelsFromCapabilities(caps)
}

// ReasoningModeForModel returns none when the connection declares no controls.
func ReasoningModeForModel(provider, name string) string {
	caps, _ := LookupModelCapabilities(provider, name)
	return caps.ReasoningMode
}

// DefaultReasoningEffortForModel returns the maintained model's default effort.
func DefaultReasoningEffortForModel(provider, name string) string {
	caps, _ := LookupModelCapabilities(provider, name)
	return caps.DefaultReasoningEffort
}

// SpeedModesForModel returns a detached copy of declared request-speed modes.
func SpeedModesForModel(provider, name string) []SpeedMode {
	caps, _ := LookupModelCapabilities(provider, name)
	return caps.SpeedModes
}
