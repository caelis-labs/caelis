package providers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/model"
)

const defaultDeepSeekBaseURL = "https://api.deepseek.com/v1"
const defaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
const defaultMimoBaseURL = "https://api.xiaomimimo.com/v1"
const defaultVolcengineBaseURL = "https://ark.cn-beijing.volces.com/api/v3"
const defaultVolcengineCodingBaseURL = "https://ark.cn-beijing.volces.com/api/coding/v3"

const (
	openRouterDefaultReferer = "https://github.com/OnslaughtSnail/caelis"
	openRouterDefaultTitle   = "Caelis"
)

const thinkingModeMinTokens = 32768

const (
	deepSeekDefaultMaxTokens = 32768
	deepSeekMaxTokens        = 393216
)

// OpenAICompatConfig configures OpenAI-compatible chat-completions providers.
type OpenAICompatConfig struct {
	Name                    string
	Provider                string
	BaseURL                 string
	Token                   string
	Model                   string
	Headers                 map[string]string
	HTTPClient              *http.Client
	Timeout                 time.Duration
	StreamFirstEventTimeout time.Duration
	MaxOutputTok            int
	OpenRouter              OpenRouterConfig
}

// OpenRouterConfig carries OpenRouter-native routing and provider options.
// Zero values leave the corresponding OpenRouter API defaults in effect.
type OpenRouterConfig struct {
	Models     []string
	Route      string
	Provider   map[string]any
	Transforms []string
	Plugins    []map[string]any
}

type openAICompatProfile struct {
	provider       string
	defaultBaseURL string
	output         func(*openAIRequestPayload, *model.OutputSpec)
	reasoning      func(*openAIRequestPayload, model.ReasoningConfig)
	request        func(*openAIRequestPayload)
}

// NewOpenAICompatible creates a generic OpenAI-compatible provider.
func NewOpenAICompatible(cfg OpenAICompatConfig) *OpenAIProvider {
	return newOpenAICompatibleWithProfile(cfg, openAICompatProfile{
		provider:       firstNonEmpty(cfg.Provider, "openai-compatible"),
		defaultBaseURL: cfg.BaseURL,
	})
}

// NewDeepSeek creates a DeepSeek OpenAI-compatible provider.
func NewDeepSeek(cfg OpenAICompatConfig) *OpenAIProvider {
	return newOpenAICompatibleWithProfile(cfg, openAICompatProfile{
		provider:       "deepseek",
		defaultBaseURL: defaultDeepSeekBaseURL,
		output:         applyDeepSeekOutput,
		reasoning:      applyDeepSeekReasoning,
	})
}

// NewOpenRouter creates an OpenRouter OpenAI-compatible provider with native routing support.
func NewOpenRouter(cfg OpenAICompatConfig) *OpenAIProvider {
	headers := openRouterHeaders(cfg.Headers)
	cfg.Headers = headers
	openRouter := cloneOpenRouterConfig(cfg.OpenRouter)
	return newOpenAICompatibleWithProfile(cfg, openAICompatProfile{
		provider:       "openrouter",
		defaultBaseURL: defaultOpenRouterBaseURL,
		output:         applyOpenRouterOutput,
		reasoning:      applyOpenAIReasoning,
		request: func(payload *openAIRequestPayload) {
			applyOpenRouterPayload(payload, openRouter)
		},
	})
}

// NewMimo creates a Xiaomi MiMo OpenAI-compatible provider.
func NewMimo(cfg OpenAICompatConfig) *OpenAIProvider {
	return newOpenAICompatibleWithProfile(cfg, openAICompatProfile{
		provider:       "mimo",
		defaultBaseURL: defaultMimoBaseURL,
		output:         applyJSONObjectOutput,
		reasoning:      applyMimoThinkingReasoning,
		request:        ensureAssistantToolCallReasoningContent,
	})
}

// NewVolcengine creates a Volcengine Ark OpenAI-compatible provider.
func NewVolcengine(cfg OpenAICompatConfig) *OpenAIProvider {
	return newOpenAICompatibleWithProfile(cfg, openAICompatProfile{
		provider:       "volcengine",
		defaultBaseURL: defaultVolcengineBaseURL,
		output:         applyJSONObjectOutput,
		reasoning:      applyVolcengineThinkingReasoning,
		request:        ensureAssistantToolCallReasoningContent,
	})
}

// NewVolcengineCoding creates a Volcengine coding-plan OpenAI-compatible provider.
func NewVolcengineCoding(cfg OpenAICompatConfig) *OpenAIProvider {
	return newOpenAICompatibleWithProfile(cfg, openAICompatProfile{
		provider:       "volcengine-coding",
		defaultBaseURL: defaultVolcengineCodingBaseURL,
		output:         applyJSONObjectOutput,
		reasoning:      applyVolcengineThinkingReasoning,
		request:        ensureAssistantToolCallReasoningContent,
	})
}

func newOpenAICompatibleWithProfile(cfg OpenAICompatConfig, profile openAICompatProfile) *OpenAIProvider {
	baseURL := profile.defaultBaseURL
	if cfg.BaseURL != "" {
		baseURL = cfg.BaseURL
	}
	return &OpenAIProvider{
		name:              cfg.Name,
		provider:          profile.provider,
		baseURL:           normalizeProviderBaseURL(baseURL, profile.defaultBaseURL),
		token:             cfg.Token,
		model:             cfg.Model,
		headers:           cloneHeaders(cfg.Headers),
		client:            coalesceHTTPClient(cfg.HTTPClient),
		firstEventTimeout: normalizeStreamFirstEventTimeout(cfg.StreamFirstEventTimeout),
		maxOutputTokens:   cfg.MaxOutputTok,
		output:            profile.output,
		reasoning:         profile.reasoning,
		request:           profile.request,
	}
}

func DiscoverDeepSeekModels(ctx context.Context, cfg OpenAICompatConfig) ([]RemoteModel, error) {
	return DiscoverOpenAIModels(ctx, openAIConfigFromCompat(cfg, defaultDeepSeekBaseURL))
}

func DiscoverOpenRouterModels(ctx context.Context, cfg OpenAICompatConfig) ([]RemoteModel, error) {
	cfg.Headers = openRouterHeaders(cfg.Headers)
	return DiscoverOpenAIModels(ctx, openAIConfigFromCompat(cfg, defaultOpenRouterBaseURL))
}

func DiscoverMimoModels(ctx context.Context, cfg OpenAICompatConfig) ([]RemoteModel, error) {
	return DiscoverOpenAIModels(ctx, openAIConfigFromCompat(cfg, defaultMimoBaseURL))
}

func DiscoverVolcengineModels(ctx context.Context, cfg OpenAICompatConfig) ([]RemoteModel, error) {
	return DiscoverOpenAIModels(ctx, openAIConfigFromCompat(cfg, defaultVolcengineBaseURL))
}

func DiscoverVolcengineCodingModels(ctx context.Context, cfg OpenAICompatConfig) ([]RemoteModel, error) {
	return DiscoverOpenAIModels(ctx, openAIConfigFromCompat(cfg, defaultVolcengineCodingBaseURL))
}

func openAIConfigFromCompat(cfg OpenAICompatConfig, defaultBaseURL string) OpenAIConfig {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return OpenAIConfig{
		Name:                    cfg.Name,
		BaseURL:                 baseURL,
		Token:                   cfg.Token,
		Model:                   cfg.Model,
		Headers:                 cloneHeaders(cfg.Headers),
		HTTPClient:              cfg.HTTPClient,
		Timeout:                 cfg.Timeout,
		StreamFirstEventTimeout: cfg.StreamFirstEventTimeout,
	}
}

func applyDeepSeekReasoning(payload *openAIRequestPayload, cfg model.ReasoningConfig) {
	if payload == nil {
		return
	}
	if !deepSeekModelSupportsThinking(payload.Model) {
		clearDeepSeekReasoningFields(payload)
		payload.MaxTokens = clampDeepSeekMaxTokens(payload.MaxTokens)
		return
	}
	effort := normalizeDeepSeekReasoningEffort(cfg.Effort)
	switch effort {
	case "none":
		payload.Thinking = &openAIThinking{Type: "disabled"}
		clearDeepSeekReasoningFields(payload)
		payload.MaxTokens = clampDeepSeekMaxTokens(payload.MaxTokens)
	default:
		payload.Thinking = &openAIThinking{Type: "enabled"}
		payload.Reasoning = nil
		payload.ReasoningEffort = effort
		payload.MaxTokens = clampDeepSeekReasonerMaxTokens(payload.MaxTokens)
		ensureDeepSeekAssistantReasoningContent(payload)
	}
}

type openAIStructuredOutputStrategy string

const (
	openAIStructuredOutputSchema openAIStructuredOutputStrategy = "json_schema"
	openAIStructuredOutputObject openAIStructuredOutputStrategy = "json_object"
)

func applyDeepSeekOutput(payload *openAIRequestPayload, output *model.OutputSpec) {
	applyJSONObjectOutput(payload, output)
}

func applyOpenRouterOutput(payload *openAIRequestPayload, output *model.OutputSpec) {
	applyOpenAIOutputSchema(payload, output, openAIStructuredOutputSchema)
}

func applyJSONObjectOutput(payload *openAIRequestPayload, output *model.OutputSpec) {
	applyOpenAIOutputSchema(payload, output, openAIStructuredOutputObject)
}

func applyOpenAIReasoning(payload *openAIRequestPayload, cfg model.ReasoningConfig) {
	if payload == nil {
		return
	}
	effort := strings.TrimSpace(cfg.Effort)
	if effort == "" {
		return
	}
	payload.Reasoning = &openAIReasoning{Effort: effort}
	payload.ReasoningEffort = effort
}

func applyMimoThinkingReasoning(payload *openAIRequestPayload, cfg model.ReasoningConfig) {
	if payload == nil {
		return
	}
	effort := strings.ToLower(strings.TrimSpace(cfg.Effort))
	switch effort {
	case "":
		return
	case "none":
		payload.Thinking = &openAIThinking{Type: "disabled"}
	default:
		payload.Thinking = &openAIThinking{Type: "enabled"}
	}
	payload.Reasoning = nil
	payload.ReasoningEffort = ""
}

func applyVolcengineThinkingReasoning(payload *openAIRequestPayload, cfg model.ReasoningConfig) {
	if payload == nil {
		return
	}
	state := "enabled"
	switch strings.ToLower(strings.TrimSpace(cfg.Effort)) {
	case "":
		state = "auto"
	case "none":
		state = "disabled"
	}
	payload.Thinking = &openAIThinking{Type: state}
	payload.Reasoning = nil
	payload.ReasoningEffort = ""
}

func applyOpenAIOutputSchema(payload *openAIRequestPayload, output *model.OutputSpec, strategy openAIStructuredOutputStrategy) {
	if payload == nil || output == nil {
		return
	}
	if output.MaxOutputTokens > 0 {
		payload.MaxTokens = output.MaxOutputTokens
	}
	switch output.Mode {
	case model.OutputModeJSON:
		payload.ResponseFormat = &openAIResponseFormat{Type: "json_object"}
	case model.OutputModeSchema:
		if strategy == openAIStructuredOutputObject {
			payload.ResponseFormat = &openAIResponseFormat{Type: "json_object"}
			return
		}
		if len(output.JSONSchema) > 0 {
			payload.ResponseFormat = &openAIResponseFormat{
				Type: "json_schema",
				JSONSchema: &openAIJSONSchemaFormat{
					Name:   "caelis_output",
					Strict: openAIStrictSchema(output.JSONSchema),
					Schema: cloneProviderMap(output.JSONSchema),
				},
			}
		}
	}
}

func openAIStrictSchema(schema map[string]any) bool {
	if len(schema) == 0 {
		return false
	}
	if schema["type"] != "object" {
		return false
	}
	additional, ok := schema["additionalProperties"].(bool)
	return ok && !additional
}

func normalizeDeepSeekReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none":
		return "none"
	case "max", "xhigh", "very_high", "veryhigh":
		return "max"
	case "", "minimal", "low", "medium", "high":
		return "high"
	default:
		return "high"
	}
}

func clearDeepSeekReasoningFields(payload *openAIRequestPayload) {
	if payload == nil {
		return
	}
	payload.Reasoning = nil
	payload.ReasoningEffort = ""
	for _, msg := range payload.Messages {
		delete(msg, "reasoning_content")
	}
}

func ensureDeepSeekAssistantReasoningContent(payload *openAIRequestPayload) {
	for _, msg := range payload.Messages {
		role, _ := msg["role"].(string)
		if role != string(model.RoleAssistant) {
			continue
		}
		if _, ok := msg["reasoning_content"]; !ok {
			msg["reasoning_content"] = ""
		}
	}
}

func deepSeekModelSupportsThinking(modelName string) bool {
	switch strings.ToLower(strings.TrimSpace(modelName)) {
	case "deepseek-v4-flash", "deepseek-v4-pro":
		return true
	default:
		return false
	}
}

func clampDeepSeekMaxTokens(current int) int {
	switch {
	case current <= 0:
		return deepSeekDefaultMaxTokens
	case current > deepSeekMaxTokens:
		return deepSeekMaxTokens
	default:
		return current
	}
}

func clampDeepSeekReasonerMaxTokens(current int) int {
	switch {
	case current <= 0:
		return thinkingModeMinTokens
	case current < thinkingModeMinTokens:
		return thinkingModeMinTokens
	case current > deepSeekMaxTokens:
		return deepSeekMaxTokens
	default:
		return current
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func openRouterHeaders(headers map[string]string) map[string]string {
	out := map[string]string{
		"HTTP-Referer": openRouterDefaultReferer,
		"X-Title":      openRouterDefaultTitle,
	}
	for key, value := range cloneHeaders(headers) {
		out[key] = value
	}
	return out
}

func applyOpenRouterPayload(payload *openAIRequestPayload, cfg OpenRouterConfig) {
	if payload == nil {
		return
	}
	payload.Model = normalizeOpenRouterModelID(payload.Model)
	payload.Models = normalizeOpenRouterModelIDs(cfg.Models)
	payload.Route = strings.TrimSpace(cfg.Route)
	payload.Provider = cloneProviderMap(cfg.Provider)
	payload.Transforms = cloneStringSlice(cfg.Transforms)
	payload.Plugins = cloneMapSlice(cfg.Plugins)
	for _, msg := range payload.Messages {
		reasoning, ok := msg["reasoning_content"]
		if !ok {
			continue
		}
		delete(msg, "reasoning_content")
		msg["reasoning"] = reasoning
	}
}

func ensureAssistantToolCallReasoningContent(payload *openAIRequestPayload) {
	if payload == nil {
		return
	}
	for _, msg := range payload.Messages {
		role, _ := msg["role"].(string)
		if role != string(model.RoleAssistant) {
			continue
		}
		if _, hasTools := msg["tool_calls"]; !hasTools {
			continue
		}
		if _, ok := msg["reasoning_content"]; !ok {
			msg["reasoning_content"] = ""
		}
	}
}

func normalizeOpenRouterModelID(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	const providerPrefix = "openrouter/"
	if strings.HasPrefix(strings.ToLower(value), providerPrefix) {
		remainder := strings.TrimSpace(value[len(providerPrefix):])
		if strings.Contains(remainder, "/") {
			return remainder
		}
	}
	return value
}

func normalizeOpenRouterModelIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		if normalized := normalizeOpenRouterModelID(item); normalized != "" {
			out = append(out, normalized)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneMapSlice(in []map[string]any) []map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(in))
	for _, item := range in {
		if len(item) == 0 {
			continue
		}
		out = append(out, cloneProviderMap(item))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneOpenRouterConfig(in OpenRouterConfig) OpenRouterConfig {
	return OpenRouterConfig{
		Models:     cloneStringSlice(in.Models),
		Route:      strings.TrimSpace(in.Route),
		Provider:   cloneProviderMap(in.Provider),
		Transforms: cloneStringSlice(in.Transforms),
		Plugins:    cloneMapSlice(in.Plugins),
	}
}
