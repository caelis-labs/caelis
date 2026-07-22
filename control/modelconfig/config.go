// Package modelconfig owns Caelis provider endpoints, configured model records,
// onboarding templates, and construction of SDK model implementations.
package modelconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/control/modelcatalog"
)

// Config is the complete Control-owned configuration for one selectable model.
type Config struct {
	ID       string `json:"id,omitempty"`
	Alias    string `json:"alias,omitempty"`
	Provider string `json:"provider,omitempty"`
	// ProviderEndpointID references the provider endpoint infrastructure used
	// by this configured model. It is not a product ModelProfile ID.
	ProviderEndpointID string            `json:"provider_endpoint_id,omitempty"`
	EndpointID         string            `json:"endpoint_id,omitempty"`
	API                providers.APIType `json:"api,omitempty"`
	Model              string            `json:"model,omitempty"`
	BaseURL            string            `json:"base_url,omitempty"`
	HTTPClient         *http.Client      `json:"-"`
	Token              string            `json:"token,omitempty"`
	TokenEnv           string            `json:"token_env,omitempty"`
	// CredentialRef identifies a Control-owned credential without persisting
	// the credential material in the model profile.
	CredentialRef string `json:"credential_ref,omitempty"`

	PersistToken            bool               `json:"persist_token,omitempty"`
	AuthType                providers.AuthType `json:"auth_type,omitempty"`
	HeaderKey               string             `json:"header_key,omitempty"`
	ContextWindowTokens     int                `json:"context_window_tokens,omitempty"`
	ReasoningEffort         string             `json:"reasoning_effort,omitempty"`
	DefaultReasoningEffort  string             `json:"default_reasoning_effort,omitempty"`
	ReasoningLevels         []string           `json:"reasoning_levels,omitempty"`
	ReasoningMode           string             `json:"reasoning_mode,omitempty"`
	MaxOutputTok            int                `json:"max_output_tokens,omitempty"`
	Timeout                 time.Duration      `json:"timeout,omitempty"`
	StreamFirstEventTimeout time.Duration      `json:"stream_first_event_timeout,omitempty"`
}

// ProviderEndpointConfig stores endpoint and credential data shared by
// configured provider models. It is infrastructure configuration, not a
// product-selectable modelprofile.ModelProfile.
type ProviderEndpointConfig struct {
	ID                      string             `json:"id,omitempty"`
	Provider                string             `json:"provider,omitempty"`
	EndpointID              string             `json:"endpoint_id,omitempty"`
	API                     providers.APIType  `json:"api,omitempty"`
	BaseURL                 string             `json:"base_url,omitempty"`
	HTTPClient              *http.Client       `json:"-"`
	Token                   string             `json:"token,omitempty"`
	TokenEnv                string             `json:"token_env,omitempty"`
	CredentialRef           string             `json:"credential_ref,omitempty"`
	PersistToken            bool               `json:"persist_token,omitempty"`
	AuthType                providers.AuthType `json:"auth_type,omitempty"`
	HeaderKey               string             `json:"header_key,omitempty"`
	Timeout                 time.Duration      `json:"timeout,omitempty"`
	StreamFirstEventTimeout time.Duration      `json:"stream_first_event_timeout,omitempty"`
}

// Choice is the presentation-neutral identity of one configured model.
type Choice struct {
	ID                 string
	Alias              string
	Provider           string
	Model              string
	ProviderEndpointID string
	EndpointID         string
	BaseURL            string
	Detail             string
}

// NormalizeConfig canonicalizes identifiers, endpoint defaults, credentials,
// and model limits.
func NormalizeConfig(cfg Config) Config {
	cfg.ID = strings.ToLower(strings.TrimSpace(cfg.ID))
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.CredentialRef = strings.ToLower(strings.TrimSpace(cfg.CredentialRef))
	cfg.EndpointID = NormalizeEndpointID(cfg.Provider, cfg.EndpointID, cfg.BaseURL, cfg.API)
	cfg.ProviderEndpointID = strings.ToLower(strings.TrimSpace(cfg.ProviderEndpointID))
	if cfg.ProviderEndpointID == "" {
		cfg.ProviderEndpointID = BuildProviderEndpointID(cfg.Provider, cfg.EndpointID, cfg.BaseURL)
	}
	cfg.Alias = strings.ToLower(strings.TrimSpace(cfg.Alias))
	if cfg.Alias == "" {
		cfg.Alias = BuildAlias(cfg.Provider, cfg.Model)
	}
	if id := BuildModelID(cfg.ProviderEndpointID, cfg.Alias); id != "" {
		cfg.ID = id
	}
	if cfg.API == "" {
		cfg.API = DefaultAPIForProvider(cfg.Provider)
	}
	if cfg.AuthType == "" {
		cfg.AuthType = DefaultAuthTypeForProvider(cfg.Provider)
	}
	if cfg.DefaultReasoningEffort == "" && cfg.ReasoningEffort != "" {
		cfg.DefaultReasoningEffort = cfg.ReasoningEffort
	}
	if cfg.ReasoningMode == "" && cfg.Provider != "" && cfg.Model != "" {
		cfg.ReasoningMode = modelcatalog.ReasoningModeForModel(cfg.Provider, cfg.Model)
	}
	if cfg.MaxOutputTok <= 0 {
		cfg.MaxOutputTok = 4096
	}
	if cfg.ContextWindowTokens < 0 {
		cfg.ContextWindowTokens = 0
	}
	cfg.ReasoningLevels = DedupeNonEmptyStrings(cfg.ReasoningLevels)
	if cfg.Token == "" && strings.TrimSpace(cfg.TokenEnv) != "" {
		cfg.Token = strings.TrimSpace(os.Getenv(strings.TrimSpace(cfg.TokenEnv)))
	}
	return cfg
}

// NormalizeProviderEndpoint canonicalizes one shared provider endpoint.
func NormalizeProviderEndpoint(endpoint ProviderEndpointConfig) ProviderEndpointConfig {
	endpoint.ID = strings.ToLower(strings.TrimSpace(endpoint.ID))
	endpoint.Provider = strings.ToLower(strings.TrimSpace(endpoint.Provider))
	endpoint.CredentialRef = strings.ToLower(strings.TrimSpace(endpoint.CredentialRef))
	endpoint.EndpointID = NormalizeEndpointID(endpoint.Provider, endpoint.EndpointID, endpoint.BaseURL, endpoint.API)
	if endpoint.ID == "" {
		endpoint.ID = BuildProviderEndpointID(endpoint.Provider, endpoint.EndpointID, endpoint.BaseURL)
	}
	if endpoint.API == "" {
		endpoint.API = DefaultAPIForProvider(endpoint.Provider)
	}
	if endpoint.AuthType == "" {
		endpoint.AuthType = DefaultAuthTypeForProvider(endpoint.Provider)
	}
	if endpoint.Token == "" && strings.TrimSpace(endpoint.TokenEnv) != "" {
		endpoint.Token = strings.TrimSpace(os.Getenv(strings.TrimSpace(endpoint.TokenEnv)))
	}
	return endpoint
}

// ProviderEndpointFromConfig extracts shared endpoint and authentication fields.
func ProviderEndpointFromConfig(cfg Config) ProviderEndpointConfig {
	cfg = NormalizeConfig(cfg)
	return NormalizeProviderEndpoint(ProviderEndpointConfig{
		ID:                      cfg.ProviderEndpointID,
		Provider:                cfg.Provider,
		EndpointID:              cfg.EndpointID,
		API:                     cfg.API,
		BaseURL:                 cfg.BaseURL,
		HTTPClient:              cfg.HTTPClient,
		Token:                   cfg.Token,
		TokenEnv:                cfg.TokenEnv,
		CredentialRef:           cfg.CredentialRef,
		PersistToken:            cfg.PersistToken,
		AuthType:                cfg.AuthType,
		HeaderKey:               cfg.HeaderKey,
		Timeout:                 cfg.Timeout,
		StreamFirstEventTimeout: cfg.StreamFirstEventTimeout,
	})
}

// ApplyConfigProviderEndpointFields overlays endpoint-owned values that are
// explicitly present in cfg onto one existing shared provider endpoint.
// Omitted values retain the current endpoint truth, so adding a model cannot
// accidentally clear credentials or connection settings.
func ApplyConfigProviderEndpointFields(current ProviderEndpointConfig, cfg Config) ProviderEndpointConfig {
	raw := cfg
	candidate := ProviderEndpointFromConfig(cfg)
	current = NormalizeProviderEndpoint(current)
	if current.ID == "" {
		current.ID = candidate.ID
	}
	if strings.TrimSpace(raw.Provider) != "" {
		current.Provider = candidate.Provider
	}
	if strings.TrimSpace(raw.EndpointID) != "" {
		current.EndpointID = candidate.EndpointID
	}
	if raw.API != "" {
		current.API = candidate.API
	}
	if strings.TrimSpace(raw.BaseURL) != "" {
		current.BaseURL = candidate.BaseURL
	}
	if raw.HTTPClient != nil {
		current.HTTPClient = raw.HTTPClient
	}
	if strings.TrimSpace(raw.Token) != "" {
		current.Token = candidate.Token
		current.TokenEnv = ""
	}
	if strings.TrimSpace(raw.TokenEnv) != "" {
		current.Token = candidate.Token
		current.TokenEnv = candidate.TokenEnv
	}
	if strings.TrimSpace(raw.CredentialRef) != "" {
		current.CredentialRef = candidate.CredentialRef
		current.Token = ""
		current.TokenEnv = ""
		current.PersistToken = false
	}
	if raw.PersistToken {
		current.PersistToken = true
	}
	if raw.AuthType != "" {
		current.AuthType = candidate.AuthType
	}
	if strings.TrimSpace(raw.HeaderKey) != "" {
		current.HeaderKey = candidate.HeaderKey
	}
	if raw.Timeout > 0 {
		current.Timeout = raw.Timeout
	}
	if raw.StreamFirstEventTimeout > 0 {
		current.StreamFirstEventTimeout = raw.StreamFirstEventTimeout
	}
	return NormalizeProviderEndpoint(current)
}

// ConfigCarriesProviderEndpointFields reports whether cfg contains any
// provider-endpoint-owned data.
func ConfigCarriesProviderEndpointFields(cfg Config) bool {
	return strings.TrimSpace(cfg.Provider) != "" ||
		strings.TrimSpace(cfg.EndpointID) != "" ||
		strings.TrimSpace(cfg.BaseURL) != "" ||
		strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.TokenEnv) != "" ||
		strings.TrimSpace(cfg.CredentialRef) != "" ||
		strings.TrimSpace(cfg.HeaderKey) != "" ||
		cfg.HTTPClient != nil ||
		cfg.API != "" ||
		cfg.AuthType != "" ||
		cfg.Timeout > 0 ||
		cfg.StreamFirstEventTimeout > 0
}

// ConfigCarriesProviderEndpointAuth reports whether cfg can update stored
// provider endpoint credentials.
func ConfigCarriesProviderEndpointAuth(cfg Config) bool {
	return strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.TokenEnv) != "" ||
		strings.TrimSpace(cfg.CredentialRef) != "" ||
		strings.TrimSpace(cfg.HeaderKey) != "" ||
		cfg.PersistToken ||
		cfg.HTTPClient != nil
}

// MergeConfigProviderEndpoint hydrates a model record from its provider endpoint.
func MergeConfigProviderEndpoint(cfg Config, endpoint ProviderEndpointConfig) Config {
	cfg = NormalizeConfig(cfg)
	endpoint = NormalizeProviderEndpoint(endpoint)
	cfg.ProviderEndpointID = endpoint.ID
	cfg.Provider = firstNonEmpty(endpoint.Provider, cfg.Provider)
	cfg.EndpointID = endpoint.EndpointID
	cfg.API = FirstNonEmptyAPI(endpoint.API, cfg.API)
	cfg.BaseURL = firstNonEmpty(endpoint.BaseURL, cfg.BaseURL)
	cfg.HTTPClient = FirstNonNilHTTPClient(endpoint.HTTPClient, cfg.HTTPClient)
	cfg.Token = firstNonEmpty(endpoint.Token, cfg.Token)
	cfg.TokenEnv = firstNonEmpty(endpoint.TokenEnv, cfg.TokenEnv)
	cfg.CredentialRef = firstNonEmpty(endpoint.CredentialRef, cfg.CredentialRef)
	cfg.PersistToken = endpoint.PersistToken || cfg.PersistToken
	cfg.AuthType = FirstNonEmptyAuthType(endpoint.AuthType, cfg.AuthType)
	cfg.HeaderKey = firstNonEmpty(endpoint.HeaderKey, cfg.HeaderKey)
	if endpoint.Timeout > 0 {
		cfg.Timeout = endpoint.Timeout
	}
	if endpoint.StreamFirstEventTimeout > 0 {
		cfg.StreamFirstEventTimeout = endpoint.StreamFirstEventTimeout
	}
	return NormalizeConfig(cfg)
}

// SupportsReasoningEffort reports whether cfg accepts an effort value.
func SupportsReasoningEffort(cfg Config, effort string) bool {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" || effort == "none" {
		return true
	}
	for _, level := range cfg.ReasoningLevels {
		if strings.EqualFold(strings.TrimSpace(level), effort) {
			return true
		}
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.ReasoningMode))
	switch mode {
	case "toggle":
		return effort == "none" || effort == "high" || effort == "max" || effort == "enabled"
	case "fixed":
		return effort == "low" || effort == "medium" || effort == "high"
	case "":
		return true
	default:
		return false
	}
}

// DefaultAPIForProvider returns the maintained protocol dialect for a provider.
func DefaultAPIForProvider(provider string) providers.APIType {
	if template, ok := LookupProvider(provider); ok {
		return template.API
	}
	return ""
}

// SanitizePersistedConfig removes profile-owned and derivable runtime fields.
func SanitizePersistedConfig(cfg Config) Config {
	cfg = NormalizeConfig(cfg)
	if cfg.ReasoningMode == modelcatalog.ReasoningModeForModel(cfg.Provider, cfg.Model) {
		cfg.ReasoningMode = ""
	}
	if cfg.ProviderEndpointID != "" {
		cfg.Provider = ""
		cfg.EndpointID = ""
		cfg.API = ""
		cfg.BaseURL = ""
		cfg.HTTPClient = nil
		cfg.Token = ""
		cfg.TokenEnv = ""
		cfg.CredentialRef = ""
		cfg.PersistToken = false
		cfg.AuthType = ""
		cfg.HeaderKey = ""
		cfg.Timeout = 0
		cfg.StreamFirstEventTimeout = 0
	}
	if !cfg.PersistToken {
		cfg.Token = ""
	}
	if cfg.API == DefaultAPIForProvider(cfg.Provider) {
		cfg.API = ""
	}
	if cfg.AuthType == DefaultAuthTypeForProvider(cfg.Provider) {
		cfg.AuthType = ""
	}
	if cfg.DefaultReasoningEffort == cfg.ReasoningEffort {
		cfg.DefaultReasoningEffort = ""
	}
	if cfg.MaxOutputTok == 4096 {
		cfg.MaxOutputTok = 0
	}
	cfg.PersistToken = false
	cfg.HTTPClient = nil
	cfg.Timeout = 0
	return cfg
}

// SanitizePersistedProviderEndpoint removes transient fields and credentials.
func SanitizePersistedProviderEndpoint(endpoint ProviderEndpointConfig) ProviderEndpointConfig {
	endpoint = NormalizeProviderEndpoint(endpoint)
	if endpoint.CredentialRef != "" {
		endpoint.Token = ""
		endpoint.TokenEnv = ""
		endpoint.PersistToken = false
	} else if !endpoint.PersistToken {
		endpoint.Token = ""
	}
	if endpoint.API == DefaultAPIForProvider(endpoint.Provider) {
		endpoint.API = ""
	}
	if endpoint.AuthType == DefaultAuthTypeForProvider(endpoint.Provider) {
		endpoint.AuthType = ""
	}
	endpoint.PersistToken = false
	endpoint.HTTPClient = nil
	endpoint.Timeout = 0
	return endpoint
}

// DefaultAuthTypeForProvider returns the maintained authentication scheme.
func DefaultAuthTypeForProvider(provider string) providers.AuthType {
	template, ok := LookupProvider(provider)
	if !ok {
		return providers.AuthAPIKey
	}
	if template.AuthType == "" {
		return providers.AuthAPIKey
	}
	return template.AuthType
}

// BuildAlias returns the visible provider/model alias.
func BuildAlias(provider string, modelName string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	modelName = strings.TrimSpace(modelName)
	if provider == "" {
		return strings.ToLower(modelName)
	}
	if modelName == "" {
		return provider
	}
	return strings.ToLower(provider + "/" + modelName)
}

// BuildProviderEndpointID returns a stable provider-endpoint identity.
func BuildProviderEndpointID(provider string, endpointID string, baseURL string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	endpointID = sanitizeConfigIDPart(firstNonEmpty(strings.TrimSpace(endpointID), "default"))
	if endpointID == "custom" || strings.HasPrefix(endpointID, "custom-") {
		endpointID = "custom-" + shortConfigHash(normalizedConfigBaseURL(baseURL))
	}
	if provider == "" {
		return endpointID
	}
	return provider + "@" + endpointID
}

// BuildModelID qualifies a visible alias by its provider profile.
func BuildModelID(profileID string, alias string) string {
	profileID = strings.ToLower(strings.TrimSpace(profileID))
	alias = strings.ToLower(strings.TrimSpace(alias))
	if profileID == "" {
		return alias
	}
	if alias == "" {
		return profileID
	}
	return profileID + "/" + alias
}

// NormalizeEndpointID resolves maintained endpoints and hashes custom URLs.
func NormalizeEndpointID(provider string, endpointID string, baseURL string, api providers.APIType) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	endpointID = sanitizeConfigIDPart(endpointID)
	if endpointID != "" {
		return endpointID
	}
	normalizedBaseURL := normalizedConfigBaseURL(baseURL)
	if provider == "volcengine" && api == providers.APIVolcengineCoding {
		return "coding-plan"
	}
	if template, ok := LookupProvider(provider); ok {
		if endpoint, found := EndpointForBaseURL(template, baseURL); found {
			return sanitizeConfigIDPart(endpoint.ID)
		}
	}
	if normalizedBaseURL == "" {
		return "default"
	}
	return "custom-" + shortConfigHash(normalizedBaseURL)
}

// ChoiceDetail returns a concise endpoint description for a configured model.
func ChoiceDetail(cfg Config) string {
	parts := []string{}
	if endpointID := strings.TrimSpace(cfg.ProviderEndpointID); endpointID != "" {
		parts = append(parts, "endpoint:"+endpointID)
	}
	if endpoint := strings.TrimSpace(cfg.EndpointID); endpoint != "" && endpoint != "default" {
		parts = append(parts, endpoint)
	}
	if baseURL := strings.TrimSpace(cfg.BaseURL); baseURL != "" {
		parts = append(parts, baseURL)
	}
	if tokenEnv := strings.TrimSpace(cfg.TokenEnv); tokenEnv != "" {
		parts = append(parts, "env:"+tokenEnv)
	}
	if credentialRef := strings.TrimSpace(cfg.CredentialRef); credentialRef != "" {
		parts = append(parts, "managed auth")
	}
	if len(parts) == 0 {
		return "configured model"
	}
	return strings.Join(parts, " · ")
}

// ChoiceFromConfig projects a configured model into selection metadata.
func ChoiceFromConfig(cfg Config) Choice {
	cfg = NormalizeConfig(cfg)
	return Choice{
		ID:                 cfg.ID,
		Alias:              cfg.Alias,
		Provider:           cfg.Provider,
		Model:              cfg.Model,
		ProviderEndpointID: cfg.ProviderEndpointID,
		EndpointID:         cfg.EndpointID,
		BaseURL:            cfg.BaseURL,
		Detail:             ChoiceDetail(cfg),
	}
}

// DedupeChoices removes duplicate or empty model identities.
func DedupeChoices(choices []Choice) []Choice {
	if len(choices) == 0 {
		return nil
	}
	out := make([]Choice, 0, len(choices))
	seen := map[string]struct{}{}
	for _, choice := range choices {
		id := strings.ToLower(strings.TrimSpace(choice.ID))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, choice)
	}
	return out
}

// DedupeNonEmptyStrings removes case-insensitive duplicates and empty values.
func DedupeNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// FirstNonEmptyAPI returns the first specified API dialect.
func FirstNonEmptyAPI(values ...providers.APIType) providers.APIType {
	for _, value := range values {
		if strings.TrimSpace(string(value)) != "" {
			return value
		}
	}
	return ""
}

// FirstNonEmptyAuthType returns the first specified authentication scheme.
func FirstNonEmptyAuthType(values ...providers.AuthType) providers.AuthType {
	for _, value := range values {
		if strings.TrimSpace(string(value)) != "" {
			return value
		}
	}
	return ""
}

// FirstNonNilHTTPClient returns the first configured HTTP client.
func FirstNonNilHTTPClient(values ...*http.Client) *http.Client {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func normalizedConfigBaseURL(baseURL string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
}

func sanitizeConfigIDPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func shortConfigHash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "default"
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:10]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
