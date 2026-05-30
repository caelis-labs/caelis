// Package openai provides a core-native OpenAI-compatible model provider.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/internal/version"
)

const defaultBaseURL = "https://api.openai.com/v1"
const openRouterReferer = "https://github.com/OnslaughtSnail/caelis"

type Flavor string

const (
	FlavorDefault    Flavor = ""
	FlavorDeepSeek   Flavor = "deepseek"
	FlavorMimo       Flavor = "mimo"
	FlavorOpenRouter Flavor = "openrouter"
	FlavorVolcengine Flavor = "volcengine"
)

type Config struct {
	ID              string
	BaseURL         string
	DefaultBaseURL  string
	APIKey          string
	AuthHeader      string
	Model           string
	MaxOutputTokens int
	Flavor          Flavor
	Headers         map[string]string
	HTTPClient      *http.Client
}

type Provider struct {
	id              string
	baseURL         string
	apiKey          string
	authHeader      string
	model           string
	maxOutputTokens int
	flavor          Flavor
	headers         map[string]string
	client          *http.Client
}

func New(cfg Config) (*Provider, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(strings.TrimSpace(cfg.DefaultBaseURL), "/")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("model/openai: invalid base url %q", baseURL)
	}
	authHeader := strings.TrimSpace(cfg.AuthHeader)
	if authHeader == "" {
		authHeader = "Authorization"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	id := strings.TrimSpace(cfg.ID)
	if id == "" {
		id = "openai-compatible"
	}
	return &Provider{
		id:              id,
		baseURL:         baseURL,
		apiKey:          strings.TrimSpace(cfg.APIKey),
		authHeader:      authHeader,
		model:           strings.TrimSpace(cfg.Model),
		maxOutputTokens: cfg.MaxOutputTokens,
		flavor:          cfg.Flavor,
		headers:         maps.Clone(cfg.Headers),
		client:          client,
	}, nil
}

func (p *Provider) ID() string {
	if p == nil || strings.TrimSpace(p.id) == "" {
		return "openai-compatible"
	}
	return p.id
}

func (p *Provider) Models(ctx context.Context) ([]model.ModelInfo, error) {
	if p == nil {
		return nil, errors.New("model/openai: provider is nil")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	p.setHeaders(req)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError("list models", resp)
	}
	var payload struct {
		Data []struct {
			ID    string `json:"id"`
			Owned string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	out := make([]model.ModelInfo, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		out = append(out, model.ModelInfo{
			ID:                id,
			Name:              id,
			Provider:          p.ID(),
			SupportsToolCalls: true,
			SupportsImages:    true,
			SupportsJSON:      true,
		})
	}
	return out, nil
}

func (p *Provider) Stream(ctx context.Context, req model.Request) (model.Stream, error) {
	if p == nil {
		return nil, errors.New("model/openai: provider is nil")
	}
	payload, err := p.chatRequest(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	p.setHeaders(httpReq)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError("chat completion", resp)
	}
	var completion chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		return nil, err
	}
	response, err := p.modelResponse(payload.Model, completion)
	if err != nil {
		return nil, err
	}
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type:     model.StreamTurnDone,
		Response: &response,
	}}}, nil
}

func (p *Provider) chatRequest(req model.Request) (chatCompletionRequest, error) {
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		modelID = p.model
	}
	if p.flavor == FlavorOpenRouter {
		modelID = normalizeOpenRouterModelID(modelID)
	}
	if modelID == "" {
		return chatCompletionRequest{}, errors.New("model/openai: model is required")
	}
	messages := make([]chatMessage, 0, len(req.Instructions)+len(req.Messages))
	for _, text := range req.Instructions {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			messages = append(messages, chatMessage{Role: "system", Content: trimmed})
		}
	}
	for _, message := range req.Messages {
		converted, ok := p.chatMessageFromCore(message)
		if ok {
			messages = append(messages, converted)
		}
	}
	if len(messages) == 0 {
		return chatCompletionRequest{}, errors.New("model/openai: at least one message is required")
	}
	payload := chatCompletionRequest{
		Model:          modelID,
		Stream:         false,
		Messages:       messages,
		Tools:          chatTools(req.Tools),
		MaxTokens:      p.maxOutputTokens,
		ResponseFormat: outputResponseFormat(req.Output, p.flavor),
	}
	p.applyReasoning(&payload, req.Reasoning)
	return payload, nil
}

func (p *Provider) modelResponse(modelID string, completion chatCompletionResponse) (model.Response, error) {
	if len(completion.Choices) == 0 {
		return model.Response{}, errors.New("model/openai: response contains no choices")
	}
	choice := completion.Choices[0]
	message := coreMessageFromChat(choice.Message)
	message.Role = model.RoleAssistant
	message.Origin = &model.Origin{
		Provider:        p.ID(),
		Model:           firstNonEmpty(completion.Model, modelID),
		RawFinishReason: choice.FinishReason,
		CreatedAt:       unixTime(completion.Created),
	}
	usage := usageFromChat(completion.Usage)
	message.Usage = &usage
	return model.Response{
		Message: message,
		Status:  model.ResponseCompleted,
		Usage:   &usage,
		Origin:  message.Origin,
	}, nil
}

func (p *Provider) setHeaders(req *http.Request) {
	if req == nil {
		return
	}
	setHeaderDefault(req.Header, "User-Agent", caelisUserAgent())
	if p.flavor == FlavorOpenRouter {
		setHeaderDefault(req.Header, "HTTP-Referer", openRouterReferer)
		setHeaderDefault(req.Header, "X-Title", "Caelis")
	}
	for key, value := range p.headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			req.Header.Set(key, value)
		}
	}
	if p.apiKey != "" {
		if strings.EqualFold(p.authHeader, "Authorization") {
			req.Header.Set("Authorization", "Bearer "+p.apiKey)
			return
		}
		req.Header.Set(p.authHeader, p.apiKey)
	}
}

type chatCompletionRequest struct {
	Model           string              `json:"model"`
	Messages        []chatMessage       `json:"messages"`
	Tools           []chatTool          `json:"tools,omitempty"`
	Stream          bool                `json:"stream"`
	MaxTokens       int                 `json:"max_tokens,omitempty"`
	ResponseFormat  *chatResponseFormat `json:"response_format,omitempty"`
	Reasoning       *chatReasoning      `json:"reasoning,omitempty"`
	ReasoningEffort string              `json:"reasoning_effort,omitempty"`
	Thinking        *chatThinking       `json:"thinking,omitempty"`
}

type chatMessage struct {
	Role             string         `json:"role"`
	Content          any            `json:"content,omitempty"`
	Reasoning        *string        `json:"reasoning,omitempty"`
	ReasoningContent *string        `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

type chatResponseFormat struct {
	Type       string                `json:"type"`
	JSONSchema *chatJSONSchemaFormat `json:"json_schema,omitempty"`
}

type chatJSONSchemaFormat struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict,omitempty"`
	Schema map[string]any `json:"schema"`
}

type chatReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type chatThinking struct {
	Type string `json:"type"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type chatCompletionResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Created int64  `json:"created"`
	Choices []struct {
		Index        int         `json:"index"`
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage chatUsage `json:"usage"`
}

type chatUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (p *Provider) chatMessageFromCore(message model.Message) (chatMessage, bool) {
	switch message.Role {
	case model.RoleSystem:
		return chatMessage{Role: "system", Content: contentText(message.Parts)}, true
	case model.RoleUser:
		return chatMessage{Role: "user", Content: userContent(message.Parts)}, true
	case model.RoleAssistant:
		out := chatMessage{Role: "assistant"}
		if text := contentText(message.Parts); text != "" {
			out.Content = text
		}
		calls := message.ToolCalls()
		if p.includeReasoningContent() {
			p.setRequestReasoning(&out, reasoningText(message.Parts), len(calls) > 0, out.Content != nil)
		}
		for _, call := range calls {
			out.ToolCalls = append(out.ToolCalls, chatToolCall{
				ID:   strings.TrimSpace(call.ID),
				Type: "function",
				Function: chatToolFunction{
					Name:      strings.TrimSpace(call.Name),
					Arguments: strings.TrimSpace(string(call.Input)),
				},
			})
		}
		return out, out.Content != nil || len(out.ToolCalls) > 0
	case model.RoleTool:
		result := firstToolResult(message.Parts)
		if result == nil {
			return chatMessage{}, false
		}
		return chatMessage{
			Role:       "tool",
			ToolCallID: strings.TrimSpace(result.ToolCallID),
			Content:    contentText(result.Content),
		}, true
	default:
		return chatMessage{}, false
	}
}

func coreMessageFromChat(message chatMessage) model.Message {
	out := model.Message{Role: model.RoleAssistant}
	if text := chatContentText(message.Content); text != "" {
		out.Parts = append(out.Parts, model.NewTextPart(text))
	}
	if text := firstNonEmpty(stringPtrValue(message.ReasoningContent), stringPtrValue(message.Reasoning)); text != "" {
		out.Parts = append(out.Parts, model.NewReasoningPart(text, model.ReasoningVisible))
	}
	for _, call := range message.ToolCalls {
		out.Parts = append(out.Parts, model.Part{
			Kind: model.PartToolUse,
			ToolUse: &model.ToolCall{
				ID:    strings.TrimSpace(call.ID),
				Name:  strings.TrimSpace(call.Function.Name),
				Input: normalizeToolInput(call.Function.Arguments),
			},
		})
	}
	return out
}

func userContent(parts []model.Part) any {
	if len(parts) == 0 {
		return ""
	}
	var items []map[string]any
	for _, part := range parts {
		switch part.Kind {
		case model.PartText:
			if part.Text != nil && strings.TrimSpace(part.Text.Text) != "" {
				items = append(items, map[string]any{"type": "text", "text": part.Text.Text})
			}
		case model.PartMedia:
			if part.Media == nil || part.Media.Modality != model.MediaImage {
				continue
			}
			imageURL := imageURL(part.Media)
			if imageURL != "" {
				items = append(items, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": imageURL,
					},
				})
			}
		case model.PartFileRef:
			if part.FileRef != nil {
				if text := strings.TrimSpace(firstNonEmpty(part.FileRef.URI, part.FileRef.LocalRef, part.FileRef.Name)); text != "" {
					items = append(items, map[string]any{"type": "text", "text": text})
				}
			}
		}
	}
	if len(items) == 0 {
		return ""
	}
	if len(items) == 1 {
		if text, ok := items[0]["text"].(string); ok && items[0]["type"] == "text" {
			return text
		}
	}
	return items
}

func imageURL(part *model.MediaPart) string {
	if part == nil {
		return ""
	}
	switch part.Source.Kind {
	case model.MediaURL:
		return strings.TrimSpace(part.Source.URI)
	case model.MediaInline:
		data := strings.TrimSpace(part.Source.Data)
		if data == "" {
			return ""
		}
		if strings.HasPrefix(data, "data:") {
			return data
		}
		mime := strings.TrimSpace(part.MimeType)
		if mime == "" {
			mime = "image/png"
		}
		return "data:" + mime + ";base64," + data
	case model.MediaLocalRef:
		return strings.TrimSpace(firstNonEmpty(part.Source.URI, part.Source.LocalRef, part.Source.Data))
	default:
		return strings.TrimSpace(part.Source.URI)
	}
}

func firstToolResult(parts []model.Part) *model.ToolResultPart {
	for _, part := range parts {
		if part.Kind == model.PartToolResult && part.ToolResult != nil {
			result := *part.ToolResult
			result.Content = model.CloneParts(part.ToolResult.Content)
			return &result
		}
	}
	return nil
}

func contentText(parts []model.Part) string {
	if len(parts) == 0 {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case model.PartText:
			if part.Text != nil && strings.TrimSpace(part.Text.Text) != "" {
				texts = append(texts, strings.TrimSpace(part.Text.Text))
			}
		case model.PartJSON:
			if part.JSON != nil && len(part.JSON.Value) > 0 {
				texts = append(texts, strings.TrimSpace(string(part.JSON.Value)))
			}
		case model.PartToolResult:
			if part.ToolResult != nil {
				if text := contentText(part.ToolResult.Content); text != "" {
					texts = append(texts, text)
				}
			}
		}
	}
	return strings.Join(texts, "\n")
}

func reasoningText(parts []model.Part) string {
	if len(parts) == 0 {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Kind == model.PartReasoning && part.Reasoning != nil {
			if text := strings.TrimSpace(part.Reasoning.VisibleText); text != "" {
				texts = append(texts, text)
			}
		}
	}
	return strings.Join(texts, "\n")
}

func chatContentText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		var texts []string
		for _, item := range typed {
			if obj, ok := item.(map[string]any); ok {
				if text, ok := obj["text"].(string); ok && strings.TrimSpace(text) != "" {
					texts = append(texts, strings.TrimSpace(text))
				}
			}
		}
		return strings.Join(texts, "\n")
	default:
		return ""
	}
}

func (p *Provider) includeReasoningContent() bool {
	return p != nil && (p.flavor == FlavorDeepSeek || p.flavor == FlavorMimo || p.flavor == FlavorOpenRouter || p.flavor == FlavorVolcengine)
}

func (p *Provider) setRequestReasoning(out *chatMessage, reasoning string, hasToolCalls bool, hasContent bool) {
	if p == nil || out == nil {
		return
	}
	reasoning = strings.TrimSpace(reasoning)
	emitEmpty := false
	switch p.flavor {
	case FlavorDeepSeek:
		emitEmpty = hasToolCalls || !hasContent
	case FlavorMimo, FlavorVolcengine:
		emitEmpty = hasToolCalls
	}
	if reasoning == "" && !emitEmpty {
		return
	}
	value := reasoning
	if p.flavor == FlavorOpenRouter {
		out.Reasoning = &value
		return
	}
	out.ReasoningContent = &value
}

func (p *Provider) applyReasoning(payload *chatCompletionRequest, cfg model.ReasoningConfig) {
	if p == nil || payload == nil {
		return
	}
	if p.flavor == FlavorDeepSeek {
		applyDeepSeekReasoning(payload, cfg)
		return
	}
	if p.flavor == FlavorMimo {
		applyMimoReasoning(payload, cfg)
		return
	}
	if p.flavor == FlavorVolcengine {
		applyVolcengineReasoning(payload, cfg)
		return
	}
	effort := strings.TrimSpace(cfg.Effort)
	if effort == "" {
		return
	}
	payload.Reasoning = &chatReasoning{Effort: effort}
	payload.ReasoningEffort = effort
}

func applyMimoReasoning(payload *chatCompletionRequest, cfg model.ReasoningConfig) {
	if payload == nil {
		return
	}
	effort := strings.ToLower(strings.TrimSpace(cfg.Effort))
	switch effort {
	case "":
		return
	case "none":
		payload.Thinking = &chatThinking{Type: "disabled"}
	default:
		payload.Thinking = &chatThinking{Type: "enabled"}
	}
	payload.Reasoning = nil
	payload.ReasoningEffort = ""
}

func applyVolcengineReasoning(payload *chatCompletionRequest, cfg model.ReasoningConfig) {
	if payload == nil {
		return
	}
	effort := strings.ToLower(strings.TrimSpace(cfg.Effort))
	state := "enabled"
	switch effort {
	case "none":
		state = "disabled"
	case "":
		state = "auto"
	}
	payload.Thinking = &chatThinking{Type: state}
	payload.Reasoning = nil
	payload.ReasoningEffort = ""
}

func applyDeepSeekReasoning(payload *chatCompletionRequest, cfg model.ReasoningConfig) {
	if payload == nil {
		return
	}
	if !deepSeekModelSupportsThinking(payload.Model) {
		payload.MaxTokens = clampDeepSeekMaxTokens(payload.MaxTokens)
		payload.Reasoning = nil
		payload.ReasoningEffort = ""
		payload.Thinking = nil
		return
	}
	effort := normalizeDeepSeekReasoningEffort(cfg.Effort)
	if effort == "none" {
		payload.Thinking = &chatThinking{Type: "disabled"}
		payload.Reasoning = nil
		payload.ReasoningEffort = ""
		payload.MaxTokens = clampDeepSeekMaxTokens(payload.MaxTokens)
		return
	}
	payload.Thinking = &chatThinking{Type: "enabled"}
	payload.Reasoning = nil
	payload.ReasoningEffort = effort
	payload.MaxTokens = clampDeepSeekReasonerMaxTokens(payload.MaxTokens)
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

func deepSeekModelSupportsThinking(modelName string) bool {
	switch strings.ToLower(strings.TrimSpace(modelName)) {
	case "deepseek-v4-flash", "deepseek-v4-pro":
		return true
	default:
		return false
	}
}

const (
	deepSeekDefaultMaxTokens  = 32768
	deepSeekThinkingMinTokens = 32768
	deepSeekAbsoluteMaxTokens = 393216
)

func clampDeepSeekMaxTokens(current int) int {
	switch {
	case current <= 0:
		return deepSeekDefaultMaxTokens
	case current > deepSeekAbsoluteMaxTokens:
		return deepSeekAbsoluteMaxTokens
	default:
		return current
	}
}

func clampDeepSeekReasonerMaxTokens(current int) int {
	switch {
	case current <= 0:
		return deepSeekThinkingMinTokens
	case current < deepSeekThinkingMinTokens:
		return deepSeekThinkingMinTokens
	case current > deepSeekAbsoluteMaxTokens:
		return deepSeekAbsoluteMaxTokens
	default:
		return current
	}
}

func outputResponseFormat(output *model.OutputSpec, flavor Flavor) *chatResponseFormat {
	if output == nil {
		return nil
	}
	switch output.Mode {
	case model.OutputJSON:
		return &chatResponseFormat{Type: "json_object"}
	case model.OutputSchema:
		if usesJSONObjectSchemaFallback(flavor) {
			return &chatResponseFormat{Type: "json_object"}
		}
		if len(output.JSONSchema) == 0 {
			return nil
		}
		return &chatResponseFormat{
			Type: "json_schema",
			JSONSchema: &chatJSONSchemaFormat{
				Name:   "caelis_output",
				Strict: strictSchema(output.JSONSchema),
				Schema: maps.Clone(output.JSONSchema),
			},
		}
	default:
		return nil
	}
}

func usesJSONObjectSchemaFallback(flavor Flavor) bool {
	return flavor == FlavorDeepSeek || flavor == FlavorMimo || flavor == FlavorVolcengine
}

func strictSchema(schema map[string]any) bool {
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
		for _, key := range stringSliceFromAny(schema["required"]) {
			required[key] = struct{}{}
		}
		for key, value := range properties {
			if _, ok := required[key]; !ok {
				return false
			}
			if nested, _ := value.(map[string]any); len(nested) > 0 && !strictSchema(nested) {
				return false
			}
		}
		return true
	case "array":
		if items, _ := schema["items"].(map[string]any); len(items) > 0 {
			return strictSchema(items)
		}
		return true
	default:
		return true
	}
}

func stringSliceFromAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, _ := item.(string); strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		return nil
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

func chatTools(specs []model.ToolSpec) []chatTool {
	if len(specs) == 0 {
		return nil
	}
	out := make([]chatTool, 0, len(specs))
	for _, spec := range specs {
		if spec.Kind != "" && spec.Kind != model.ToolSpecFunction {
			continue
		}
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		out = append(out, chatTool{
			Type: "function",
			Function: chatFunction{
				Name:        name,
				Description: strings.TrimSpace(spec.Description),
				Parameters:  maps.Clone(spec.InputSchema),
			},
		})
	}
	return out
}

func normalizeToolInput(input string) json.RawMessage {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return nil
	}
	if json.Valid([]byte(raw)) {
		return append(json.RawMessage(nil), raw...)
	}
	wrapped, _ := json.Marshal(map[string]any{
		"raw": raw,
	})
	return wrapped
}

func usageFromChat(in chatUsage) model.Usage {
	return model.Usage{
		InputTokens:       in.PromptTokens,
		CachedInputTokens: in.PromptTokensDetails.CachedTokens,
		OutputTokens:      in.CompletionTokens,
		ReasoningTokens:   in.CompletionTokensDetails.ReasoningTokens,
		TotalTokens:       in.TotalTokens,
	}
}

func unixTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
}

func responseError(operation string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Error.Message) != "" {
		return fmt.Errorf("model/openai: %s failed: %s: %s", operation, resp.Status, payload.Error.Message)
	}
	return fmt.Errorf("model/openai: %s failed: %s", operation, resp.Status)
}

func caelisUserAgent() string {
	value := strings.TrimSpace(version.String())
	value = strings.TrimPrefix(value, "v")
	if value == "" {
		value = "dev"
	}
	return "caelis/" + value
}

func setHeaderDefault(headers http.Header, key string, value string) {
	if headers == nil || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
		return
	}
	if strings.TrimSpace(headers.Get(key)) != "" {
		return
	}
	headers.Set(key, value)
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

var _ model.Provider = (*Provider)(nil)
