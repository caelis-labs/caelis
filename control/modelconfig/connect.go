package modelconfig

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/control/modelcatalog"
)

// AuthPurpose identifies the Control operation requesting provider
// authentication.
type AuthPurpose string

const (
	// AuthPurposeConnect authenticates a provider profile before it is saved.
	AuthPurposeConnect AuthPurpose = "connect"
	// AuthPurposeModelSelection authenticates before listing selectable models.
	AuthPurposeModelSelection AuthPurpose = "model_selection"
)

// AuthenticateRequest carries the endpoint context required by a provider's
// interactive authentication flow.
type AuthenticateRequest struct {
	Provider        string
	BaseURL         string
	HTTPClient      *http.Client
	Purpose         AuthPurpose
	OpenBrowser     bool
	CallbackTimeout time.Duration
}

// AuthenticateResult contains provider-owned facts discovered while
// authenticating. A provider may return an authoritative account-scoped model
// catalog for model selection; callers fall back to the maintained catalog
// only when ModelCatalogAuthoritative is false.
type AuthenticateResult struct {
	SelectableModels          []string
	ModelCatalogAuthoritative bool
}

// AuthenticateFunc performs provider-specific authentication for Control.
type AuthenticateFunc func(context.Context, AuthenticateRequest) (AuthenticateResult, error)

// AuthenticateProvider runs the maintained authentication implementation for
// a provider. Providers that do not require an interactive flow are no-ops.
func AuthenticateProvider(ctx context.Context, req AuthenticateRequest) (AuthenticateResult, error) {
	template, ok := LookupProvider(req.Provider)
	if !ok {
		return AuthenticateResult{}, fmt.Errorf("modelconfig: provider %q is not supported", strings.TrimSpace(req.Provider))
	}
	if template.AuthFlow == "" {
		return AuthenticateResult{}, nil
	}
	if template.AuthFlow == AuthFlowCodexOAuth {
		return AuthenticateResult{}, fmt.Errorf("modelconfig: codex authentication must be provided by the Control host")
	}
	if template.AuthFlow != AuthFlowCodeFreeOAuth {
		return AuthenticateResult{}, fmt.Errorf("modelconfig: provider %q has unsupported authentication flow %q", template.Provider, template.AuthFlow)
	}
	opts := providers.CodeFreeEnsureAuthOptions{
		BaseURL:         firstNonEmpty(req.BaseURL, template.DefaultBaseURL),
		HTTPClient:      req.HTTPClient,
		OpenBrowser:     req.OpenBrowser,
		CallbackTimeout: req.CallbackTimeout,
	}
	if req.Purpose == AuthPurposeModelSelection {
		_, err := providers.CodeFreeEnsureModelSelectionAuth(ctx, opts)
		return AuthenticateResult{}, err
	}
	_, err := providers.CodeFreeEnsureAuth(ctx, opts)
	return AuthenticateResult{}, err
}

// ModelSelection contains the model-specific part of one provider connection.
// A nil ReasoningLevels slice selects maintained defaults; a provided empty
// slice explicitly disables selectable reasoning effort levels.
type ModelSelection struct {
	Name                string
	ContextWindowTokens int
	MaxOutputTokens     int
	ReasoningEffort     string
	ReasoningLevels     []string
}

// ConnectRequest is the transport-neutral input used to authenticate one
// provider profile and assemble one or more complete configured models.
// Surface/protocol request types must be mapped into it.
type ConnectRequest struct {
	Provider                       string
	EndpointID                     string
	Models                         []ModelSelection
	BaseURL                        string
	HTTPClient                     *http.Client
	TimeoutSeconds                 int
	StreamFirstEventTimeoutSeconds int
	APIKey                         string
	TokenEnv                       string
	AuthType                       string
}

// ConnectOptions supplies runtime facts needed while assembling a connection.
type ConnectOptions struct {
	HasReusableAuth func(context.Context, string, string) bool
	Authenticate    AuthenticateFunc
}

// ModelDefaults contains Control-selected limits and reasoning behavior for a
// model choice.
type ModelDefaults struct {
	ContextWindowTokens    int
	MaxOutputTokens        int
	ReasoningLevels        []string
	ReasoningMode          string
	DefaultReasoningEffort string
}

// SelectableModel is one Control-maintained model choice. MetadataComplete is
// true only when Control can assemble limits and reasoning behavior without
// asking the user for advanced configuration.
type SelectableModel struct {
	Name             string
	MetadataComplete bool
}

// ResolveModelDefaults resolves maintained metadata for a model and falls back
// to the provider template only when the concrete model is unknown.
func ResolveModelDefaults(provider string, modelName string) (ModelDefaults, error) {
	template, ok := LookupProvider(provider)
	if !ok {
		return ModelDefaults{}, fmt.Errorf("modelconfig: provider %q is not supported", strings.TrimSpace(provider))
	}
	if template.AuthFlow == AuthFlowCodexOAuth {
		if defaults, known := codexOAuthModelDefaults(modelName); known {
			return defaults, nil
		}
	}
	caps, known := modelcatalog.LookupModelCapabilities(template.Provider, modelName)
	if !known {
		caps = modelcatalog.DefaultModelCapabilities()
		if template.DefaultContextWindowTokens > 0 {
			caps.ContextWindowTokens = template.DefaultContextWindowTokens
		}
		if template.DefaultMaxOutputTokens > 0 {
			caps.MaxOutputTokens = template.DefaultMaxOutputTokens
			caps.DefaultMaxOutputTokens = template.DefaultMaxOutputTokens
		}
		if levels := NormalizeReasoningLevels(template.DefaultReasoningLevels); len(levels) > 0 {
			caps.SupportsReasoning = true
			caps.ReasoningEfforts = levels
		}
		if mode := modelcatalog.NormalizeReasoningMode(template.DefaultReasoningMode); mode != "" {
			caps.ReasoningMode = mode
		}
		if effort := modelcatalog.NormalizeReasoningEffort(template.DefaultReasoningEffort); effort != "" {
			caps.DefaultReasoningEffort = effort
		}
	}
	contextWindow := caps.ContextWindowTokens
	if contextWindow <= 0 {
		contextWindow = template.DefaultContextWindowTokens
	}
	if contextWindow <= 0 {
		contextWindow = modelcatalog.DefaultModelCapabilities().ContextWindowTokens
	}
	maxOutput := caps.DefaultMaxOutputTokens
	if maxOutput <= 0 {
		maxOutput = caps.MaxOutputTokens
	}
	if maxOutput <= 0 {
		maxOutput = template.DefaultMaxOutputTokens
	}
	if maxOutput <= 0 {
		maxOutput = modelcatalog.DefaultModelCapabilities().DefaultMaxOutputTokens
	}
	reasoningLevels := NormalizeReasoningLevels(modelcatalog.ReasoningLevelsForModel(template.Provider, modelName))
	if len(reasoningLevels) == 0 {
		reasoningLevels = NormalizeReasoningLevels(caps.ReasoningEfforts)
	}
	reasoningMode := modelcatalog.NormalizeReasoningMode(caps.ReasoningMode)
	if reasoningMode == "" {
		reasoningMode = modelcatalog.ReasoningModeForModel(template.Provider, modelName)
	}
	return ModelDefaults{
		ContextWindowTokens:    contextWindow,
		MaxOutputTokens:        maxOutput,
		ReasoningLevels:        reasoningLevels,
		ReasoningMode:          reasoningMode,
		DefaultReasoningEffort: modelcatalog.NormalizeReasoningEffort(caps.DefaultReasoningEffort),
	}, nil
}

// AssembleConnect validates and authenticates a provider connection once, then
// produces complete model configurations consumed by persistence and SDK
// construction. Result order matches the requested model-selection order.
func AssembleConnect(ctx context.Context, req ConnectRequest, opts ConnectOptions) ([]Config, error) {
	template, ok := LookupProvider(req.Provider)
	if !ok {
		return nil, fmt.Errorf("provider %q is not supported", strings.TrimSpace(req.Provider))
	}
	req.Provider = template.Provider
	req.Models = normalizeModelSelections(req.Models)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.TokenEnv = strings.TrimSpace(req.TokenEnv)
	if env, ok := ParseTokenEnvSpec(req.APIKey); ok {
		req.TokenEnv = env
		req.APIKey = ""
	}
	if req.BaseURL == "" {
		req.BaseURL = template.DefaultBaseURL
	}
	endpoint, hasEndpoint := EndpointForBaseURL(template, req.BaseURL)
	if strings.TrimSpace(req.EndpointID) == "" && hasEndpoint {
		req.EndpointID = endpoint.ID
	}
	reusableAuth := opts.HasReusableAuth != nil && opts.HasReusableAuth(ctx, template.Provider, req.BaseURL)
	if err := validateConnectRequest(template, req, reusableAuth); err != nil {
		return nil, err
	}
	if template.AuthFlow != "" {
		if opts.Authenticate == nil {
			return nil, fmt.Errorf("modelconfig: %s authentication is unavailable", template.Provider)
		}
		if _, err := opts.Authenticate(ctx, AuthenticateRequest{
			Provider:        template.Provider,
			BaseURL:         req.BaseURL,
			HTTPClient:      req.HTTPClient,
			Purpose:         AuthPurposeConnect,
			OpenBrowser:     true,
			CallbackTimeout: 5 * time.Minute,
		}); err != nil {
			return nil, err
		}
	}
	api := template.API
	if hasEndpoint && endpoint.API != "" {
		api = endpoint.API
	}
	authType := DefaultAuthTypeForProvider(template.Provider)
	if strings.TrimSpace(req.AuthType) != "" {
		authType = parseAuthType(req.AuthType)
	}
	if template.NoAuthRequired {
		authType = model.AuthNone
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if req.TimeoutSeconds <= 0 {
		timeout = 60 * time.Second
	}
	out := make([]Config, 0, len(req.Models))
	for _, selection := range req.Models {
		defaults, err := ResolveModelDefaults(template.Provider, selection.Name)
		if err != nil {
			return nil, err
		}
		contextWindow := selection.ContextWindowTokens
		if contextWindow <= 0 {
			contextWindow = defaults.ContextWindowTokens
		}
		maxOutput := selection.MaxOutputTokens
		if maxOutput <= 0 {
			maxOutput = defaults.MaxOutputTokens
		}
		reasoningLevels := defaults.ReasoningLevels
		reasoningMode := defaults.ReasoningMode
		defaultReasoningEffort := defaults.DefaultReasoningEffort
		if selection.ReasoningLevels != nil {
			reasoningLevels = NormalizeReasoningLevels(selection.ReasoningLevels)
			if len(reasoningLevels) == 0 {
				reasoningMode = modelcatalog.ReasoningModeNone
				defaultReasoningEffort = ""
			} else if reasoningMode == "" || reasoningMode == modelcatalog.ReasoningModeNone {
				reasoningMode = modelcatalog.ReasoningModeEffort
			}
			if defaultReasoningEffort != "" &&
				!modelcatalog.SupportsReasoningEffortList(reasoningLevels, defaultReasoningEffort) {
				defaultReasoningEffort = ""
			}
		}
		reasoningEffort := modelcatalog.NormalizeReasoningEffort(selection.ReasoningEffort)
		if reasoningEffort == "" {
			reasoningEffort = defaultReasoningEffort
		}
		if reasoningEffort == "" {
			reasoningEffort = modelcatalog.PreferredReasoningEffort(reasoningLevels)
		}
		if reasoningMode == modelcatalog.ReasoningModeNone {
			reasoningEffort = ""
		}
		out = append(out, NormalizeConfig(Config{
			Provider:                template.Provider,
			EndpointID:              strings.TrimSpace(req.EndpointID),
			API:                     api,
			Model:                   selection.Name,
			BaseURL:                 req.BaseURL,
			HTTPClient:              req.HTTPClient,
			Token:                   req.APIKey,
			TokenEnv:                req.TokenEnv,
			CredentialRef:           credentialRefForTemplate(template),
			PersistToken:            req.APIKey != "" && req.TokenEnv == "",
			AuthType:                authType,
			ContextWindowTokens:     contextWindow,
			DefaultReasoningEffort:  reasoningEffort,
			ReasoningEffort:         reasoningEffort,
			ReasoningLevels:         reasoningLevels,
			ReasoningMode:           reasoningMode,
			MaxOutputTok:            maxOutput,
			Timeout:                 timeout,
			StreamFirstEventTimeout: time.Duration(req.StreamFirstEventTimeoutSeconds) * time.Second,
		}))
	}
	return out, nil
}

// SelectableModels authenticates when required and returns the maintained
// model choices for a provider. Configured model IDs are intentionally not a
// candidate source: a private model whose name happens to match a catalog
// prefix must still use the advanced configuration path.
func SelectableModels(ctx context.Context, provider string, baseURL string, authenticate AuthenticateFunc) ([]SelectableModel, error) {
	template, ok := LookupProvider(provider)
	if !ok {
		return nil, nil
	}
	authResult := AuthenticateResult{}
	if template.AuthFlow != "" {
		if authenticate == nil {
			return nil, fmt.Errorf("modelconfig: %s model-selection authentication is unavailable", template.Provider)
		}
		var err error
		authResult, err = authenticate(ctx, AuthenticateRequest{
			Provider:        template.Provider,
			BaseURL:         firstNonEmpty(baseURL, template.DefaultBaseURL),
			Purpose:         AuthPurposeModelSelection,
			OpenBrowser:     true,
			CallbackTimeout: 5 * time.Minute,
		})
		if err != nil {
			return nil, err
		}
	}
	var maintained []string
	if template.AuthFlow == AuthFlowCodexOAuth {
		if authResult.ModelCatalogAuthoritative {
			maintained = filterCodexOAuthSelectableModels(authResult.SelectableModels)
		} else {
			maintained = codexOAuthSelectableModels()
		}
	} else if template.UseModelDirectory {
		maintained = modelcatalog.ListModelDirectoryModels(template.Provider)
	} else {
		maintained = modelcatalog.ListRecommendedModels(template.Provider)
	}
	models := make([]SelectableModel, 0, len(maintained))
	for _, name := range maintained {
		metadataComplete := hasCompleteModelMetadata(template.Provider, name)
		if template.AuthFlow == AuthFlowCodexOAuth {
			// The fixed Codex profile supplies conservative context, output, and
			// reasoning defaults for every explicitly allowed catalog entry.
			metadataComplete = true
		}
		models = append(models, SelectableModel{Name: name, MetadataComplete: metadataComplete})
	}
	if template.AuthFlow == AuthFlowCodexOAuth {
		// The account catalog and bundled Codex snapshot are maintained in
		// product priority order. Keep that order aligned with the official
		// client instead of alphabetizing version-like model IDs.
		return uniqueSelectableModels(models), nil
	}
	return sortedUniqueSelectableModels(models), nil
}

// ParseTokenEnvSpec recognizes env:NAME and $NAME credential references.
func ParseTokenEnvSpec(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(strings.ToLower(trimmed), "env:"):
		env := strings.TrimSpace(trimmed[len("env:"):])
		return env, env != ""
	case strings.HasPrefix(trimmed, "$"):
		env := strings.TrimSpace(strings.TrimPrefix(trimmed, "$"))
		return env, env != ""
	default:
		return "", false
	}
}

// NormalizeReasoningLevels normalizes a user- or catalog-provided effort list.
func NormalizeReasoningLevels(levels []string) []string {
	if len(levels) == 0 {
		return nil
	}
	out := make([]string, 0, len(levels))
	seen := map[string]struct{}{}
	for _, level := range levels {
		normalized := modelcatalog.NormalizeReasoningEffort(level)
		if normalized == "" || normalized == "-" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func validateConnectRequest(template ProviderTemplate, req ConnectRequest, reusableAuth bool) error {
	if len(req.Models) == 0 {
		return fmt.Errorf("model is required; use /connect and choose or type a model name")
	}
	if req.BaseURL != "" {
		parsed, err := url.Parse(req.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("base URL is invalid; use a full URL such as %s", template.DefaultBaseURL)
		}
	}
	if template.AuthFlow == AuthFlowCodexOAuth && NormalizeBaseURL(req.BaseURL) != NormalizeBaseURL(template.DefaultBaseURL) {
		return fmt.Errorf("modelconfig: codex OAuth requires the maintained endpoint %s", template.DefaultBaseURL)
	}
	if template.NoAuthRequired || template.AuthFlow != "" || reusableAuth || req.APIKey != "" || req.TokenEnv != "" {
		return nil
	}
	envHint := DefaultTokenEnv(template.Provider, req.BaseURL)
	if envHint == "" {
		envHint = "YOUR_API_KEY"
	}
	return fmt.Errorf("API key is missing; paste a key or enter env:%s in /connect", envHint)
}

func credentialRefForTemplate(template ProviderTemplate) string {
	if template.AuthFlow == AuthFlowCodexOAuth {
		return CodexOAuthCredentialRef
	}
	return ""
}

func normalizeModelSelections(selections []ModelSelection) []ModelSelection {
	seen := map[string]struct{}{}
	out := make([]ModelSelection, 0, len(selections))
	for _, selection := range selections {
		selection.Name = strings.TrimSpace(selection.Name)
		if selection.Name == "" {
			continue
		}
		key := strings.ToLower(selection.Name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if selection.ReasoningLevels != nil {
			selection.ReasoningLevels = append([]string{}, selection.ReasoningLevels...)
		}
		out = append(out, selection)
	}
	return out
}

func hasCompleteModelMetadata(provider string, modelName string) bool {
	caps, ok := modelcatalog.LookupModelCapabilities(provider, modelName)
	if !ok || caps.ContextWindowTokens <= 0 {
		return false
	}
	if caps.DefaultMaxOutputTokens <= 0 && caps.MaxOutputTokens <= 0 {
		return false
	}
	return modelcatalog.NormalizeReasoningMode(caps.ReasoningMode) != ""
}

func sortedUniqueSelectableModels(values []SelectableModel) []SelectableModel {
	out := uniqueSelectableModels(values)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func uniqueSelectableModels(values []SelectableModel) []SelectableModel {
	seen := map[string]SelectableModel{}
	keys := make([]string, 0, len(values))
	for _, value := range values {
		value.Name = strings.TrimSpace(value.Name)
		if value.Name == "" {
			continue
		}
		key := strings.ToLower(value.Name)
		if existing, ok := seen[key]; ok {
			existing.MetadataComplete = existing.MetadataComplete || value.MetadataComplete
			seen[key] = existing
			continue
		}
		seen[key] = value
		keys = append(keys, key)
	}
	out := make([]SelectableModel, 0, len(keys))
	for _, key := range keys {
		out = append(out, seen[key])
	}
	return out
}

func parseAuthType(value string) model.AuthType {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "api_key", "apikey":
		return model.AuthAPIKey
	case "bearer_token", "bearer":
		return model.AuthBearerToken
	case "oauth_token", "oauth":
		return model.AuthOAuthToken
	case "none":
		return model.AuthNone
	default:
		return model.AuthAPIKey
	}
}
