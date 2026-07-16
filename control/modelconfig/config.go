// Package modelconfig owns Caelis provider profiles, configured model records,
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
	ID         string            `json:"id,omitempty"`
	Alias      string            `json:"alias,omitempty"`
	Provider   string            `json:"provider,omitempty"`
	ProfileID  string            `json:"profile_id,omitempty"`
	EndpointID string            `json:"endpoint_id,omitempty"`
	API        providers.APIType `json:"api,omitempty"`
	Model      string            `json:"model,omitempty"`
	BaseURL    string            `json:"base_url,omitempty"`
	HTTPClient *http.Client      `json:"-"`
	Token      string            `json:"token,omitempty"`
	TokenEnv   string            `json:"token_env,omitempty"`
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

// ProfileConfig stores endpoint and credential data shared by configured models.
type ProfileConfig struct {
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
	ID         string
	Alias      string
	Provider   string
	Model      string
	ProfileID  string
	EndpointID string
	BaseURL    string
	Detail     string
}

// NormalizeConfig canonicalizes identifiers, endpoint defaults, credentials,
// and model limits.
func NormalizeConfig(cfg Config) Config {
	cfg.ID = strings.ToLower(strings.TrimSpace(cfg.ID))
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.CredentialRef = strings.ToLower(strings.TrimSpace(cfg.CredentialRef))
	cfg.EndpointID = NormalizeEndpointID(cfg.Provider, cfg.EndpointID, cfg.BaseURL, cfg.API)
	cfg.ProfileID = strings.ToLower(strings.TrimSpace(cfg.ProfileID))
	if cfg.ProfileID == "" {
		cfg.ProfileID = BuildProfileID(cfg.Provider, cfg.EndpointID, cfg.BaseURL)
	}
	cfg.Alias = strings.ToLower(strings.TrimSpace(cfg.Alias))
	if cfg.Alias == "" {
		cfg.Alias = BuildAlias(cfg.Provider, cfg.Model)
	}
	if id := BuildModelID(cfg.ProfileID, cfg.Alias); id != "" {
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

// NormalizeProfileConfig canonicalizes one shared provider profile.
func NormalizeProfileConfig(profile ProfileConfig) ProfileConfig {
	profile.ID = strings.ToLower(strings.TrimSpace(profile.ID))
	profile.Provider = strings.ToLower(strings.TrimSpace(profile.Provider))
	profile.CredentialRef = strings.ToLower(strings.TrimSpace(profile.CredentialRef))
	profile.EndpointID = NormalizeEndpointID(profile.Provider, profile.EndpointID, profile.BaseURL, profile.API)
	if profile.ID == "" {
		profile.ID = BuildProfileID(profile.Provider, profile.EndpointID, profile.BaseURL)
	}
	if profile.API == "" {
		profile.API = DefaultAPIForProvider(profile.Provider)
	}
	if profile.AuthType == "" {
		profile.AuthType = DefaultAuthTypeForProvider(profile.Provider)
	}
	if profile.Token == "" && strings.TrimSpace(profile.TokenEnv) != "" {
		profile.Token = strings.TrimSpace(os.Getenv(strings.TrimSpace(profile.TokenEnv)))
	}
	return profile
}

// ProfileFromConfig extracts shared endpoint and authentication fields.
func ProfileFromConfig(cfg Config) ProfileConfig {
	cfg = NormalizeConfig(cfg)
	return NormalizeProfileConfig(ProfileConfig{
		ID:                      cfg.ProfileID,
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

// ConfigCarriesProfileFields reports whether cfg contains any profile-owned data.
func ConfigCarriesProfileFields(cfg Config) bool {
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

// ConfigCarriesProfileAuth reports whether cfg can update stored credentials.
func ConfigCarriesProfileAuth(cfg Config) bool {
	return strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.TokenEnv) != "" ||
		strings.TrimSpace(cfg.CredentialRef) != "" ||
		strings.TrimSpace(cfg.HeaderKey) != "" ||
		cfg.PersistToken ||
		cfg.HTTPClient != nil
}

// MergeConfigProfile hydrates a model record from its provider profile.
func MergeConfigProfile(cfg Config, profile ProfileConfig) Config {
	cfg = NormalizeConfig(cfg)
	profile = NormalizeProfileConfig(profile)
	cfg.ProfileID = profile.ID
	cfg.Provider = firstNonEmpty(profile.Provider, cfg.Provider)
	cfg.EndpointID = profile.EndpointID
	cfg.API = FirstNonEmptyAPI(profile.API, cfg.API)
	cfg.BaseURL = firstNonEmpty(profile.BaseURL, cfg.BaseURL)
	cfg.HTTPClient = FirstNonNilHTTPClient(profile.HTTPClient, cfg.HTTPClient)
	cfg.Token = firstNonEmpty(profile.Token, cfg.Token)
	cfg.TokenEnv = firstNonEmpty(profile.TokenEnv, cfg.TokenEnv)
	cfg.CredentialRef = firstNonEmpty(profile.CredentialRef, cfg.CredentialRef)
	cfg.PersistToken = profile.PersistToken || cfg.PersistToken
	cfg.AuthType = FirstNonEmptyAuthType(profile.AuthType, cfg.AuthType)
	cfg.HeaderKey = firstNonEmpty(profile.HeaderKey, cfg.HeaderKey)
	if profile.Timeout > 0 {
		cfg.Timeout = profile.Timeout
	}
	if profile.StreamFirstEventTimeout > 0 {
		cfg.StreamFirstEventTimeout = profile.StreamFirstEventTimeout
	}
	return NormalizeConfig(cfg)
}

// SupportsReasoningEffort reports whether cfg accepts an effort value.
func SupportsReasoningEffort(cfg Config, effort string) bool {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
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
	if cfg.ProfileID != "" {
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

// SanitizePersistedProfile removes transient fields and non-persisted secrets.
func SanitizePersistedProfile(profile ProfileConfig) ProfileConfig {
	profile = NormalizeProfileConfig(profile)
	if profile.CredentialRef != "" {
		profile.Token = ""
		profile.TokenEnv = ""
		profile.PersistToken = false
	} else if !profile.PersistToken {
		profile.Token = ""
	}
	if profile.API == DefaultAPIForProvider(profile.Provider) {
		profile.API = ""
	}
	if profile.AuthType == DefaultAuthTypeForProvider(profile.Provider) {
		profile.AuthType = ""
	}
	profile.PersistToken = false
	profile.HTTPClient = nil
	profile.Timeout = 0
	return profile
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

// BuildProfileID returns a stable provider-endpoint identity.
func BuildProfileID(provider string, endpointID string, baseURL string) string {
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
	if profileID := strings.TrimSpace(cfg.ProfileID); profileID != "" {
		parts = append(parts, "profile:"+profileID)
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
		ID:         cfg.ID,
		Alias:      cfg.Alias,
		Provider:   cfg.Provider,
		Model:      cfg.Model,
		ProfileID:  cfg.ProfileID,
		EndpointID: cfg.EndpointID,
		BaseURL:    cfg.BaseURL,
		Detail:     ChoiceDetail(cfg),
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
