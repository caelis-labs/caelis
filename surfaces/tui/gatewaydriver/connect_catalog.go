package gatewaydriver

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/model/catalog"
	"github.com/OnslaughtSnail/caelis/impl/model/providers"
)

const (
	connectVolcengineStandardValue = "standard"
	connectVolcengineCodingValue   = "coding-plan"

	connectXiaomiAPIBaseURL         = "https://api.xiaomimimo.com/v1"
	connectXiaomiTokenPlanCNBaseURL = "https://token-plan-cn.xiaomimimo.com/v1"
	connectXiaomiTokenPlanCNAlias   = "xiaomi-token-plan-cn"
)

type providerTemplate struct {
	label               string
	api                 providers.APIType
	provider            string
	description         string
	defaultEndpointID   string
	defaultBaseURL      string
	defaultContextToken int
	defaultMaxOutputTok int
	noAuthRequired      bool
	commonModels        []string
	endpoints           []connectEndpointTemplate
}

type connectEndpointTemplate struct {
	id       string
	baseURL  string
	display  string
	detail   string
	api      providers.APIType
	tokenEnv string
}

var xiaomiMimoCommonModels = []string{"mimo-v2.5-pro", "mimo-v2-pro", "mimo-v2.5", "mimo-v2-omni", "mimo-v2-flash"}

var connectXiaomiEndpoints = []connectEndpointTemplate{
	{id: "api-cn", baseURL: connectXiaomiAPIBaseURL, display: "api cn", detail: "Xiaomi MiMo API CN · OpenAI-compatible", api: providers.APIMimo, tokenEnv: "XIAOMI_API_KEY"},
	{id: "token-plan-cn", baseURL: connectXiaomiTokenPlanCNBaseURL, display: "token plan cn", detail: "Xiaomi MiMo Token Plan CN · OpenAI-compatible", api: providers.APIMimo, tokenEnv: "MIMO_TOKEN_PLAN_API_KEY"},
}

var connectVolcengineEndpoints = []connectEndpointTemplate{
	{id: connectVolcengineStandardValue, baseURL: "https://ark.cn-beijing.volces.com/api/v3", display: "standard api", detail: "regular Ark endpoint", api: providers.APIVolcengine, tokenEnv: "VOLCENGINE_API_KEY"},
	{id: connectVolcengineCodingValue, baseURL: "https://ark.cn-beijing.volces.com/api/coding/v3", display: "coding plan", detail: "Ark coding-plan endpoint", api: providers.APIVolcengineCoding, tokenEnv: "VOLCENGINE_API_KEY"},
}

type connectModelChoice struct {
	Name    string
	Display string
	Detail  string
}

type connectModelDefaults struct {
	ContextWindow          int
	MaxOutput              int
	ReasoningLevels        []string
	DefaultReasoningEffort string
}

type connectWizardPayload struct {
	Provider string
	BaseURL  string
	Timeout  string
	APIKey   string
	Model    string
}

var providerTemplates = []providerTemplate{
	{label: "openai", api: providers.APIOpenAI, provider: "openai", description: "OpenAI-hosted models", defaultBaseURL: "https://api.openai.com/v1", defaultContextToken: 128000, commonModels: []string{"gpt-4o", "gpt-4o-mini", "o3", "o4-mini"}},
	{label: "openai-compatible", api: providers.APIOpenAICompatible, provider: "openai-compatible", description: "OpenAI-compatible proxy or self-hosted endpoint", defaultBaseURL: "https://api.openai.com/v1", defaultContextToken: 128000, commonModels: []string{"gpt-4o", "gpt-4o-mini", "o3", "o4-mini"}},
	{label: "codefree", api: providers.APICodeFree, provider: "codefree", description: "China Telecom SRD CodeFree models via browser OAuth", defaultBaseURL: "https://www.srdcloud.cn", defaultContextToken: 88000, defaultMaxOutputTok: 8000, noAuthRequired: true, commonModels: []string{"GLM-4.7", "DeepSeek-V3.1-Terminus", "Qwen3.5-122B-A10B", "GLM-5.1"}},
	{label: "openrouter", api: providers.APIOpenRouter, provider: "openrouter", description: "OpenRouter multi-provider routing", defaultBaseURL: "https://openrouter.ai/api/v1", defaultContextToken: 262144, commonModels: []string{"openai/gpt-4o-mini", "anthropic/claude-sonnet-4", "google/gemini-2.5-flash"}},
	{label: "gemini", api: providers.APIGemini, provider: "gemini", description: "Google Gemini API", defaultBaseURL: "https://generativelanguage.googleapis.com/v1beta", defaultContextToken: 128000, commonModels: []string{"gemini-2.5-flash", "gemini-2.5-pro"}},
	{label: "anthropic", api: providers.APIAnthropic, provider: "anthropic", description: "Anthropic Claude API", defaultBaseURL: "https://api.anthropic.com", defaultContextToken: 200000, defaultMaxOutputTok: 1024, commonModels: []string{"claude-sonnet-4-20250514", "claude-opus-4-20250514"}},
	{label: "anthropic-compatible", api: providers.APIAnthropicCompatible, provider: "anthropic-compatible", description: "Anthropic-compatible proxy or self-hosted endpoint", defaultBaseURL: "https://api.anthropic.com", defaultContextToken: 200000, defaultMaxOutputTok: 1024},
	{label: "deepseek", api: providers.APIDeepSeek, provider: "deepseek", description: "DeepSeek V4 models", defaultBaseURL: "https://api.deepseek.com/v1", defaultContextToken: 1048576, commonModels: []string{"deepseek-v4-flash", "deepseek-v4-pro"}},
	{label: "xiaomi", api: providers.APIMimo, provider: "xiaomi", description: "Xiaomi Mimo models", defaultEndpointID: "api-cn", defaultBaseURL: connectXiaomiAPIBaseURL, defaultContextToken: 262144, commonModels: xiaomiMimoCommonModels, endpoints: connectXiaomiEndpoints},
	{label: "minimax", api: providers.APIMiniMax, provider: "minimax", description: "MiniMax models over an Anthropic-compatible API", defaultBaseURL: "https://api.minimaxi.com/anthropic", defaultContextToken: 204800, defaultMaxOutputTok: 8192, commonModels: []string{"MiniMax-M2.7", "MiniMax-M2.7-highspeed", "MiniMax-M2.5", "MiniMax-M2.5-highspeed", "MiniMax-M2.1", "MiniMax-M2.1-highspeed", "MiniMax-M2"}},
	{label: "volcengine", api: providers.APIVolcengine, provider: "volcengine", description: "Volcengine Ark standard or coding-plan endpoints", defaultEndpointID: connectVolcengineStandardValue, defaultBaseURL: "https://ark.cn-beijing.volces.com/api/v3", defaultContextToken: 128000, endpoints: connectVolcengineEndpoints},
	{label: "ollama", api: providers.APIOllama, provider: "ollama", description: "Local Ollama runtime", defaultBaseURL: "http://localhost:11434", defaultContextToken: 128000, noAuthRequired: true, commonModels: []string{"qwen2.5:7b", "llama3.1:8b", "deepseek-r1:7b", "gemma3:4b"}},
}

func completeConnectArgs(ctx context.Context, driver *GatewayDriver, command string, query string, limit int) ([]SlashArgCandidate, error) {
	switch {
	case command == "connect":
		return completeConnectProviders(query, limit), nil
	case strings.HasPrefix(command, "connect-baseurl:"):
		return completeConnectBaseURL(ctx, driver, strings.TrimPrefix(command, "connect-baseurl:"), query, limit), nil
	case strings.HasPrefix(command, "connect-timeout:"):
		return completeConnectTimeout(strings.TrimPrefix(command, "connect-timeout:"), query, limit), nil
	case strings.HasPrefix(command, "connect-apikey:"):
		return nil, nil
	case strings.HasPrefix(command, "connect-model:"):
		return completeConnectModels(ctx, driver, parseConnectWizardPayload(strings.TrimPrefix(command, "connect-model:")), query, limit)
	case strings.HasPrefix(command, "connect-context:"):
		return completeConnectContext(ctx, parseConnectWizardPayload(strings.TrimPrefix(command, "connect-context:")), query, limit)
	case strings.HasPrefix(command, "connect-maxout:"):
		return completeConnectMaxOutput(ctx, parseConnectWizardPayload(strings.TrimPrefix(command, "connect-maxout:")), query, limit)
	case strings.HasPrefix(command, "connect-reasoning-levels:"):
		return completeConnectReasoningLevels(ctx, parseConnectWizardPayload(strings.TrimPrefix(command, "connect-reasoning-levels:")), query, limit)
	default:
		return nil, nil
	}
}

func completeConnectProviders(query string, limit int) []SlashArgCandidate {
	out := make([]SlashArgCandidate, 0, len(providerTemplates))
	for _, tpl := range providerTemplates {
		if query != "" && !strings.Contains(strings.ToLower(tpl.label+" "+tpl.defaultBaseURL), strings.ToLower(strings.TrimSpace(query))) {
			continue
		}
		detailParts := []string{strings.TrimSpace(tpl.description), strings.TrimSpace(tpl.defaultBaseURL)}
		if tpl.provider == "codefree" {
			detailParts = append(detailParts, "browser oauth")
		} else if tpl.noAuthRequired {
			detailParts = append(detailParts, "no auth")
		} else if env := defaultTokenEnvName(tpl.provider); env != "" {
			detailParts = append(detailParts, "env:"+env)
		}
		out = append(out, SlashArgCandidate{
			Value:   tpl.label,
			Display: tpl.label,
			Detail:  strings.Join(compactNonEmpty(detailParts), " · "),
			NoAuth:  tpl.noAuthRequired,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func completeConnectBaseURL(ctx context.Context, driver *GatewayDriver, provider string, query string, limit int) []SlashArgCandidate {
	tpl, ok := findProviderTemplate(provider)
	if !ok {
		return nil
	}
	candidates := connectEndpointCandidates(tpl)
	if len(candidates) == 0 {
		candidates = append(candidates, SlashArgCandidate{Value: tpl.defaultBaseURL, Display: tpl.defaultBaseURL, Detail: "default base URL"})
	}
	for i := range candidates {
		if driver != nil && driver.hasReusableConnectAuth(ctx, tpl.provider, candidates[i].Value) {
			candidates[i].NoAuth = true
			candidates[i].Detail = strings.Join(compactNonEmpty([]string{strings.TrimSpace(candidates[i].Detail), "configured auth"}), " · ")
		}
	}
	return filterSlashArgCandidates(candidates, query, limit)
}

func connectEndpointCandidates(tpl providerTemplate) []SlashArgCandidate {
	if len(tpl.endpoints) == 0 {
		return nil
	}
	out := make([]SlashArgCandidate, 0, len(tpl.endpoints))
	for _, endpoint := range tpl.endpoints {
		detail := strings.TrimSpace(endpoint.detail)
		if endpoint.tokenEnv != "" {
			detail = strings.Join(compactNonEmpty([]string{detail, "env:" + endpoint.tokenEnv}), " · ")
		}
		out = append(out, SlashArgCandidate{
			Value:   endpoint.baseURL,
			Display: endpoint.display,
			Detail:  detail,
		})
	}
	return out
}

func completeConnectTimeout(provider string, query string, limit int) []SlashArgCandidate {
	values := []string{"60", "120", "180"}
	out := make([]SlashArgCandidate, 0, len(values))
	for _, value := range values {
		out = append(out, SlashArgCandidate{Value: value, Display: value, Detail: fmt.Sprintf("%ss", value)})
	}
	_ = provider
	return filterSlashArgCandidates(out, query, limit)
}

func completeConnectModels(ctx context.Context, driver *GatewayDriver, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	tpl, ok := findProviderTemplate(payload.Provider)
	if !ok {
		return nil, nil
	}
	if tpl.provider == "codefree" {
		baseURL := strings.TrimSpace(payload.BaseURL)
		if baseURL == "" {
			baseURL = tpl.defaultBaseURL
		}
		if _, err := providers.CodeFreeEnsureModelSelectionAuth(ctx, providers.CodeFreeEnsureAuthOptions{
			BaseURL:         baseURL,
			OpenBrowser:     true,
			CallbackTimeout: 5 * time.Minute,
		}); err != nil {
			return nil, err
		}
	}
	fallbackModels := fallbackConnectModels(tpl)
	if driver != nil && driver.stack != nil {
		fallbackModels = append(fallbackModels, driver.stack.ListProviderModels(tpl.provider)...)
	}
	choices := buildConnectModelChoices(tpl.provider, fallbackModels)
	out := make([]SlashArgCandidate, 0, len(choices))
	for _, choice := range choices {
		if query != "" && !strings.Contains(strings.ToLower(choice.Name+" "+choice.Display+" "+choice.Detail), strings.ToLower(strings.TrimSpace(query))) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:   choice.Name,
			Display: choice.Display,
			Detail:  choice.Detail,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func connectDefaultsForConfig(ctx context.Context, cfg ConnectConfig) (connectModelDefaults, error) {
	tpl, ok := findProviderTemplate(strings.ToLower(strings.TrimSpace(cfg.Provider)))
	if !ok {
		return connectModelDefaults{}, nil
	}
	payload := connectWizardPayload{
		Provider: strings.ToLower(strings.TrimSpace(cfg.Provider)),
		BaseURL:  strings.TrimSpace(cfg.BaseURL),
		Timeout:  strconv.Itoa(cfg.TimeoutSeconds),
		APIKey:   strings.TrimSpace(cfg.APIKey),
		Model:    strings.TrimSpace(cfg.Model),
	}
	if payload.BaseURL == "" {
		payload.BaseURL = tpl.defaultBaseURL
	}
	if strings.TrimSpace(payload.Timeout) == "" || strings.TrimSpace(payload.Timeout) == "0" {
		payload.Timeout = "60"
	}
	return connectDefaultsForPayload(ctx, payload)
}

func completeConnectContext(ctx context.Context, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	defaults, err := connectDefaultsForPayload(ctx, payload)
	if err != nil {
		return nil, err
	}
	return filterSlashArgCandidates([]SlashArgCandidate{{Value: strconv.Itoa(defaults.ContextWindow), Display: strconv.Itoa(defaults.ContextWindow), Detail: "context window tokens"}}, query, limit), nil
}

func completeConnectMaxOutput(ctx context.Context, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	defaults, err := connectDefaultsForPayload(ctx, payload)
	if err != nil {
		return nil, err
	}
	return filterSlashArgCandidates([]SlashArgCandidate{{Value: strconv.Itoa(defaults.MaxOutput), Display: strconv.Itoa(defaults.MaxOutput), Detail: "max output tokens"}}, query, limit), nil
}

func completeConnectReasoningLevels(ctx context.Context, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	defaults, err := connectDefaultsForPayload(ctx, payload)
	if err != nil {
		return nil, err
	}
	value := "-"
	detail := "no reasoning levels"
	if len(defaults.ReasoningLevels) > 0 {
		value = strings.Join(defaults.ReasoningLevels, ",")
		detail = "suggested reasoning levels"
	}
	return filterSlashArgCandidates([]SlashArgCandidate{{Value: value, Display: value, Detail: detail}}, query, limit), nil
}

func connectDefaultsForPayload(ctx context.Context, payload connectWizardPayload) (connectModelDefaults, error) {
	tpl, ok := findProviderTemplate(payload.Provider)
	if !ok {
		return connectModelDefaults{}, nil
	}
	_ = ctx
	baseCaps, baseKnown := modelcatalog.LookupModelCapabilities(tpl.provider, payload.Model)
	if !baseKnown {
		baseCaps = modelcatalog.DefaultModelCapabilities()
	}
	if baseCaps.ContextWindowTokens <= 0 {
		baseCaps.ContextWindowTokens = defaultContextWindowForTemplate(tpl)
	}
	if baseCaps.DefaultMaxOutputTokens <= 0 {
		baseCaps.DefaultMaxOutputTokens = defaultMaxOutputForTemplate(tpl)
	}
	if baseCaps.MaxOutputTokens <= 0 {
		baseCaps.MaxOutputTokens = baseCaps.DefaultMaxOutputTokens
	}
	reasoningLevels := normalizeReasoningLevels(modelcatalog.ReasoningLevelsForModel(tpl.provider, payload.Model))
	if len(reasoningLevels) == 0 {
		reasoningLevels = normalizeReasoningLevels(baseCaps.ReasoningEfforts)
	}
	defaultReasoningEffort := strings.ToLower(strings.TrimSpace(baseCaps.DefaultReasoningEffort))
	contextWindow := baseCaps.ContextWindowTokens
	if contextWindow <= 0 {
		contextWindow = defaultContextWindowForTemplate(tpl)
	}
	maxOutput := baseCaps.DefaultMaxOutputTokens
	if maxOutput <= 0 {
		maxOutput = baseCaps.MaxOutputTokens
	}
	if maxOutput <= 0 {
		maxOutput = defaultMaxOutputForTemplate(tpl)
	}
	return connectModelDefaults{
		ContextWindow:          contextWindow,
		MaxOutput:              maxOutput,
		ReasoningLevels:        reasoningLevels,
		DefaultReasoningEffort: defaultReasoningEffort,
	}, nil
}

func parseConnectWizardPayload(raw string) connectWizardPayload {
	parts := strings.SplitN(raw, "|", 5)
	for len(parts) < 5 {
		parts = append(parts, "")
	}
	return connectWizardPayload{
		Provider: strings.TrimSpace(parts[0]),
		BaseURL:  decodeConnectWizardPart(parts[1]),
		Timeout:  strings.TrimSpace(parts[2]),
		APIKey:   decodeConnectWizardPart(parts[3]),
		Model:    decodeConnectWizardPart(parts[4]),
	}
}

func decodeConnectWizardPart(value string) string {
	decoded, err := url.QueryUnescape(strings.TrimSpace(value))
	if err != nil {
		return strings.TrimSpace(value)
	}
	return decoded
}

func filterSlashArgCandidates(candidates []SlashArgCandidate, query string, limit int) []SlashArgCandidate {
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]SlashArgCandidate, 0, len(candidates))
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

func findProviderTemplate(value string) (providerTemplate, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, tpl := range providerTemplates {
		if tpl.label == value || tpl.provider == value {
			return tpl, true
		}
	}
	switch value {
	case connectXiaomiTokenPlanCNAlias:
		return providerTemplate{
			label:               connectXiaomiTokenPlanCNAlias,
			api:                 providers.APIMimo,
			provider:            "xiaomi",
			description:         "Xiaomi Mimo Token Plan CN over an OpenAI-compatible API",
			defaultEndpointID:   "token-plan-cn",
			defaultBaseURL:      connectXiaomiTokenPlanCNBaseURL,
			defaultContextToken: 1048576,
			commonModels:        xiaomiMimoCommonModels,
			endpoints:           connectXiaomiEndpoints,
		}, true
	}
	return providerTemplate{}, false
}

func normalizedConnectBaseURL(baseURL string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
}

func connectEndpointForBaseURL(tpl providerTemplate, baseURL string) (connectEndpointTemplate, bool) {
	normalized := normalizedConnectBaseURL(baseURL)
	for _, endpoint := range tpl.endpoints {
		if normalized != "" && normalized == normalizedConnectBaseURL(endpoint.baseURL) {
			return endpoint, true
		}
	}
	if normalized == "" {
		for _, endpoint := range tpl.endpoints {
			if strings.EqualFold(strings.TrimSpace(endpoint.id), strings.TrimSpace(tpl.defaultEndpointID)) {
				return endpoint, true
			}
		}
	}
	return connectEndpointTemplate{}, false
}

func buildConnectModelChoices(provider string, fallbackModels []string) []connectModelChoice {
	seen := map[string]struct{}{}
	out := make([]connectModelChoice, 0, len(fallbackModels))
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
			detail = describeConnectModel(provider, name)
		}
		out = append(out, connectModelChoice{
			Name:    name,
			Display: connectDisplayModelRef(provider, name),
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

func fallbackConnectModels(tpl providerTemplate) []string {
	if models := modelcatalog.ListCatalogModels(tpl.provider); len(models) > 0 {
		return models
	}
	if len(tpl.commonModels) > 0 {
		return append([]string(nil), tpl.commonModels...)
	}
	return commonModelsForProvider(tpl.provider)
}

func connectDisplayModelRef(provider, modelName string) string {
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

func defaultContextWindowForTemplate(tpl providerTemplate) int {
	if tpl.defaultContextToken > 0 {
		return tpl.defaultContextToken
	}
	return 128000
}

func defaultMaxOutputForTemplate(tpl providerTemplate) int {
	if tpl.defaultMaxOutputTok > 0 {
		return tpl.defaultMaxOutputTok
	}
	return 4096
}

func commonModelsForProvider(provider string) []string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, tpl := range providerTemplates {
		if tpl.provider == provider || tpl.label == provider {
			return append([]string(nil), tpl.commonModels...)
		}
	}
	return nil
}

func describeConnectModel(provider, modelName string) string {
	caps, ok := modelcatalog.LookupModelCapabilities(provider, modelName)
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

func compactNonEmpty(values []string) []string {
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

func connectRemoteCapabilities(remote *providers.RemoteModel) (supportsToolCalls bool, supportsReasoning bool, supportsImages bool, supportsJSON bool, known bool) {
	if remote == nil {
		return false, false, false, false, false
	}
	for _, cap := range remote.Capabilities {
		switch strings.ToLower(strings.TrimSpace(cap)) {
		case "tools", "tool_call", "tool_calls", "function_calling", "function-calling":
			supportsToolCalls = true
			known = true
		case "reasoning", "thinking":
			supportsReasoning = true
			known = true
		case "image", "images", "vision":
			supportsImages = true
			known = true
		case "json", "structured_output", "structured-output":
			supportsJSON = true
			known = true
		}
	}
	return
}

func normalizeReasoningLevels(levels []string) []string {
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
