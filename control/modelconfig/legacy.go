package modelconfig

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"io"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/control/modelcatalog"
)

// These frozen records are Control's pre-pinned configuration reader. They
// cannot populate connection menus. Retained input IDs use these same records
// through the ordinary assembler, without recreating a selectable directory.
// Remove them when the minimum supported upgrade source persists explicit API,
// authentication and model capabilities (the first release shipping this reader).
//
//go:embed legacy_capabilities.json.gz
var legacyCapabilitiesJSON []byte

//go:embed legacy_providers.json
var legacyProvidersJSON []byte

var legacyProviders = func() map[string]ProviderTemplate {
	var entries map[string]ProviderTemplate
	if err := json.Unmarshal(legacyProvidersJSON, &entries); err != nil {
		panic(err)
	}
	return entries
}()

var legacyCapabilities = sync.OnceValue(func() map[string]modelcatalog.ModelCapabilities {
	r, err := gzip.NewReader(bytes.NewReader(legacyCapabilitiesJSON))
	if err != nil {
		panic(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		panic(err)
	}
	var result map[string]modelcatalog.ModelCapabilities
	if err := json.Unmarshal(data, &result); err != nil {
		panic(err)
	}

	return result
})

// RestoreSavedCapabilities fills only omitted pre-pinned model fields. Callers
// must hydrate the saved provider endpoint first. IDs and credential references
// are retained. Maintained native and gateway recommendations do not use this data.
func RestoreSavedCapabilities(cfg Config) Config {
	if _, ok := legacyProviders[cfg.Provider]; !ok {
		return cfg
	}
	if cfg.ImageInput != nil && cfg.ReasoningMode != "" && cfg.ContextWindowTokens > 0 && ((cfg.Provider != "openai" && cfg.Provider != "openai-codex") || cfg.SpeedModes != nil) {
		return cfg
	}
	caps, ok := legacyModelCapabilities(cfg.Provider, cfg.BaseURL, cfg.Model)
	if !ok {
		return cfg
	}
	if cfg.ContextWindowTokens <= 0 {
		cfg.ContextWindowTokens = caps.ContextWindowTokens
	}
	// Older writers omitted 4096 because it was the configured default.
	if cfg.MaxOutputTok <= 0 {
		cfg.MaxOutputTok = 4096
	}
	if cfg.ImageInput == nil {
		cfg.ImageInput = boolPointer(caps.SupportsImages)
	}
	if cfg.ReasoningMode == "" {
		cfg.ReasoningMode = caps.ReasoningMode
		if cfg.ReasoningLevels == nil {
			cfg.ReasoningLevels = append([]string(nil), caps.ReasoningEfforts...)
		}
	}
	if cfg.DefaultReasoningEffort == "" && cfg.ReasoningMode != modelcatalog.ReasoningModeNone {
		cfg.DefaultReasoningEffort = firstNonEmpty(cfg.ReasoningEffort, caps.DefaultReasoningEffort)
	}
	if cfg.SpeedModes == nil {
		cfg.SpeedModes = append([]modelcatalog.SpeedMode(nil), caps.SpeedModes...)
	}
	return cfg
}

func legacyModelCapabilities(provider, baseURL, name string) (modelcatalog.ModelCapabilities, bool) {
	if _, ok := legacyProviders[provider]; !ok {
		return modelcatalog.ModelCapabilities{}, false
	}
	if provider == "ollama" && NormalizeBaseURL(baseURL) != "https://ollama.com" {
		return modelcatalog.ModelCapabilities{}, false
	}
	if provider == "ollama" && NormalizeBaseURL(baseURL) == "https://ollama.com" {
		provider = "ollama-cloud"
	}
	key := strings.ToLower(provider + ":" + name)
	var caps modelcatalog.ModelCapabilities
	best := 0
	for pattern, entry := range legacyCapabilities() {
		if strings.HasPrefix(key, pattern) && len(pattern) > best {
			caps, best = entry, len(pattern)
		}
	}
	caps.ReasoningEfforts = append([]string(nil), caps.ReasoningEfforts...)
	caps.SpeedModes = append([]modelcatalog.SpeedMode(nil), caps.SpeedModes...)
	return caps, best > 0
}
