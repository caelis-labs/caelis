package modelcatalog

// model_catalog_remote.go
//
// Dynamic model capability catalog:
//   - Local override file (~/.agents/model_capabilities.json) may override known models.
//   - Remote live data and the embedded snapshot are fallback capability sources
//     for custom model names that are not maintained in builtinCatalog.
//
// Call InitModelCatalog when you need to refresh dynamic catalog data
// (for example from /connect). Static model lists do not use this data.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	_ "embed"
)

//go:embed models_dev_snapshot.json
var embeddedCatalogSnapshot []byte

const (
	// modelsDevURL is the public models.dev API endpoint.
	modelsDevURL = "https://models.dev/api.json"

	// catalogFetchTimeout is the HTTP timeout for the remote fetch.
	catalogFetchTimeout = 5 * time.Second
)

// modelsDevURLOverride can be set in tests to redirect the HTTP fetch.
// An empty string means the canonical modelsDevURL is used.
var modelsDevURLOverride string

// resolvedModelsDevURL returns the effective URL (override if set, else canonical).
func resolvedModelsDevURL() string {
	if modelsDevURLOverride != "" {
		return modelsDevURLOverride
	}
	return modelsDevURL
}

// ---------------------------------------------------------------------------
// Compact JSON format used by both the snapshot file and the local override
// ---------------------------------------------------------------------------

// capEntry is the per-model JSON record stored in the snapshot / override file.
// Keys in the map are "provider:model_prefix" (lower-case, e.g. "openai:gpt-4o").
type capEntry struct {
	ContextWindow          int      `json:"context_window"`
	MaxOutput              int      `json:"max_output"`
	DefaultMaxOutput       int      `json:"default_max_output,omitempty"` // 0 → heuristic applied later
	ToolCalls              bool     `json:"tool_calls"`
	Reasoning              bool     `json:"reasoning"`
	ReasoningMode          string   `json:"reasoning_mode,omitempty"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	Images                 bool     `json:"images"`
	JSONOutput             bool     `json:"json_output"`
}

// capSnapshot is the in-memory representation: map["provider:model"] → capEntry.
type capSnapshot map[string]capEntry

// ---------------------------------------------------------------------------
// Package-level global state
// ---------------------------------------------------------------------------

var (
	dynamicMu       sync.RWMutex
	remoteCatalog   capSnapshot                                   // loaded from models.dev
	embeddedCatalog = parseSnapshotBytes(embeddedCatalogSnapshot) // loaded from embedded snapshot
	localOverrides  capSnapshot                                   // loaded from the user's override file
)

// CatalogInitStatus describes the result of one dynamic catalog refresh.
type CatalogInitStatus struct {
	RemoteFetched bool
	RemoteError   error
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

// InitModelCatalog initialises or refreshes the dynamic capability catalog.
//
//   - client: an *http.Client to use for the remote fetch; nil uses http.DefaultClient.
//   - overridePath: path to a JSON override file (e.g. ~/.agents/model_capabilities.json).
//     Pass "" to skip loading local overrides.
//
// Errors during remote fetch or override loading are logged but never returned;
// the catalog always falls back gracefully to the embedded snapshot.
func InitModelCatalog(ctx context.Context, client *http.Client, overridePath string) {
	_ = InitModelCatalogWithStatus(ctx, client, overridePath)
}

// InitModelCatalogWithStatus initialises dynamic catalog data and returns
// whether the remote fetch succeeded.
func InitModelCatalogWithStatus(ctx context.Context, client *http.Client, overridePath string) CatalogInitStatus {
	if client == nil {
		client = &http.Client{Timeout: catalogFetchTimeout}
	} else if client.Timeout == 0 {
		// Ensure a timeout even if the caller's client has none.
		client = &http.Client{
			Transport: client.Transport,
			Timeout:   catalogFetchTimeout,
		}
	}

	embedded := parseSnapshotBytes(embeddedCatalogSnapshot)

	// 1. Try remote fetch.
	remote, err := fetchModelsDev(ctx, client)
	remoteFetched := err == nil && len(remote) > 0
	if err != nil {
		remote = nil
	}

	dynamicMu.Lock()
	remoteCatalog = remote
	embeddedCatalog = embedded
	dynamicMu.Unlock()

	// 3. Load local overrides (non-fatal if the file is missing).
	if overridePath != "" {
		if ov, err := loadOverrideFile(overridePath); err == nil {
			dynamicMu.Lock()
			localOverrides = ov
			dynamicMu.Unlock()
		} else if !errors.Is(err, os.ErrNotExist) {
			log.Printf("[model-catalog] override file %q: %v", overridePath, err)
		}
	}
	return CatalogInitStatus{
		RemoteFetched: remoteFetched,
		RemoteError:   err,
	}
}

// ---------------------------------------------------------------------------
// Internal lookup used by LookupModelCapabilities
// ---------------------------------------------------------------------------

// lookupDynamic searches local/remote/embedded catalogs in priority order.
// Returns (caps, true) if found, otherwise (zero, false).
func lookupDynamic(provider, modelName string) (ModelCapabilities, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	if provider == "" || modelName == "" {
		return ModelCapabilities{}, false
	}

	dynamicMu.RLock()
	local := localOverrides
	remote := remoteCatalog
	embedded := embeddedCatalog
	dynamicMu.RUnlock()

	// Local overrides take priority.
	if caps, ok := searchCapSnapshot(local, provider, modelName); ok {
		return caps, true
	}
	// Then remote/snapshot.
	if caps, ok := searchCapSnapshot(remote, provider, modelName); ok {
		return caps, true
	}
	// Then embedded snapshot.
	if caps, ok := searchCapSnapshot(embedded, provider, modelName); ok {
		return caps, true
	}
	return ModelCapabilities{}, false
}

func lookupLocalOverride(provider, modelName string) (ModelCapabilities, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	if provider == "" || modelName == "" {
		return ModelCapabilities{}, false
	}

	dynamicMu.RLock()
	local := localOverrides
	dynamicMu.RUnlock()

	return searchCapSnapshot(local, provider, modelName)
}

func lookupRemoteOrEmbedded(provider, modelName string) (ModelCapabilities, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	if provider == "" || modelName == "" {
		return ModelCapabilities{}, false
	}

	dynamicMu.RLock()
	remote := remoteCatalog
	embedded := embeddedCatalog
	dynamicMu.RUnlock()

	if caps, ok := searchCapSnapshot(remote, provider, modelName); ok {
		return caps, true
	}
	return searchCapSnapshot(embedded, provider, modelName)
}

// LookupDynamicModelCapabilities searches only local/remote/embedded catalogs.
// It does not fall back to builtin hard-coded catalog.
func LookupDynamicModelCapabilities(provider, modelName string) (ModelCapabilities, bool) {
	return lookupDynamic(provider, modelName)
}

// searchCapSnapshot performs a longest-prefix match inside a capSnapshot.
// Keys in the snapshot are "provider:model_prefix" (lower-case).
func searchCapSnapshot(snap capSnapshot, provider, modelName string) (ModelCapabilities, bool) {
	if len(snap) == 0 {
		return ModelCapabilities{}, false
	}
	var bestKey string
	bestLen := 0

	for k := range snap {
		kprov, kmodel, ok := splitCatalogKey(k)
		if !ok {
			continue
		}
		if !providerMatches(provider, kprov) {
			continue
		}
		if modelName != kmodel && !strings.HasPrefix(modelName, kmodel) {
			continue
		}
		if len(kmodel) > bestLen {
			bestLen = len(kmodel)
			bestKey = k
		}
	}
	if bestKey == "" {
		return ModelCapabilities{}, false
	}
	return entryToCaps(snap[bestKey]), true
}

// splitCatalogKey splits "provider:model" → ("provider", "model", true).
func splitCatalogKey(key string) (provider, model string, ok bool) {
	idx := strings.Index(key, ":")
	if idx <= 0 || idx == len(key)-1 {
		return "", "", false
	}
	return key[:idx], key[idx+1:], true
}

// providerMatches checks if a catalog key's provider matches the query.
// The catalog may use alternate provider IDs (e.g. "google" for our "gemini").
func providerMatches(queryProvider, keyProvider string) bool {
	if queryProvider == keyProvider {
		return true
	}
	// Handle models.dev → internal provider alias mappings.
	if alias, ok := modelsDevProviderAlias[keyProvider]; ok && alias == queryProvider {
		return true
	}
	// Substring fallback for hyphenated variants (e.g. "openai-compatible" contains "openai").
	return strings.Contains(queryProvider, keyProvider)
}

// entryToCaps converts a capEntry to ModelCapabilities, applying a
// DefaultMaxOutput heuristic when the override file omits that field.
func entryToCaps(e capEntry) ModelCapabilities {
	caps := ModelCapabilities{
		ContextWindowTokens:    e.ContextWindow,
		MaxOutputTokens:        e.MaxOutput,
		DefaultMaxOutputTokens: e.DefaultMaxOutput,
		SupportsToolCalls:      e.ToolCalls,
		SupportsReasoning:      e.Reasoning,
		ReasoningMode:          e.ReasoningMode,
		ReasoningEfforts:       normalizeReasoningEffortList(e.ReasoningEfforts),
		DefaultReasoningEffort: NormalizeReasoningEffort(e.DefaultReasoningEffort),
		SupportsImages:         e.Images,
		SupportsJSONOutput:     e.JSONOutput,
	}
	normalizeModelCapabilitiesReasoning(&caps)
	if caps.DefaultMaxOutputTokens <= 0 {
		caps.DefaultMaxOutputTokens = defaultMaxOutputHeuristic(caps.MaxOutputTokens, caps.ContextWindowTokens, caps.SupportsReasoning)
	}
	return caps
}

// defaultMaxOutputHeuristic applies a conservative default when
// DefaultMaxOutputTokens is not explicitly specified in the source data.
func defaultMaxOutputHeuristic(maxOut int, contextWindow int, reasoning bool) int {
	if maxOut <= 0 {
		return conservativeDefaultMaxOutputTokens(contextWindow, reasoning)
	}
	base := maxOut
	if reasoning {
		// Reasoning models often need more output headroom.
		if base >= 32768 {
			base = 32768
		}
		return capSuggestedDefaultMaxOutput(contextWindow, base, true)
	}
	if base > 8192 {
		base = 8192
	}
	return capSuggestedDefaultMaxOutput(contextWindow, base, false)
}

// ---------------------------------------------------------------------------
// models.dev provider-ID → internal provider name mapping
// ---------------------------------------------------------------------------

// modelsDevProviderAlias maps models.dev provider IDs to our internal names
// where they differ.
var modelsDevProviderAlias = map[string]string{
	"google": "gemini",
}

// ---------------------------------------------------------------------------
// Remote fetch
// ---------------------------------------------------------------------------

// modelsDevProvider is one provider object from the models.dev API response.
type modelsDevProvider struct {
	ID     string                    `json:"id"`
	Models map[string]modelsDevModel `json:"models"`
}

// modelsDevModel is one model record from the models.dev API response.
type modelsDevModel struct {
	ID               string              `json:"id"`
	Reasoning        bool                `json:"reasoning"`
	ToolCall         bool                `json:"tool_call"`
	Attachment       bool                `json:"attachment"` // image input
	StructuredOutput bool                `json:"structured_output"`
	Limit            modelsDevModelLimit `json:"limit"`
}

// modelsDevModelLimit holds token limits from models.dev.
type modelsDevModelLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

// fetchModelsDev downloads and parses https://models.dev/api.json into our
// internal capSnapshot format.
func fetchModelsDev(ctx context.Context, client *http.Client) (capSnapshot, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, catalogFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, resolvedModelsDevURL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "caelis/model-catalog-updater")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Guard against accidentally huge responses.
	const maxBytes = 32 * 1024 * 1024 // 32 MiB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, &httpStatusError{code: resp.StatusCode}
	}

	return parseModelsDevJSON(body)
}

type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string {
	return "models.dev: HTTP " + http.StatusText(e.code)
}

// parseModelsDevJSON parses the models.dev API JSON into a capSnapshot.
// The outer structure is map[providerID]modelsDevProvider.
func parseModelsDevJSON(data []byte) (capSnapshot, error) {
	var raw map[string]modelsDevProvider
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	snap := make(capSnapshot, len(raw)*8)
	providerIDs := make([]string, 0, len(raw))
	for providerID := range raw {
		providerIDs = append(providerIDs, providerID)
	}
	sort.Strings(providerIDs)
	for _, providerID := range providerIDs {
		prov := raw[providerID]
		if len(prov.Models) == 0 {
			continue
		}
		// Map to our internal provider name.
		internalProvider := providerID
		if alias, ok := modelsDevProviderAlias[providerID]; ok {
			internalProvider = alias
		}

		modelIDs := make([]string, 0, len(prov.Models))
		for modelID := range prov.Models {
			modelIDs = append(modelIDs, modelID)
		}
		sort.Strings(modelIDs)
		for _, modelID := range modelIDs {
			m := prov.Models[modelID]
			if m.Limit.Context <= 0 && m.Limit.Output <= 0 {
				continue // skip incomplete entries
			}
			entry := capEntry{
				ContextWindow: m.Limit.Context,
				MaxOutput:     m.Limit.Output,
				// DefaultMaxOutput left 0; heuristic applied in entryToCaps.
				ToolCalls:  m.ToolCall,
				Reasoning:  m.Reasoning,
				Images:     m.Attachment,
				JSONOutput: m.StructuredOutput,
			}
			insertCapEntry(snap, internalProvider+":"+strings.ToLower(modelID), entry)
			if derivedProvider, derivedModel, ok := splitVendorModelID(modelID); ok {
				insertCapEntry(snap, derivedProvider+":"+derivedModel, entry)
			}
		}
	}
	return snap, nil
}

func insertCapEntry(snap capSnapshot, key string, entry capEntry) {
	if snap == nil || strings.TrimSpace(key) == "" {
		return
	}
	if existing, ok := snap[key]; ok {
		snap[key] = mergeCapEntry(existing, entry)
		return
	}
	snap[key] = entry
}

func mergeCapEntry(existing, next capEntry) capEntry {
	if next.ContextWindow > existing.ContextWindow {
		existing.ContextWindow = next.ContextWindow
	}
	if next.MaxOutput > existing.MaxOutput {
		existing.MaxOutput = next.MaxOutput
	}
	existing.ToolCalls = existing.ToolCalls || next.ToolCalls
	existing.Reasoning = existing.Reasoning || next.Reasoning
	existing.Images = existing.Images || next.Images
	existing.JSONOutput = existing.JSONOutput || next.JSONOutput
	return existing
}

func splitVendorModelID(modelID string) (provider string, model string, ok bool) {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(modelID)), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// ---------------------------------------------------------------------------
// Snapshot / override file loading
// ---------------------------------------------------------------------------

// parseSnapshotBytes parses the embedded JSON snapshot into a capSnapshot.
// Errors are swallowed (the embedded data is trusted).
func parseSnapshotBytes(data []byte) capSnapshot {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	looksCompact := false
	for key := range raw {
		if key == "_comment" {
			continue
		}
		if strings.Contains(key, ":") {
			looksCompact = true
			break
		}
	}
	if !looksCompact {
		if parsed, err := parseModelsDevJSON(data); err == nil {
			return parsed
		}
	}

	snap := make(capSnapshot, len(raw))
	for k, v := range raw {
		if k == "_comment" {
			continue
		}
		var entry capEntry
		if err := json.Unmarshal(v, &entry); err != nil {
			continue
		}
		snap[strings.ToLower(k)] = entry
	}
	return snap
}

// loadOverrideFile reads and parses a local capability override file.
// The file format is the same as models_dev_snapshot.json.
func loadOverrideFile(path string) (capSnapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	snap := parseSnapshotBytes(raw)
	if snap == nil {
		return nil, errors.New("model-catalog: failed to parse override file")
	}
	return snap, nil
}
