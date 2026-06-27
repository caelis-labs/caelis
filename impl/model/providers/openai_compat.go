package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

type openAICompatLLM struct {
	name                string
	provider            string
	baseURL             string
	token               string
	headers             map[string]string
	client              *http.Client
	requestTimeout      time.Duration
	firstEventTimeout   time.Duration
	maxOutputTok        int
	contextWindowTokens int
	options             openAICompatOptions
}

type openAICompatOptions struct {
	IncludeReasoningContent        bool
	EmitEmptyReasoningForToolCall  bool
	EmitEmptyReasoningForAssistant bool
	ApplyReasoning                 func(*openAICompatRequest, model.ReasoningConfig)
	StructuredOutput               openAICompatStructuredOutput
	StrictFunctionTools            bool
	ProviderTools                  func(modelName string, specs []model.ToolSpec) []openAICompatTool
	UsesProviderExecutedTools      func(modelName string, specs []model.ToolSpec) bool
}

type openAICompatProfile struct {
	IncludeReasoningContent        bool
	EmitEmptyReasoningForToolCall  bool
	EmitEmptyReasoningForAssistant bool
	DisableReasoning               bool
	ApplyReasoning                 func(*openAICompatRequest, model.ReasoningConfig)
	StructuredOutput               openAICompatStructuredOutput
	ProviderTools                  func(modelName string, specs []model.ToolSpec) []openAICompatTool
	UsesProviderExecutedTools      func(modelName string, specs []model.ToolSpec) bool
}

func defaultOpenAICompatOptions() openAICompatOptions {
	return openAICompatOptions{
		ApplyReasoning:   applyOpenAIReasoning,
		StructuredOutput: openAICompatStructuredOutputSchema,
	}
}

type openAICompatStructuredOutput string

const (
	openAICompatStructuredOutputSchema     openAICompatStructuredOutput = "schema"
	openAICompatStructuredOutputJSONOutput openAICompatStructuredOutput = "json_object"
)

func newOpenAICompat(cfg Config, token string) *openAICompatLLM {
	llm := &openAICompatLLM{
		name:                cfg.Model,
		provider:            cfg.Provider,
		baseURL:             strings.TrimRight(cfg.BaseURL, "/"),
		token:               token,
		headers:             cloneHeaders(cfg.Headers),
		client:              coalesceHTTPClient(cfg.HTTPClient),
		requestTimeout:      cfg.Timeout,
		firstEventTimeout:   normalizeStreamFirstEventTimeout(cfg.StreamFirstEventTimeout),
		maxOutputTok:        cfg.MaxOutputTok,
		contextWindowTokens: cfg.ContextWindowTokens,
		options:             defaultOpenAICompatOptions(),
	}
	applyOpenAICompatCapabilities(&llm.options, cfg)
	return llm
}

func newOpenAICompatWithProfile(cfg Config, token string, profile openAICompatProfile) *openAICompatLLM {
	llm := newOpenAICompat(cfg, token)
	llm.options = openAICompatOptionsForProfile(profile)
	applyOpenAICompatCapabilities(&llm.options, cfg)
	return llm
}

func applyOpenAICompatCapabilities(options *openAICompatOptions, cfg Config) {
	// Only the official OpenAI API is known to accept wire-level function.strict.
	// Generic OpenAI-compatible providers may expose the same route shape while
	// using different tool-call parsers.
	if cfg.API == APIOpenAI {
		options.StrictFunctionTools = true
	}
}

func openAICompatOptionsForProfile(profile openAICompatProfile) openAICompatOptions {
	options := defaultOpenAICompatOptions()
	options.IncludeReasoningContent = profile.IncludeReasoningContent
	options.EmitEmptyReasoningForToolCall = profile.EmitEmptyReasoningForToolCall
	options.EmitEmptyReasoningForAssistant = profile.EmitEmptyReasoningForAssistant
	if profile.DisableReasoning {
		options.ApplyReasoning = nil
	} else if profile.ApplyReasoning != nil {
		options.ApplyReasoning = profile.ApplyReasoning
	}
	if profile.StructuredOutput != "" {
		options.StructuredOutput = profile.StructuredOutput
	}
	options.ProviderTools = profile.ProviderTools
	options.UsesProviderExecutedTools = profile.UsesProviderExecutedTools
	return options
}

func (l *openAICompatLLM) Name() string {
	return l.name
}

func (l *openAICompatLLM) ProviderName() string {
	return l.provider
}

func (l *openAICompatLLM) ContextWindowTokens() int {
	return l.contextWindowTokens
}

func (l *openAICompatLLM) UsesProviderExecutedTools(req *model.Request) bool {
	if l == nil || req == nil || l.options.UsesProviderExecutedTools == nil {
		return false
	}
	return l.options.UsesProviderExecutedTools(l.name, req.Tools)
}

func (l *openAICompatLLM) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if req == nil {
			yield(nil, fmt.Errorf("model: request is nil"))
			return
		}
		tools := fromKernelTools(model.FunctionToolDefinitions(req.Tools), l.options.StrictFunctionTools)
		if l.options.ProviderTools != nil {
			tools = append(tools, l.options.ProviderTools(l.name, req.Tools)...)
		}
		payload := openAICompatRequest{
			Model:     l.name,
			Messages:  l.fromKernelMessages(req.Instructions, req.Messages),
			Tools:     tools,
			Stream:    req.Stream,
			MaxTokens: l.maxOutputTok,
		}
		applyOpenAICompatOutput(&payload, req.Output, l.options.StructuredOutput)
		if req.Stream {
			payload.StreamOptions = &openAICompatStreamOptions{IncludeUsage: true}
		}
		if l.options.ApplyReasoning != nil {
			l.options.ApplyReasoning(&payload, req.Reasoning)
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			yield(nil, err)
			return
		}

		runCtx := ctx
		cancel := func() {}
		// For streaming SSE, rely on caller context cancellation to avoid hard timeout
		// cutting off long-running responses.
		if !req.Stream && l.requestTimeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, l.requestTimeout)
		}
		defer cancel()

		httpReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, l.baseURL+"/chat/completions", bytes.NewReader(raw))
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		applyDefaultAttributionHeaders(httpReq, APIOpenAICompatible)
		applyDefaultAuthHeader(httpReq, Config{API: APIOpenAICompatible, Provider: l.provider}, l.token, false)
		applyConfiguredHeaders(httpReq, l.headers)

		resp, err := l.client.Do(httpReq)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			yield(nil, statusError(resp))
			return
		}

		if !req.Stream {
			var out openAICompatResponse
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				yield(nil, err)
				return
			}
			if len(out.Choices) == 0 {
				yield(nil, fmt.Errorf("model: empty choices"))
				return
			}
			msg, err := toKernelMessage(out.Choices[0].Message)
			if err != nil {
				yield(nil, err)
				return
			}
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      msg,
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: normalizeOpenAICompatFinishReason(out.Choices[0].FinishReason),
					Model:        out.Model,
					Provider:     l.provider,
					Usage:        out.Usage.toKernelUsage(),
				},
			}, nil)
			return
		}

		acc := openAIStreamAccumulator{
			role:      model.RoleAssistant,
			toolCalls: map[int]*openAICompatToolCall{},
		}
		var usage model.Usage
		finishReason := model.FinishReasonUnknown
		stopped := false
		if err := readSSEWithFirstEventTimeout(resp.Body, l.firstEventTimeout, func(data []byte) error {
			var chunk openAICompatStreamChunk
			if err := json.Unmarshal(data, &chunk); err != nil {
				return err
			}
			if chunk.Usage.hasAny() {
				usage = chunk.Usage.toKernelUsage()
			}
			if len(chunk.Choices) == 0 {
				return nil
			}
			if one := normalizeOpenAICompatFinishReason(chunk.Choices[0].FinishReason); one != model.FinishReasonUnknown {
				finishReason = one
			}
			delta := chunk.Choices[0].Delta
			if strings.TrimSpace(delta.Role) != "" {
				acc.role = model.Role(delta.Role)
			}
			if text, ok := delta.Content.(string); ok && text != "" {
				acc.text.WriteString(text)
				if !yield(&model.StreamEvent{
					Type:      model.StreamEventPartDelta,
					PartDelta: &model.PartDelta{Kind: model.PartKindText, TextDelta: text},
				}, nil) {
					stopped = true
					return errStopSSE
				}
			}
			if delta.ReasoningContent != "" {
				acc.reasoning.WriteString(delta.ReasoningContent)
				if !yield(&model.StreamEvent{
					Type:      model.StreamEventPartDelta,
					PartDelta: &model.PartDelta{Kind: model.PartKindReasoning, TextDelta: delta.ReasoningContent},
				}, nil) {
					stopped = true
					return errStopSSE
				}
			}
			for _, tc := range delta.ToolCalls {
				entry := acc.toolCalls[tc.Index]
				if entry == nil {
					entry = &openAICompatToolCall{}
					acc.toolCalls[tc.Index] = entry
				}
				if tc.ID != "" {
					entry.ID = tc.ID
				}
				if tc.Function.Name != "" {
					entry.Function.Name = tc.Function.Name
				}
				entry.Function.Arguments += tc.Function.Arguments
			}
			return nil
		}); err != nil {
			yield(nil, err)
			return
		}
		if stopped {
			return
		}
		finalMsg, err := acc.message()
		if err != nil {
			yield(nil, err)
			return
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      finalMsg,
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: finishReason,
				Model:        l.name,
				Provider:     l.provider,
				Usage:        usage,
			},
		}, nil)
	}
}

func cloneHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type openAICompatRequest struct {
	Model           string                     `json:"model"`
	ModelName       string                     `json:"modelName,omitempty"`
	Messages        []openAICompatReqMsg       `json:"messages"`
	Tools           []openAICompatTool         `json:"tools,omitempty"`
	Stream          bool                       `json:"stream"`
	StreamOptions   *openAICompatStreamOptions `json:"stream_options,omitempty"`
	MaxTokens       int                        `json:"max_tokens,omitempty"`
	Temperature     *float64                   `json:"temperature,omitempty"`
	TopP            *float64                   `json:"top_p,omitempty"`
	ResponseFormat  *openAIResponseFormat      `json:"response_format,omitempty"`
	ReasoningEffort string                     `json:"reasoning_effort,omitempty"`
	Reasoning       *openAIReasoning           `json:"reasoning,omitempty"`
	Thinking        *openAIThinking            `json:"thinking,omitempty"`
}

type openAIResponseFormat struct {
	Type       string                  `json:"type"`
	JSONSchema *openAIJSONSchemaFormat `json:"json_schema,omitempty"`
}

type openAIJSONSchemaFormat struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict,omitempty"`
	Schema map[string]any `json:"schema"`
}

func applyOpenAICompatOutput(payload *openAICompatRequest, output *model.OutputSpec, strategy openAICompatStructuredOutput) {
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
		if strategy == openAICompatStructuredOutputJSONOutput {
			payload.ResponseFormat = &openAIResponseFormat{Type: "json_object"}
		} else if len(output.JSONSchema) > 0 {
			payload.ResponseFormat = &openAIResponseFormat{
				Type: "json_schema",
				JSONSchema: &openAIJSONSchemaFormat{
					Name:   "caelis_output",
					Strict: openAICompatStrictSchema(output.JSONSchema),
					Schema: cloneAnyMap(output.JSONSchema),
				},
			}
		}
	}
}

func openAICompatStrictSchema(schema map[string]any) bool {
	if len(schema) == 0 {
		return false
	}
	if _, ok := schema["anyOf"]; ok {
		return openAICompatStrictAnyOf(schema["anyOf"])
	}
	switch openAICompatSchemaPrimaryType(schema["type"]) {
	case "object":
		if additionalProperties, ok := schema["additionalProperties"].(bool); !ok || additionalProperties {
			return false
		}
		properties, _ := schema["properties"].(map[string]any)
		if len(properties) == 0 {
			return true
		}
		required := map[string]struct{}{}
		for _, key := range stringSliceFromProviderAny(schema["required"]) {
			required[key] = struct{}{}
		}
		for key, value := range properties {
			if _, ok := required[key]; !ok {
				return false
			}
			if nested, _ := value.(map[string]any); len(nested) > 0 && !openAICompatStrictSchema(nested) {
				return false
			}
		}
		return true
	case "array":
		if items, _ := schema["items"].(map[string]any); len(items) > 0 {
			return openAICompatStrictSchema(items)
		}
		return true
	default:
		return true
	}
}

func openAICompatStrictAnyOf(value any) bool {
	variants, ok := value.([]any)
	if !ok || len(variants) == 0 {
		return false
	}
	for _, variant := range variants {
		nested, _ := variant.(map[string]any)
		if len(nested) == 0 || !openAICompatStrictSchema(nested) {
			return false
		}
	}
	return true
}

func openAICompatSchemaPrimaryType(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(typed))
	case []string:
		return openAICompatPrimaryTypeFromStrings(typed)
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			text, _ := item.(string)
			values = append(values, text)
		}
		return openAICompatPrimaryTypeFromStrings(values)
	default:
		return ""
	}
}

func openAICompatPrimaryTypeFromStrings(values []string) string {
	primary := ""
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || value == "null" {
			continue
		}
		if primary != "" && primary != value {
			return ""
		}
		primary = value
	}
	return primary
}

func openAICompatStrictToolParameters(schema map[string]any) (map[string]any, bool) {
	converted, ok := openAICompatStrictCompatibleSchema(schema)
	if !ok || !openAICompatStrictSchema(converted) {
		return nil, false
	}
	return converted, true
}

func openAICompatStrictCompatibleSchema(schema map[string]any) (map[string]any, bool) {
	if len(schema) == 0 {
		return nil, false
	}
	out := openAICompatCloneSchemaMap(schema)
	if _, ok := out["anyOf"]; ok {
		variants, ok := openAICompatStrictCompatibleAnyOf(out["anyOf"])
		if !ok {
			return nil, false
		}
		out["anyOf"] = variants
		return out, true
	}
	switch openAICompatSchemaPrimaryType(out["type"]) {
	case "object":
		if additionalProperties, ok := out["additionalProperties"].(bool); !ok || additionalProperties {
			return nil, false
		}
		properties, _ := out["properties"].(map[string]any)
		if len(properties) == 0 {
			return out, true
		}
		required := map[string]struct{}{}
		for _, key := range stringSliceFromProviderAny(out["required"]) {
			required[key] = struct{}{}
		}
		keys := make([]string, 0, len(properties))
		nextProperties := make(map[string]any, len(properties))
		for key, value := range properties {
			propSchema, _ := value.(map[string]any)
			if len(propSchema) == 0 {
				return nil, false
			}
			converted, ok := openAICompatStrictCompatibleSchema(propSchema)
			if !ok {
				return nil, false
			}
			if _, ok := required[key]; !ok {
				converted = openAICompatNullableSchema(converted)
			}
			nextProperties[key] = converted
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out["properties"] = nextProperties
		out["required"] = keys
		return out, true
	case "array":
		items, _ := out["items"].(map[string]any)
		if len(items) == 0 {
			return out, true
		}
		converted, ok := openAICompatStrictCompatibleSchema(items)
		if !ok {
			return nil, false
		}
		out["items"] = converted
		return out, true
	case "string", "integer", "number", "boolean", "null":
		return out, true
	default:
		return nil, false
	}
}

func openAICompatStrictCompatibleAnyOf(value any) ([]any, bool) {
	variants, ok := value.([]any)
	if !ok || len(variants) == 0 {
		return nil, false
	}
	out := make([]any, 0, len(variants))
	for _, variant := range variants {
		nested, _ := variant.(map[string]any)
		if len(nested) == 0 {
			return nil, false
		}
		converted, ok := openAICompatStrictCompatibleSchema(nested)
		if !ok {
			return nil, false
		}
		out = append(out, converted)
	}
	return out, true
}

func openAICompatNullableSchema(schema map[string]any) map[string]any {
	out := openAICompatCloneSchemaMap(schema)
	if variants, _ := out["anyOf"].([]any); len(variants) > 0 {
		if !openAICompatAnyOfHasNull(variants) {
			out["anyOf"] = append(variants, map[string]any{"type": "null"})
		}
		return out
	}
	out["type"] = openAICompatNullableType(out["type"])
	switch enumValues := out["enum"].(type) {
	case []any:
		if !openAICompatEnumHasNull(enumValues) {
			out["enum"] = append(enumValues, nil)
		}
	case []string:
		out["enum"] = openAICompatNullableEnumStrings(enumValues)
	}
	return out
}

func openAICompatAnyOfHasNull(values []any) bool {
	for _, value := range values {
		nested, _ := value.(map[string]any)
		if len(nested) == 0 {
			continue
		}
		if openAICompatSchemaPrimaryType(nested["type"]) == "null" {
			return true
		}
	}
	return false
}

func openAICompatNullableType(value any) any {
	switch typed := value.(type) {
	case string:
		if strings.EqualFold(strings.TrimSpace(typed), "null") {
			return "null"
		}
		return []any{typed, "null"}
	case []string:
		out := make([]any, 0, len(typed)+1)
		hasNull := false
		for _, item := range typed {
			if strings.EqualFold(strings.TrimSpace(item), "null") {
				hasNull = true
			}
			out = append(out, item)
		}
		if !hasNull {
			out = append(out, "null")
		}
		return out
	case []any:
		out := append([]any(nil), typed...)
		hasNull := false
		for _, item := range out {
			text, _ := item.(string)
			if strings.EqualFold(strings.TrimSpace(text), "null") {
				hasNull = true
				break
			}
		}
		if !hasNull {
			out = append(out, "null")
		}
		return out
	default:
		return value
	}
}

func openAICompatEnumHasNull(values []any) bool {
	for _, value := range values {
		if value == nil {
			return true
		}
	}
	return false
}

func openAICompatNullableEnumStrings(values []string) []any {
	out := make([]any, 0, len(values)+1)
	for _, value := range values {
		out = append(out, value)
	}
	out = append(out, nil)
	return out
}

func openAICompatCloneSchemaMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = openAICompatCloneSchemaValue(value)
	}
	return out
}

func openAICompatCloneSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return openAICompatCloneSchemaMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = openAICompatCloneSchemaValue(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}

func stringSliceFromProviderAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, _ := item.(string)
			text = strings.TrimSpace(text)
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

type openAICompatStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type openAIReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type openAIThinking struct {
	Type string `json:"type"`
}

func applyToggleThinkingReasoning(payload *openAICompatRequest, cfg model.ReasoningConfig) {
	if payload == nil {
		return
	}
	effort := strings.ToLower(strings.TrimSpace(cfg.Effort))
	if effort == "" {
		return
	}
	state := "enabled"
	if effort == "none" {
		state = "disabled"
	}
	payload.Thinking = &openAIThinking{Type: state}
	payload.Reasoning = nil
	payload.ReasoningEffort = ""
}

func applyOpenAIReasoning(payload *openAICompatRequest, cfg model.ReasoningConfig) {
	if payload == nil {
		return
	}
	effort := strings.TrimSpace(cfg.Effort)
	if effort == "" {
		return
	}
	payload.Reasoning = &openAIReasoning{Effort: effort}
	// Keep this for compatibility with some OpenAI-compatible gateways.
	payload.ReasoningEffort = effort
}
