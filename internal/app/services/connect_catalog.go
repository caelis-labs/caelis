package services

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
)

const (
	ConnectDefaultTimeoutSeconds = 60

	ConnectVolcengineStandardValue = "standard"
	ConnectVolcengineCodingValue   = "coding-plan"

	ConnectXiaomiAPIBaseURL         = "https://api.xiaomimimo.com/v1"
	ConnectXiaomiTokenPlanCNBaseURL = "https://token-plan-cn.xiaomimimo.com/v1"
	ConnectXiaomiTokenPlanCNAlias   = "xiaomi-token-plan-cn"
)

type ConnectProviderTemplate struct {
	Label                string
	API                  model.APIType
	Provider             string
	Description          string
	DefaultEndpointID    string
	DefaultBaseURL       string
	DefaultContextTokens int
	DefaultMaxOutput     int
	NoAuthRequired       bool
	CommonModels         []string
	Endpoints            []ConnectEndpointTemplate
}

type ConnectEndpointTemplate struct {
	ID       string
	BaseURL  string
	Display  string
	Detail   string
	API      model.APIType
	TokenEnv string
}

type ConnectCandidate struct {
	Value   string
	Display string
	Detail  string
	NoAuth  bool
}

type ConnectModelDefaults struct {
	ContextWindow          int
	MaxOutput              int
	ReasoningLevels        []string
	DefaultReasoningEffort string
}

var xiaomiMimoConnectModels = []string{"mimo-v2.5-pro", "mimo-v2-pro", "mimo-v2.5", "mimo-v2-omni", "mimo-v2-flash"}

var connectProviderTemplates = []ConnectProviderTemplate{
	{Label: "openai", API: model.APIOpenAI, Provider: "openai", Description: "OpenAI-hosted models", DefaultBaseURL: "https://api.openai.com/v1", DefaultContextTokens: 128000, CommonModels: []string{"gpt-4o", "gpt-4o-mini", "o3", "o4-mini"}},
	{Label: "openai-compatible", API: model.APIOpenAICompatible, Provider: "openai-compatible", Description: "OpenAI-compatible proxy or self-hosted endpoint", DefaultBaseURL: "https://api.openai.com/v1", DefaultContextTokens: 128000, CommonModels: []string{"gpt-4o", "gpt-4o-mini", "o3", "o4-mini"}},
	{Label: "codefree", API: model.APICodeFree, Provider: "codefree", Description: "China Telecom SRD CodeFree models via browser OAuth", DefaultBaseURL: "https://www.srdcloud.cn", DefaultContextTokens: 128000, DefaultMaxOutput: 8000, NoAuthRequired: true, CommonModels: []string{"GLM-4.7", "DeepSeek-V3.1-Terminus", "Qwen3.5-122B-A10B", "GLM-5.1"}},
	{Label: "openrouter", API: model.APIOpenRouter, Provider: "openrouter", Description: "OpenRouter multi-provider routing", DefaultBaseURL: "https://openrouter.ai/api/v1", DefaultContextTokens: 262144, CommonModels: []string{"openai/gpt-4o-mini", "anthropic/claude-sonnet-4", "google/gemini-2.5-flash"}},
	{Label: "gemini", API: model.APIGemini, Provider: "gemini", Description: "Google Gemini API", DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta", DefaultContextTokens: 128000, CommonModels: []string{"gemini-2.5-flash", "gemini-2.5-pro"}},
	{Label: "anthropic", API: model.APIAnthropic, Provider: "anthropic", Description: "Anthropic Claude API", DefaultBaseURL: "https://api.anthropic.com", DefaultContextTokens: 200000, DefaultMaxOutput: 1024, CommonModels: []string{"claude-sonnet-4-20250514", "claude-opus-4-20250514"}},
	{Label: "anthropic-compatible", API: model.APIAnthropicCompatible, Provider: "anthropic-compatible", Description: "Anthropic-compatible proxy or self-hosted endpoint", DefaultBaseURL: "https://api.anthropic.com", DefaultContextTokens: 200000, DefaultMaxOutput: 1024},
	{Label: "deepseek", API: model.APIDeepSeek, Provider: "deepseek", Description: "DeepSeek V4 models", DefaultBaseURL: "https://api.deepseek.com/v1", DefaultContextTokens: 1048576, CommonModels: []string{"deepseek-v4-flash", "deepseek-v4-pro"}},
	{Label: "xiaomi", API: model.APIMimo, Provider: "xiaomi", Description: "Xiaomi Mimo models", DefaultEndpointID: "api-cn", DefaultBaseURL: ConnectXiaomiAPIBaseURL, DefaultContextTokens: 262144, CommonModels: xiaomiMimoConnectModels, Endpoints: []ConnectEndpointTemplate{
		{ID: "api-cn", BaseURL: ConnectXiaomiAPIBaseURL, Display: "api cn", Detail: "Xiaomi MiMo API CN · OpenAI-compatible", API: model.APIMimo, TokenEnv: "XIAOMI_API_KEY"},
		{ID: "token-plan-cn", BaseURL: ConnectXiaomiTokenPlanCNBaseURL, Display: "token plan cn", Detail: "Xiaomi MiMo Token Plan CN · OpenAI-compatible", API: model.APIMimo, TokenEnv: "MIMO_TOKEN_PLAN_API_KEY"},
	}},
	{Label: "minimax", API: model.APIMiniMax, Provider: "minimax", Description: "MiniMax models over an Anthropic-compatible API", DefaultBaseURL: "https://api.minimaxi.com/anthropic", DefaultContextTokens: 204800, DefaultMaxOutput: 8192, CommonModels: []string{"MiniMax-M2.7", "MiniMax-M2.7-highspeed", "MiniMax-M2.5", "MiniMax-M2.5-highspeed", "MiniMax-M2.1", "MiniMax-M2.1-highspeed", "MiniMax-M2"}},
	{Label: "volcengine", API: model.APIVolcengine, Provider: "volcengine", Description: "Volcengine Ark standard or coding-plan endpoints", DefaultEndpointID: ConnectVolcengineStandardValue, DefaultBaseURL: "https://ark.cn-beijing.volces.com/api/v3", DefaultContextTokens: 128000, Endpoints: []ConnectEndpointTemplate{
		{ID: ConnectVolcengineStandardValue, BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Display: "standard api", Detail: "regular Ark endpoint", API: model.APIVolcengine, TokenEnv: "VOLCENGINE_API_KEY"},
		{ID: ConnectVolcengineCodingValue, BaseURL: "https://ark.cn-beijing.volces.com/api/coding/v3", Display: "coding plan", Detail: "Ark coding-plan endpoint", API: model.APIVolcengineCoding, TokenEnv: "VOLCENGINE_API_KEY"},
	}},
	{Label: "ollama", API: model.APIOllama, Provider: "ollama", Description: "Local Ollama runtime", DefaultBaseURL: "http://localhost:11434", DefaultContextTokens: 128000, NoAuthRequired: true, CommonModels: []string{"qwen2.5:7b", "llama3.1:8b", "deepseek-r1:7b", "gemma3:4b"}},
}

func ConnectProviderTemplates() []ConnectProviderTemplate {
	out := make([]ConnectProviderTemplate, 0, len(connectProviderTemplates))
	for _, tpl := range connectProviderTemplates {
		out = append(out, cloneConnectProviderTemplate(tpl))
	}
	return out
}

func FindConnectProviderTemplate(value string) (ConnectProviderTemplate, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, tpl := range connectProviderTemplates {
		if tpl.Label == value || tpl.Provider == value {
			return cloneConnectProviderTemplate(tpl), true
		}
	}
	switch value {
	case ConnectXiaomiTokenPlanCNAlias:
		return ConnectProviderTemplate{
			Label:                ConnectXiaomiTokenPlanCNAlias,
			API:                  model.APIMimo,
			Provider:             "xiaomi",
			Description:          "Xiaomi Mimo Token Plan CN over an OpenAI-compatible API",
			DefaultEndpointID:    "token-plan-cn",
			DefaultBaseURL:       ConnectXiaomiTokenPlanCNBaseURL,
			DefaultContextTokens: 1048576,
			CommonModels:         append([]string(nil), xiaomiMimoConnectModels...),
			Endpoints:            xiaomiConnectEndpoints(),
		}, true
	}
	return ConnectProviderTemplate{}, false
}

func xiaomiConnectEndpoints() []ConnectEndpointTemplate {
	for _, tpl := range connectProviderTemplates {
		if tpl.Provider == "xiaomi" {
			return cloneConnectEndpoints(tpl.Endpoints)
		}
	}
	return nil
}

func ConnectEndpointForBaseURL(tpl ConnectProviderTemplate, baseURL string) (ConnectEndpointTemplate, bool) {
	normalized := NormalizeConnectBaseURL(baseURL)
	for _, endpoint := range tpl.Endpoints {
		if normalized != "" && normalized == NormalizeConnectBaseURL(endpoint.BaseURL) {
			return endpoint, true
		}
	}
	if normalized == "" {
		for _, endpoint := range tpl.Endpoints {
			if strings.EqualFold(strings.TrimSpace(endpoint.ID), strings.TrimSpace(tpl.DefaultEndpointID)) {
				return endpoint, true
			}
		}
	}
	return ConnectEndpointTemplate{}, false
}

func ConnectModelConfigFromWizardState(state ConnectWizardState) (appsettings.ModelConfig, bool) {
	tpl, ok := FindConnectProviderTemplate(state.Provider)
	if !ok {
		return appsettings.ModelConfig{}, false
	}
	baseURL := strings.TrimSpace(state.BaseURL)
	if baseURL == "" {
		baseURL = tpl.DefaultBaseURL
	}
	timeoutSeconds := state.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = ConnectDefaultTimeoutSeconds
	}
	endpoint, hasEndpoint := ConnectEndpointForBaseURL(tpl, baseURL)
	authType := DefaultConnectAuthType(tpl.Provider)
	if tpl.NoAuthRequired {
		authType = model.AuthNone
	}
	cfg := appsettings.ModelConfig{
		Provider:            tpl.Provider,
		Model:               strings.TrimSpace(state.Model),
		BaseURL:             baseURL,
		AuthType:            string(authType),
		ContextWindowTokens: state.ContextWindowTokens,
		MaxOutputTokens:     state.MaxOutputTokens,
		ReasoningLevels:     append([]string(nil), state.ReasoningLevels...),
		Timeout:             time.Duration(timeoutSeconds) * time.Second,
	}
	if hasEndpoint {
		cfg.EndpointID = strings.TrimSpace(endpoint.ID)
	}
	tokenRef := strings.TrimSpace(state.TokenRef)
	if env, ok := ParseConnectTokenEnvSpec(tokenRef); ok {
		cfg.TokenEnv = env
	} else {
		cfg.Token = tokenRef
	}
	return appsettings.NormalizeModelConfig(cfg), true
}

func NormalizeConnectBaseURL(baseURL string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
}

func ParseConnectTokenEnvSpec(value string) (string, bool) {
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

func DefaultConnectTokenEnvName(provider string, baseURL string) string {
	endpoint, endpointOK := connectEndpointForProviderAndBaseURL(provider, baseURL)
	if endpointOK && strings.TrimSpace(endpoint.TokenEnv) != "" {
		return strings.TrimSpace(endpoint.TokenEnv)
	}
	if IsXiaomiTokenPlanProvider(provider) || IsXiaomiTokenPlanBaseURL(baseURL) {
		return "MIMO_TOKEN_PLAN_API_KEY"
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "minimax":
		return "MINIMAX_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "openai-compatible":
		return "OPENAI_COMPATIBLE_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "anthropic-compatible":
		return "ANTHROPIC_COMPATIBLE_API_KEY"
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case ConnectXiaomiTokenPlanCNAlias:
		return "MIMO_TOKEN_PLAN_API_KEY"
	case "xiaomi":
		return "XIAOMI_API_KEY"
	case "volcengine":
		return "VOLCENGINE_API_KEY"
	default:
		return ""
	}
}

func DefaultConnectAuthType(provider string) model.AuthType {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "minimax":
		return model.AuthBearerToken
	default:
		return model.AuthAPIKey
	}
}

func IsXiaomiTokenPlanProvider(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), ConnectXiaomiTokenPlanCNAlias)
}

func IsXiaomiTokenPlanBaseURL(baseURL string) bool {
	if NormalizeConnectBaseURL(baseURL) == NormalizeConnectBaseURL(ConnectXiaomiTokenPlanCNBaseURL) {
		return true
	}
	host := ""
	if parsed, err := url.Parse(strings.TrimSpace(baseURL)); err == nil {
		host = strings.ToLower(strings.TrimSpace(parsed.Host))
	}
	if host == "" {
		host = strings.ToLower(strings.TrimSpace(strings.Split(strings.TrimPrefix(baseURL, "//"), "/")[0]))
	}
	return host == "token-plan-cn.xiaomimimo.com"
}

func connectEndpointForProviderAndBaseURL(provider string, baseURL string) (ConnectEndpointTemplate, bool) {
	tpl, ok := FindConnectProviderTemplate(provider)
	if !ok {
		return ConnectEndpointTemplate{}, false
	}
	return ConnectEndpointForBaseURL(tpl, baseURL)
}

func (s ModelService) ConnectProviderCandidates(query string, limit int) []ConnectCandidate {
	out := make([]ConnectCandidate, 0, len(connectProviderTemplates))
	for _, tpl := range connectProviderTemplates {
		if query != "" && !strings.Contains(strings.ToLower(tpl.Label+" "+tpl.DefaultBaseURL), strings.ToLower(strings.TrimSpace(query))) {
			continue
		}
		detailParts := []string{strings.TrimSpace(tpl.Description), strings.TrimSpace(tpl.DefaultBaseURL)}
		if tpl.Provider == "codefree" {
			detailParts = append(detailParts, "browser oauth")
		} else if tpl.NoAuthRequired {
			detailParts = append(detailParts, "no auth")
		} else if env := DefaultConnectTokenEnvName(tpl.Provider, ""); env != "" {
			detailParts = append(detailParts, "env:"+env)
		}
		out = append(out, ConnectCandidate{
			Value:   tpl.Label,
			Display: tpl.Label,
			Detail:  strings.Join(compactConnectNonEmpty(detailParts), " · "),
			NoAuth:  tpl.NoAuthRequired,
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s ModelService) ConnectEndpointCandidates(ctx context.Context, provider string, query string, limit int) []ConnectCandidate {
	tpl, ok := FindConnectProviderTemplate(provider)
	if !ok {
		return nil
	}
	candidates := connectEndpointCandidatesForTemplate(tpl)
	if len(candidates) == 0 {
		candidates = append(candidates, ConnectCandidate{Value: tpl.DefaultBaseURL, Display: tpl.DefaultBaseURL, Detail: "default base URL"})
	}
	for i := range candidates {
		if s.hasReusableConnectAuth(ctx, tpl.Provider, candidates[i].Value) {
			candidates[i].NoAuth = true
			candidates[i].Detail = strings.Join(compactConnectNonEmpty([]string{strings.TrimSpace(candidates[i].Detail), "configured auth"}), " · ")
		}
	}
	return filterConnectCandidates(candidates, query, limit)
}

func (s ModelService) ConnectTimeoutCandidates(query string, limit int) []ConnectCandidate {
	values := []string{"60", "120", "180"}
	out := make([]ConnectCandidate, 0, len(values))
	for _, value := range values {
		out = append(out, ConnectCandidate{Value: value, Display: value, Detail: value + "s"})
	}
	return filterConnectCandidates(out, query, limit)
}

func (s ModelService) ConnectModelCandidates(ctx context.Context, cfg appsettings.ModelConfig, query string, limit int) ([]ConnectCandidate, error) {
	cfg = appsettings.NormalizeModelConfig(cfg)
	tpl, ok := FindConnectProviderTemplate(cfg.Provider)
	if !ok {
		return nil, nil
	}
	if tpl.Provider == "codefree" {
		baseURL := strings.TrimSpace(cfg.BaseURL)
		if baseURL == "" {
			baseURL = tpl.DefaultBaseURL
		}
		if _, err := s.EnsureCodeFreeModelSelectionAuth(ctx, CodeFreeAuthRequest{
			BaseURL:         baseURL,
			OpenBrowser:     true,
			CallbackTimeout: 5 * time.Minute,
		}); err != nil {
			return nil, err
		}
	}
	fallback := s.fallbackConnectModels(tpl)
	discoverCtx, cancel := providerModelDiscoveryContext(ctx)
	defer cancel()
	discovered, _ := s.ProviderModels(discoverCtx, cfg)
	fallback = append(fallback, discovered...)
	choices := s.buildConnectModelChoices(tpl.Provider, fallback)
	out := make([]ConnectCandidate, 0, len(choices))
	for _, choice := range choices {
		if query != "" && !strings.Contains(strings.ToLower(choice.Value+" "+choice.Display+" "+choice.Detail), strings.ToLower(strings.TrimSpace(query))) {
			continue
		}
		out = append(out, choice)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s ModelService) ConnectDefaults(ctx context.Context, cfg appsettings.ModelConfig) (ConnectModelDefaults, error) {
	_ = ctx
	cfg = appsettings.NormalizeModelConfig(cfg)
	tpl, ok := FindConnectProviderTemplate(cfg.Provider)
	if !ok {
		return ConnectModelDefaults{}, nil
	}
	caps, known := s.LookupCapabilities(tpl.Provider, cfg.Model)
	if !known {
		caps = s.DefaultCapabilities()
	}
	if caps.ContextWindowTokens <= 0 {
		caps.ContextWindowTokens = defaultConnectContextWindow(tpl)
	}
	if caps.DefaultMaxOutputTokens <= 0 {
		caps.DefaultMaxOutputTokens = defaultConnectMaxOutput(tpl)
	}
	if caps.MaxOutputTokens <= 0 {
		caps.MaxOutputTokens = caps.DefaultMaxOutputTokens
	}
	reasoningLevels := normalizeConnectReasoningLevels(s.ReasoningLevels(tpl.Provider, cfg.Model))
	if len(reasoningLevels) == 0 {
		reasoningLevels = normalizeConnectReasoningLevels(caps.ReasoningEfforts)
	}
	contextWindow := caps.ContextWindowTokens
	if contextWindow <= 0 {
		contextWindow = defaultConnectContextWindow(tpl)
	}
	maxOutput := caps.DefaultMaxOutputTokens
	if maxOutput <= 0 {
		maxOutput = caps.MaxOutputTokens
	}
	if maxOutput <= 0 {
		maxOutput = defaultConnectMaxOutput(tpl)
	}
	return ConnectModelDefaults{
		ContextWindow:          contextWindow,
		MaxOutput:              maxOutput,
		ReasoningLevels:        reasoningLevels,
		DefaultReasoningEffort: strings.ToLower(strings.TrimSpace(caps.DefaultReasoningEffort)),
	}, nil
}

func (s ModelService) PrepareConnectConfig(ctx context.Context, cfg appsettings.ModelConfig) (appsettings.ModelConfig, error) {
	tpl, ok := FindConnectProviderTemplate(cfg.Provider)
	if !ok {
		return appsettings.ModelConfig{}, fmt.Errorf("provider %q is not supported", strings.TrimSpace(cfg.Provider))
	}
	cfg.Provider = tpl.Provider
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.Token = strings.TrimSpace(cfg.Token)
	cfg.TokenEnv = strings.TrimSpace(cfg.TokenEnv)
	if env, ok := ParseConnectTokenEnvSpec(cfg.Token); ok {
		cfg.TokenEnv = env
		cfg.Token = ""
		cfg.PersistToken = false
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = tpl.DefaultBaseURL
	}
	endpoint, hasEndpoint := ConnectEndpointForBaseURL(tpl, cfg.BaseURL)
	if strings.TrimSpace(cfg.EndpointID) == "" && hasEndpoint {
		cfg.EndpointID = endpoint.ID
	}
	reusingProfileAuth := !tpl.NoAuthRequired &&
		strings.TrimSpace(cfg.Token) == "" &&
		strings.TrimSpace(cfg.TokenEnv) == "" &&
		s.hasReusableConnectAuth(ctx, tpl.Provider, cfg.BaseURL)
	if err := s.validateConnectConfig(ctx, tpl, cfg); err != nil {
		return appsettings.ModelConfig{}, err
	}
	if tpl.Provider == "codefree" {
		if _, err := s.EnsureCodeFreeAuth(ctx, CodeFreeAuthRequest{
			BaseURL:         cfg.BaseURL,
			OpenBrowser:     true,
			CallbackTimeout: 5 * time.Minute,
		}); err != nil {
			return appsettings.ModelConfig{}, err
		}
	}
	defaults, err := s.ConnectDefaults(ctx, cfg)
	if err != nil {
		return appsettings.ModelConfig{}, err
	}
	if cfg.ContextWindowTokens <= 0 {
		cfg.ContextWindowTokens = defaults.ContextWindow
	}
	if cfg.MaxOutputTokens <= 0 {
		cfg.MaxOutputTokens = defaults.MaxOutput
	}
	if len(cfg.ReasoningLevels) == 0 {
		cfg.ReasoningLevels = defaults.ReasoningLevels
	}
	if cfg.DefaultReasoningEffort == "" {
		cfg.DefaultReasoningEffort = defaults.DefaultReasoningEffort
	}
	if cfg.ReasoningEffort == "" {
		cfg.ReasoningEffort = cfg.DefaultReasoningEffort
	}
	if tpl.NoAuthRequired {
		cfg.AuthType = string(model.AuthNone)
	} else if !reusingProfileAuth && (cfg.Token != "" || cfg.TokenEnv != "" || cfg.AuthType != "") {
		if cfg.AuthType == "" {
			cfg.AuthType = string(DefaultConnectAuthType(tpl.Provider))
		}
	}
	cfg = appsettings.NormalizeModelConfig(cfg)
	if reusingProfileAuth {
		cfg.Token = ""
		cfg.TokenEnv = ""
		cfg.PersistToken = false
		cfg.AuthType = ""
		cfg.HeaderKey = ""
		cfg.Timeout = 0
	}
	return cfg, nil
}

func (s ModelService) validateConnectConfig(ctx context.Context, tpl ConnectProviderTemplate, cfg appsettings.ModelConfig) error {
	if strings.TrimSpace(cfg.Model) == "" {
		return fmt.Errorf("model is required; use /connect and choose or type a model name")
	}
	if baseURL := strings.TrimSpace(cfg.BaseURL); baseURL != "" {
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("base URL is invalid; use a full URL such as %s", tpl.DefaultBaseURL)
		}
	}
	if tpl.NoAuthRequired {
		return nil
	}
	if strings.TrimSpace(cfg.Token) != "" || strings.TrimSpace(cfg.TokenEnv) != "" {
		return nil
	}
	if s.hasReusableConnectAuth(ctx, tpl.Provider, cfg.BaseURL) {
		return nil
	}
	envHint := DefaultConnectTokenEnvName(tpl.Provider, cfg.BaseURL)
	if envHint == "" {
		envHint = "YOUR_API_KEY"
	}
	return fmt.Errorf("API key is missing; paste a key or enter env:%s in /connect", envHint)
}

func (s ModelService) hasReusableConnectAuth(ctx context.Context, provider string, baseURL string) bool {
	if NormalizeConnectBaseURL(baseURL) == "" {
		return false
	}
	choices, err := s.List(ctx)
	if err != nil {
		return false
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	baseURL = NormalizeConnectBaseURL(baseURL)
	for _, choice := range choices {
		if strings.ToLower(strings.TrimSpace(choice.Provider)) != provider {
			continue
		}
		if NormalizeConnectBaseURL(choice.BaseURL) != baseURL {
			continue
		}
		cfg, err := s.Resolve(ctx, choice.ID)
		if err != nil {
			continue
		}
		tpl, _ := FindConnectProviderTemplate(cfg.Provider)
		if tpl.NoAuthRequired ||
			strings.TrimSpace(cfg.Token) != "" ||
			strings.TrimSpace(cfg.TokenEnv) != "" ||
			strings.EqualFold(strings.TrimSpace(cfg.AuthType), string(model.AuthNone)) {
			return true
		}
	}
	return false
}

func connectEndpointCandidatesForTemplate(tpl ConnectProviderTemplate) []ConnectCandidate {
	if len(tpl.Endpoints) == 0 {
		return nil
	}
	out := make([]ConnectCandidate, 0, len(tpl.Endpoints))
	for _, endpoint := range tpl.Endpoints {
		detail := strings.TrimSpace(endpoint.Detail)
		if endpoint.TokenEnv != "" {
			detail = strings.Join(compactConnectNonEmpty([]string{detail, "env:" + endpoint.TokenEnv}), " · ")
		}
		out = append(out, ConnectCandidate{
			Value:   endpoint.BaseURL,
			Display: endpoint.Display,
			Detail:  detail,
		})
	}
	return out
}

func (s ModelService) fallbackConnectModels(tpl ConnectProviderTemplate) []string {
	if models := s.ListCatalogModels(tpl.Provider); len(models) > 0 {
		return models
	}
	if len(tpl.CommonModels) > 0 {
		return append([]string(nil), tpl.CommonModels...)
	}
	return CommonModelsForConnectProvider(tpl.Provider)
}

func (s ModelService) buildConnectModelChoices(provider string, fallbackModels []string) []ConnectCandidate {
	seen := map[string]struct{}{}
	out := make([]ConnectCandidate, 0, len(fallbackModels))
	add := func(name string, detail string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		if strings.TrimSpace(detail) == "" {
			detail = s.describeConnectModel(provider, name)
		}
		out = append(out, ConnectCandidate{
			Value:   name,
			Display: ConnectDisplayModelRef(provider, name),
			Detail:  strings.TrimSpace(detail),
		})
	}
	for _, item := range fallbackModels {
		add(item, "")
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Display) < strings.ToLower(out[j].Display)
	})
	return out
}

func (s ModelService) describeConnectModel(provider string, modelName string) string {
	caps, ok := s.LookupCapabilities(provider, modelName)
	if !ok {
		return "suggested model"
	}
	parts := []string{"catalog preset"}
	if caps.ContextWindowTokens > 0 {
		parts = append(parts, fmt.Sprintf("%dk ctx", caps.ContextWindowTokens/1000))
	}
	if caps.SupportsReasoning {
		parts = append(parts, "reasoning")
	}
	if caps.SupportsToolCalls {
		parts = append(parts, "tools")
	}
	return strings.Join(parts, " · ")
}

func providerModelDiscoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, 3*time.Second)
}

func CommonModelsForConnectProvider(provider string) []string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, tpl := range connectProviderTemplates {
		if tpl.Provider == provider || tpl.Label == provider {
			return append([]string(nil), tpl.CommonModels...)
		}
	}
	return nil
}

func ConnectDisplayModelRef(provider, modelName string) string {
	provider = strings.TrimSpace(provider)
	modelName = strings.TrimSpace(modelName)
	if provider == "" {
		return modelName
	}
	if modelName == "" {
		return provider
	}
	if strings.HasPrefix(strings.ToLower(modelName), strings.ToLower(provider)+"/") {
		return modelName
	}
	return provider + "/" + modelName
}

func defaultConnectContextWindow(tpl ConnectProviderTemplate) int {
	if tpl.DefaultContextTokens > 0 {
		return tpl.DefaultContextTokens
	}
	return 128000
}

func defaultConnectMaxOutput(tpl ConnectProviderTemplate) int {
	if tpl.DefaultMaxOutput > 0 {
		return tpl.DefaultMaxOutput
	}
	return 4096
}

func filterConnectCandidates(candidates []ConnectCandidate, query string, limit int) []ConnectCandidate {
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]ConnectCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if query != "" && !hasConnectCandidatePrefix(query, candidate.Value, candidate.Display, candidate.Detail) {
			continue
		}
		out = append(out, candidate)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func hasConnectCandidatePrefix(query string, values ...string) bool {
	if query == "" {
		return true
	}
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if strings.HasPrefix(normalized, query) {
			return true
		}
	}
	return false
}

func normalizeConnectReasoningLevels(levels []string) []string {
	if len(levels) == 0 {
		return nil
	}
	out := make([]string, 0, len(levels))
	seen := map[string]struct{}{}
	for _, level := range levels {
		trimmed := strings.ToLower(strings.TrimSpace(level))
		if trimmed == "" || trimmed == "-" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func compactConnectNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func cloneConnectProviderTemplate(tpl ConnectProviderTemplate) ConnectProviderTemplate {
	tpl.CommonModels = append([]string(nil), tpl.CommonModels...)
	tpl.Endpoints = cloneConnectEndpoints(tpl.Endpoints)
	return tpl
}

func cloneConnectEndpoints(in []ConnectEndpointTemplate) []ConnectEndpointTemplate {
	if len(in) == 0 {
		return nil
	}
	out := make([]ConnectEndpointTemplate, len(in))
	copy(out, in)
	return out
}
