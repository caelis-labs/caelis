package providers

import (
	"bytes"
	"cmp"
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
		maxOutputTok:        cfg.MaxOutputTok,
		contextWindowTokens: cfg.ContextWindowTokens,
		options:             defaultOpenAICompatOptions(),
	}
	return llm
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

func (l *openAICompatLLM) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if req == nil {
			yield(nil, fmt.Errorf("model: request is nil"))
			return
		}
		payload := openAICompatRequest{
			Model:     l.name,
			Messages:  l.fromKernelMessages(req.Instructions, req.Messages),
			Tools:     fromKernelTools(model.FunctionToolDefinitions(req.Tools)),
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
		if err := readSSE(resp.Body, func(data []byte) error {
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
	typ, _ := schema["type"].(string)
	switch strings.ToLower(strings.TrimSpace(typ)) {
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

type openAICompatMsg struct {
	Role             string                 `json:"role"`
	Content          any                    `json:"content,omitempty"`
	ReasoningContent string                 `json:"reasoning_content,omitempty"`
	ToolCallID       string                 `json:"tool_call_id,omitempty"`
	ToolCalls        []openAICompatToolCall `json:"tool_calls,omitempty"`
}

type openAICompatReqMsg struct {
	Role             string                 `json:"role"`
	Content          any                    `json:"content,omitempty"`
	ReasoningContent *string                `json:"reasoning_content,omitempty"`
	ToolCallID       string                 `json:"tool_call_id,omitempty"`
	ToolCalls        []openAICompatToolCall `json:"tool_calls,omitempty"`
}

type openAIReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type openAIThinking struct {
	Type string `json:"type"`
}

type openAICompatTool struct {
	Type     string                   `json:"type"`
	Function openAICompatFunctionDecl `json:"function"`
}

type openAICompatFunctionDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type openAICompatToolCall struct {
	ID       string                   `json:"id"`
	Index    int                      `json:"index,omitempty"`
	Type     string                   `json:"type,omitempty"`
	Function openAICompatCallFunction `json:"function"`
}

type openAICompatCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIImageURL struct {
	URL string `json:"url"`
}

type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

type openAICompatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      openAICompatMsg `json:"message"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Usage openAICompatUsage `json:"usage"`
}

type openAICompatStreamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta        openAICompatMsg `json:"delta"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Usage openAICompatUsage `json:"usage"`
}

type openAIStreamAccumulator struct {
	role      model.Role
	text      strings.Builder
	reasoning strings.Builder
	toolCalls map[int]*openAICompatToolCall
}

func (a *openAIStreamAccumulator) message() (model.Message, error) {
	calls := make([]model.ToolCall, 0, len(a.toolCalls))
	keys := make([]int, 0, len(a.toolCalls))
	for idx := range a.toolCalls {
		keys = append(keys, idx)
	}
	sort.Ints(keys)
	for _, idx := range keys {
		tc := a.toolCalls[idx]
		calls = append(calls, model.ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: tc.Function.Arguments,
		})
	}
	parts := make([]model.Part, 0, len(calls)+2)
	if strings.TrimSpace(a.reasoning.String()) != "" {
		parts = append(parts, model.NewReasoningPart(a.reasoning.String(), model.ReasoningVisibilityVisible))
	}
	if strings.TrimSpace(a.text.String()) != "" {
		parts = append(parts, model.NewTextPart(a.text.String()))
	}
	for _, call := range calls {
		part := model.NewToolUsePart(call.ID, call.Name, json.RawMessage(strings.TrimSpace(call.Args)))
		if part.ToolUse != nil && strings.TrimSpace(call.ThoughtSignature) != "" {
			part.ToolUse.Replay = &model.ReplayMeta{Token: call.ThoughtSignature}
		}
		parts = append(parts, part)
	}
	return model.Message{Role: cmp.Or(a.role, model.RoleAssistant), Parts: parts}, nil
}

func (l *openAICompatLLM) fromKernelMessages(instructions []model.Part, messages []model.Message) []openAICompatReqMsg {
	if len(instructions) > 0 {
		messages = append([]model.Message{model.NewMessage(model.RoleSystem, instructions...)}, messages...)
	}
	out := make([]openAICompatReqMsg, 0, len(messages))
	seenToolCalls := map[string]struct{}{}
	for _, m := range messages {
		// OpenAI-compatible APIs reject role=tool messages that do not carry
		// a tool_call_id. Skip malformed history entries.
		if m.Role == model.RoleTool && m.ToolResponse() == nil {
			continue
		}
		for _, call := range m.ToolCalls() {
			callID := strings.TrimSpace(call.ID)
			if callID != "" {
				seenToolCalls[callID] = struct{}{}
			}
		}
		// OpenAI-compatible APIs require tool messages to carry a non-empty
		// tool_call_id that references a preceding assistant tool call.
		// Resume/legacy histories may contain incomplete tool responses; skip
		// these invalid entries to avoid hard request failures.
		if resp := m.ToolResponse(); resp != nil {
			respID := strings.TrimSpace(resp.ID)
			if respID == "" {
				continue
			}
			if _, ok := seenToolCalls[respID]; !ok {
				continue
			}
		}
		out = append(out, l.fromKernelMessage(m))
	}
	return out
}

func fromKernelTools(tools []model.ToolDefinition) []openAICompatTool {
	out := make([]openAICompatTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, openAICompatTool{
			Type: "function",
			Function: openAICompatFunctionDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return out
}

func (l *openAICompatLLM) fromKernelMessage(m model.Message) openAICompatReqMsg {
	if resp := m.ToolResponse(); resp != nil {
		raw, _ := json.Marshal(resp.Result)
		return openAICompatReqMsg{
			Role:       string(model.RoleTool),
			ToolCallID: resp.ID,
			Content:    string(raw),
		}
	}
	if callsIn := m.ToolCalls(); len(callsIn) > 0 {
		calls := make([]openAICompatToolCall, 0, len(callsIn))
		for _, c := range callsIn {
			raw := strings.TrimSpace(c.Args)
			if raw == "" {
				raw = "{}"
			}
			calls = append(calls, openAICompatToolCall{
				ID:   c.ID,
				Type: "function",
				Function: openAICompatCallFunction{
					Name:      c.Name,
					Arguments: raw,
				},
			})
		}
		content := any(nil)
		if text := m.TextContent(); text != "" {
			content = text
		}
		return openAICompatReqMsg{
			Role:             string(m.Role),
			Content:          content,
			ReasoningContent: l.reasoningContentField(m.ReasoningText(), true, true),
			ToolCalls:        calls,
		}
	}
	if m.Role == model.RoleUser {
		contentParts := model.ContentPartsFromParts(m.Parts)
		if len(contentParts) > 0 {
			parts := make([]openAIContentPart, 0, len(contentParts))
			for _, cp := range contentParts {
				switch cp.Type {
				case model.ContentPartText:
					parts = append(parts, openAIContentPart{Type: "text", Text: cp.Text})
				case model.ContentPartImage:
					parts = append(parts, openAIContentPart{
						Type:     "image_url",
						ImageURL: &openAIImageURL{URL: fmt.Sprintf("data:%s;base64,%s", cp.MimeType, cp.Data)},
					})
				}
			}
			return openAICompatReqMsg{
				Role:    string(m.Role),
				Content: parts,
			}
		}
	}
	return openAICompatReqMsg{
		Role:             string(m.Role),
		Content:          m.TextContent(),
		ReasoningContent: l.reasoningContentField(m.ReasoningText(), false, m.Role == model.RoleAssistant),
	}
}

func (l *openAICompatLLM) reasoningContentField(reasoning string, hasToolCalls bool, assistant bool) *string {
	if l == nil || !l.options.IncludeReasoningContent {
		return nil
	}
	if strings.TrimSpace(reasoning) != "" {
		return &reasoning
	}
	if hasToolCalls && l.options.EmitEmptyReasoningForToolCall {
		empty := ""
		return &empty
	}
	if assistant && l.options.EmitEmptyReasoningForAssistant {
		empty := ""
		return &empty
	}
	return nil
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

func toKernelMessage(m openAICompatMsg) (model.Message, error) {
	role := model.Role(m.Role)
	if role == "" {
		role = model.RoleAssistant
	}
	text := ""
	if contentText, ok := m.Content.(string); ok {
		text = contentText
	}
	calls := make([]model.ToolCall, 0, len(m.ToolCalls))
	for _, c := range m.ToolCalls {
		calls = append(calls, model.ToolCall{
			ID:   c.ID,
			Name: c.Function.Name,
			Args: c.Function.Arguments,
		})
	}
	if role == model.RoleAssistant {
		return model.MessageFromAssistantParts(text, m.ReasoningContent, calls), nil
	}
	parts := make([]model.Part, 0, 1)
	if strings.TrimSpace(text) != "" {
		parts = append(parts, model.NewTextPart(text))
	}
	return model.NewMessage(role, parts...), nil
}

func normalizeOpenAICompatFinishReason(raw string) model.FinishReason {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return model.FinishReasonUnknown
	case "stop":
		return model.FinishReasonStop
	case "length", "max_tokens":
		return model.FinishReasonLength
	case "tool_calls", "function_call":
		return model.FinishReasonToolCalls
	case "content_filter":
		return model.FinishReasonContentFilter
	default:
		return model.FinishReason(strings.ToLower(strings.TrimSpace(raw)))
	}
}
