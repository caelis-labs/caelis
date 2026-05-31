package gatewaydriver

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	"github.com/OnslaughtSnail/caelis/ports/model"
)

const (
	connectVolcengineStandardValue = appservices.ConnectVolcengineStandardValue
	connectVolcengineCodingValue   = appservices.ConnectVolcengineCodingValue

	connectXiaomiAPIBaseURL         = appservices.ConnectXiaomiAPIBaseURL
	connectXiaomiTokenPlanCNBaseURL = appservices.ConnectXiaomiTokenPlanCNBaseURL
	connectXiaomiTokenPlanCNAlias   = appservices.ConnectXiaomiTokenPlanCNAlias
)

type providerTemplate = appservices.ConnectProviderTemplate

type connectEndpointTemplate = appservices.ConnectEndpointTemplate

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

type connectWizardPayload = appservices.ConnectWizardState

var providerTemplates = appservices.ConnectProviderTemplates()

func completeConnectArgs(ctx context.Context, driver *GatewayDriver, command string, query string, limit int) ([]SlashArgCandidate, error) {
	switch {
	case command == "connect":
		if candidates, ok, err := stackForDriver(driver).ConnectProviderCandidates(ctx, query, limit); ok || err != nil {
			return candidates, err
		}
		return completeConnectProviders(query, limit), nil
	case strings.HasPrefix(command, "connect-baseurl:"):
		if candidates, ok, err := stackForDriver(driver).ConnectBaseURLCandidates(ctx, strings.TrimPrefix(command, "connect-baseurl:"), query, limit); ok || err != nil {
			return candidates, err
		}
		return completeConnectBaseURL(ctx, driver, strings.TrimPrefix(command, "connect-baseurl:"), query, limit), nil
	case strings.HasPrefix(command, "connect-timeout:"):
		if candidates, ok, err := stackForDriver(driver).ConnectTimeoutCandidates(ctx, query, limit); ok || err != nil {
			return candidates, err
		}
		return completeConnectTimeout(strings.TrimPrefix(command, "connect-timeout:"), query, limit), nil
	case strings.HasPrefix(command, "connect-apikey:"):
		return nil, nil
	case strings.HasPrefix(command, "connect-model:"):
		return completeConnectModels(ctx, driver, appservices.ParseConnectWizardPayload(strings.TrimPrefix(command, "connect-model:")), query, limit)
	case strings.HasPrefix(command, "connect-context:"):
		return completeConnectContext(ctx, driver, appservices.ParseConnectWizardPayload(strings.TrimPrefix(command, "connect-context:")), query, limit)
	case strings.HasPrefix(command, "connect-maxout:"):
		return completeConnectMaxOutput(ctx, driver, appservices.ParseConnectWizardPayload(strings.TrimPrefix(command, "connect-maxout:")), query, limit)
	case strings.HasPrefix(command, "connect-reasoning-levels:"):
		return completeConnectReasoningLevels(ctx, driver, appservices.ParseConnectWizardPayload(strings.TrimPrefix(command, "connect-reasoning-levels:")), query, limit)
	default:
		return nil, nil
	}
}

func completeConnectProviders(query string, limit int) []SlashArgCandidate {
	out := make([]SlashArgCandidate, 0, len(providerTemplates))
	for _, tpl := range providerTemplates {
		if query != "" && !strings.Contains(strings.ToLower(tpl.Label+" "+tpl.DefaultBaseURL), strings.ToLower(strings.TrimSpace(query))) {
			continue
		}
		detailParts := []string{strings.TrimSpace(tpl.Description), strings.TrimSpace(tpl.DefaultBaseURL)}
		if tpl.Provider == "codefree" {
			detailParts = append(detailParts, "browser oauth")
		} else if tpl.NoAuthRequired {
			detailParts = append(detailParts, "no auth")
		} else if env := defaultTokenEnvName(tpl.Provider); env != "" {
			detailParts = append(detailParts, "env:"+env)
		}
		out = append(out, SlashArgCandidate{
			Value:   tpl.Label,
			Display: tpl.Label,
			Detail:  strings.Join(compactNonEmpty(detailParts), " · "),
			NoAuth:  tpl.NoAuthRequired,
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
		candidates = append(candidates, SlashArgCandidate{Value: tpl.DefaultBaseURL, Display: tpl.DefaultBaseURL, Detail: "default base URL"})
	}
	for i := range candidates {
		if driver != nil && driver.hasReusableConnectAuth(ctx, tpl.Provider, candidates[i].Value) {
			candidates[i].NoAuth = true
			candidates[i].Detail = strings.Join(compactNonEmpty([]string{strings.TrimSpace(candidates[i].Detail), "configured auth"}), " · ")
		}
	}
	return filterSlashArgCandidates(candidates, query, limit)
}

func connectEndpointCandidates(tpl providerTemplate) []SlashArgCandidate {
	if len(tpl.Endpoints) == 0 {
		return nil
	}
	out := make([]SlashArgCandidate, 0, len(tpl.Endpoints))
	for _, endpoint := range tpl.Endpoints {
		detail := strings.TrimSpace(endpoint.Detail)
		if endpoint.TokenEnv != "" {
			detail = strings.Join(compactNonEmpty([]string{detail, "env:" + endpoint.TokenEnv}), " · ")
		}
		out = append(out, SlashArgCandidate{
			Value:   endpoint.BaseURL,
			Display: endpoint.Display,
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
	if candidates, handled, err := stackForDriver(driver).ConnectModelCandidates(ctx, connectModelConfigFromPayload(tpl, payload), query, limit); handled || err != nil {
		return candidates, err
	}
	if tpl.Provider == "codefree" {
		baseURL := strings.TrimSpace(payload.BaseURL)
		if baseURL == "" {
			baseURL = tpl.DefaultBaseURL
		}
		if driver == nil || driver.stack == nil {
			return nil, fmt.Errorf("surfaces/tui/gatewaydriver: codefree model auth dependency is unavailable")
		}
		if err := driver.stack.EnsureCodeFreeModelSelectionAuth(ctx, CodeFreeAuthRequest{
			BaseURL:         baseURL,
			OpenBrowser:     true,
			CallbackTimeout: 5 * time.Minute,
		}); err != nil {
			return nil, err
		}
	}
	fallbackModels := fallbackConnectModels(stackForDriver(driver), tpl)
	if driver != nil && driver.stack != nil {
		discoverCtx, cancel := providerModelDiscoveryContext(ctx)
		defer cancel()
		discovered, _ := driver.stack.ListProviderModelsForConfig(discoverCtx, connectModelConfigFromPayload(tpl, payload))
		if len(discovered) == 0 {
			discovered = driver.stack.ListProviderModels(tpl.Provider)
		}
		fallbackModels = append(fallbackModels, discovered...)
	}
	choices := buildConnectModelChoices(stackForDriver(driver), tpl.Provider, fallbackModels)
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

func providerModelDiscoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, 3*time.Second)
}

func connectDefaultsForConfig(ctx context.Context, cfg ConnectConfig) (connectModelDefaults, error) {
	return connectDefaultsForConfigWithStack(ctx, nil, cfg)
}

func connectDefaultsForConfigWithStack(ctx context.Context, stack *DriverStack, cfg ConnectConfig) (connectModelDefaults, error) {
	tpl, ok := findProviderTemplate(strings.ToLower(strings.TrimSpace(cfg.Provider)))
	if !ok {
		return connectModelDefaults{}, nil
	}
	if defaults, handled, err := stack.ConnectDefaults(ctx, ModelConfig{
		Provider:            strings.ToLower(strings.TrimSpace(cfg.Provider)),
		EndpointID:          strings.TrimSpace(cfg.EndpointID),
		Model:               strings.TrimSpace(cfg.Model),
		BaseURL:             strings.TrimSpace(cfg.BaseURL),
		Token:               strings.TrimSpace(cfg.APIKey),
		TokenEnv:            strings.TrimSpace(cfg.TokenEnv),
		ContextWindowTokens: cfg.ContextWindowTokens,
		MaxOutputTok:        cfg.MaxOutputTokens,
		ReasoningLevels:     append([]string(nil), cfg.ReasoningLevels...),
	}); handled || err != nil {
		return defaults, err
	}
	payload := appservices.ConnectWizardState{
		Provider:       strings.ToLower(strings.TrimSpace(cfg.Provider)),
		BaseURL:        strings.TrimSpace(cfg.BaseURL),
		TimeoutSeconds: cfg.TimeoutSeconds,
		TokenRef:       strings.TrimSpace(cfg.APIKey),
		Model:          strings.TrimSpace(cfg.Model),
	}
	if payload.BaseURL == "" {
		payload.BaseURL = tpl.DefaultBaseURL
	}
	if payload.TimeoutSeconds <= 0 {
		payload.TimeoutSeconds = appservices.ConnectDefaultTimeoutSeconds
	}
	return connectDefaultsForPayload(ctx, stack, payload)
}

func completeConnectContext(ctx context.Context, driver *GatewayDriver, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	defaults, err := connectDefaultsForPayload(ctx, stackForDriver(driver), payload)
	if err != nil {
		return nil, err
	}
	return filterSlashArgCandidates([]SlashArgCandidate{{Value: strconv.Itoa(defaults.ContextWindow), Display: strconv.Itoa(defaults.ContextWindow), Detail: "context window tokens"}}, query, limit), nil
}

func completeConnectMaxOutput(ctx context.Context, driver *GatewayDriver, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	defaults, err := connectDefaultsForPayload(ctx, stackForDriver(driver), payload)
	if err != nil {
		return nil, err
	}
	return filterSlashArgCandidates([]SlashArgCandidate{{Value: strconv.Itoa(defaults.MaxOutput), Display: strconv.Itoa(defaults.MaxOutput), Detail: "max output tokens"}}, query, limit), nil
}

func completeConnectReasoningLevels(ctx context.Context, driver *GatewayDriver, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	defaults, err := connectDefaultsForPayload(ctx, stackForDriver(driver), payload)
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

func connectDefaultsForPayload(ctx context.Context, stack *DriverStack, payload connectWizardPayload) (connectModelDefaults, error) {
	tpl, ok := findProviderTemplate(payload.Provider)
	if !ok {
		return connectModelDefaults{}, nil
	}
	if defaults, handled, err := stack.ConnectDefaults(ctx, connectModelConfigFromPayload(tpl, payload)); handled || err != nil {
		return defaults, err
	}
	_ = ctx
	baseCaps, baseKnown := lookupConnectModelCapabilities(stack, tpl.Provider, payload.Model)
	if !baseKnown {
		baseCaps = defaultConnectCapabilities(stack)
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
	reasoningLevels := normalizeReasoningLevels(reasoningLevelsForModel(stack, tpl.Provider, payload.Model))
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
	return appservices.FindConnectProviderTemplate(value)
}

func normalizedConnectBaseURL(baseURL string) string {
	return appservices.NormalizeConnectBaseURL(baseURL)
}

func connectEndpointForBaseURL(tpl providerTemplate, baseURL string) (connectEndpointTemplate, bool) {
	return appservices.ConnectEndpointForBaseURL(tpl, baseURL)
}

func buildConnectModelChoices(stack *DriverStack, provider string, fallbackModels []string) []connectModelChoice {
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
			detail = describeConnectModel(stack, provider, name)
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

func connectModelConfigFromPayload(tpl providerTemplate, payload connectWizardPayload) ModelConfig {
	baseURL := strings.TrimSpace(payload.BaseURL)
	if baseURL == "" {
		baseURL = tpl.DefaultBaseURL
	}
	timeoutSeconds := payload.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = appservices.ConnectDefaultTimeoutSeconds
	}
	endpoint, hasEndpoint := connectEndpointForBaseURL(tpl, baseURL)
	api := tpl.API
	if hasEndpoint && strings.TrimSpace(string(endpoint.API)) != "" {
		api = endpoint.API
	}
	authType := defaultConnectAuthType(tpl.Provider)
	if tpl.NoAuthRequired {
		authType = model.AuthNone
	}
	cfg := ModelConfig{
		Provider:            tpl.Provider,
		API:                 api,
		Model:               strings.TrimSpace(payload.Model),
		BaseURL:             baseURL,
		AuthType:            authType,
		ContextWindowTokens: payload.ContextWindowTokens,
		MaxOutputTok:        payload.MaxOutputTokens,
		Timeout:             time.Duration(timeoutSeconds) * time.Second,
	}
	if hasEndpoint {
		cfg.EndpointID = strings.TrimSpace(endpoint.ID)
	}
	tokenRef := strings.TrimSpace(payload.TokenRef)
	if env, ok := parseTokenEnvSpec(tokenRef); ok {
		cfg.TokenEnv = env
	} else {
		cfg.Token = tokenRef
	}
	return cfg
}

func fallbackConnectModels(stack *DriverStack, tpl providerTemplate) []string {
	if stack != nil {
		if models := stack.ListCatalogModels(tpl.Provider); len(models) > 0 {
			return models
		}
	}
	if len(tpl.CommonModels) > 0 {
		return append([]string(nil), tpl.CommonModels...)
	}
	return commonModelsForProvider(tpl.Provider)
}

func connectDisplayModelRef(provider, modelName string) string {
	return appservices.ConnectDisplayModelRef(provider, modelName)
}

func defaultContextWindowForTemplate(tpl providerTemplate) int {
	if tpl.DefaultContextTokens > 0 {
		return tpl.DefaultContextTokens
	}
	return 128000
}

func defaultMaxOutputForTemplate(tpl providerTemplate) int {
	if tpl.DefaultMaxOutput > 0 {
		return tpl.DefaultMaxOutput
	}
	return 4096
}

func commonModelsForProvider(provider string) []string {
	return appservices.CommonModelsForConnectProvider(provider)
}

func describeConnectModel(stack *DriverStack, provider string, modelName string) string {
	caps, ok := lookupConnectModelCapabilities(stack, provider, modelName)
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

func stackForDriver(driver *GatewayDriver) *DriverStack {
	if driver == nil {
		return nil
	}
	return driver.stack
}

func defaultConnectCapabilities(stack *DriverStack) ModelCapabilityInfo {
	if stack == nil {
		return ModelCapabilityInfo{
			ContextWindowTokens:    128000,
			DefaultMaxOutputTokens: 4096,
			MaxOutputTokens:        4096,
		}
	}
	return stack.DefaultModelCapabilities()
}

func lookupConnectModelCapabilities(stack *DriverStack, provider string, modelName string) (ModelCapabilityInfo, bool) {
	if stack == nil {
		return ModelCapabilityInfo{}, false
	}
	return stack.LookupModelCapabilities(provider, modelName)
}

func reasoningLevelsForModel(stack *DriverStack, provider string, modelName string) []string {
	if stack == nil {
		return nil
	}
	return stack.ReasoningLevelsForModel(provider, modelName)
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
