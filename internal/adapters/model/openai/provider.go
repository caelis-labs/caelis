// Package openai provides a core-native OpenAI-compatible model provider.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := responseError("chat completion", resp)
		_ = resp.Body.Close()
		return nil, err
	}
	if payload.Stream && responseLooksLikeSSE(resp) {
		return newChatSSEStream(resp.Body, p.ID(), payload.Model), nil
	}
	defer resp.Body.Close()
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

func responseLooksLikeSSE(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
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
		Stream:         req.Stream,
		Messages:       messages,
		Tools:          chatTools(req.Tools),
		MaxTokens:      p.maxOutputTokens,
		ResponseFormat: outputResponseFormat(req.Output, p.flavor),
	}
	if payload.Stream {
		payload.StreamOptions = &chatStreamOptions{IncludeUsage: true}
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

type chatSSEStream struct {
	body   io.Closer
	events <-chan chatStreamItem
	once   sync.Once
}

type chatStreamItem struct {
	event model.StreamEvent
	err   error
}

func newChatSSEStream(body io.ReadCloser, providerID string, modelID string) model.Stream {
	events := make(chan chatStreamItem, 32)
	stream := &chatSSEStream{body: body, events: events}
	go readChatSSE(body, providerID, modelID, events)
	return stream
}

func (s *chatSSEStream) Recv() (model.StreamEvent, error) {
	if s == nil || s.events == nil {
		return model.StreamEvent{}, io.EOF
	}
	item, ok := <-s.events
	if !ok {
		return model.StreamEvent{}, io.EOF
	}
	if item.err != nil {
		return model.StreamEvent{}, item.err
	}
	return item.event, nil
}

func (s *chatSSEStream) Close() error {
	if s == nil || s.body == nil {
		return nil
	}
	var err error
	s.once.Do(func() {
		err = s.body.Close()
	})
	return err
}

func readChatSSE(body io.ReadCloser, providerID string, modelID string, out chan<- chatStreamItem) {
	defer close(out)
	defer body.Close()
	acc := chatStreamAccumulator{
		role:      model.RoleAssistant,
		toolCalls: map[int]*chatToolCall{},
	}
	var usage model.Usage
	origin := &model.Origin{Provider: providerID, Model: strings.TrimSpace(modelID)}
	finishReason := ""
	err := readSSE(body, func(data []byte) error {
		var chunk chatStreamChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			return err
		}
		if strings.TrimSpace(chunk.Model) != "" {
			origin.Model = strings.TrimSpace(chunk.Model)
		}
		if created := unixTime(chunk.Created); !created.IsZero() {
			origin.CreatedAt = created
		}
		if chunk.Usage.hasAny() {
			usage = usageFromChat(chunk.Usage)
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		choice := chunk.Choices[0]
		if strings.TrimSpace(choice.FinishReason) != "" {
			finishReason = strings.TrimSpace(choice.FinishReason)
		}
		for _, event := range acc.apply(choice.Delta) {
			if !sendChatStreamItem(out, chatStreamItem{event: event}) {
				return errStopSSE
			}
		}
		return nil
	})
	if err != nil {
		_ = sendChatStreamItem(out, chatStreamItem{err: err})
		return
	}
	message := acc.message()
	message.Origin = origin
	message.Usage = &usage
	origin.RawFinishReason = finishReason
	response := model.Response{
		Message: message,
		Status:  model.ResponseCompleted,
		Usage:   &usage,
		Origin:  origin,
	}
	_ = sendChatStreamItem(out, chatStreamItem{event: model.StreamEvent{
		Type:     model.StreamTurnDone,
		Response: &response,
	}})
}

func sendChatStreamItem(out chan<- chatStreamItem, item chatStreamItem) bool {
	select {
	case out <- item:
		return true
	default:
		out <- item
		return true
	}
}

type chatStreamAccumulator struct {
	role      model.Role
	text      strings.Builder
	reasoning strings.Builder
	toolCalls map[int]*chatToolCall
}

func (a *chatStreamAccumulator) apply(delta chatMessage) []model.StreamEvent {
	if a == nil {
		return nil
	}
	if role := model.Role(strings.TrimSpace(delta.Role)); role != "" {
		a.role = role
	}
	var events []model.StreamEvent
	if text := chatDeltaContentText(delta.Content); text != "" {
		a.text.WriteString(text)
		events = append(events, model.StreamEvent{
			Type:  model.StreamPartDelta,
			Delta: text,
			Part:  ptrPart(model.NewTextPart(text)),
		})
	}
	if text := firstNonEmpty(stringPtrRawValue(delta.ReasoningContent), stringPtrRawValue(delta.Reasoning)); text != "" {
		a.reasoning.WriteString(text)
		events = append(events, model.StreamEvent{
			Type:  model.StreamPartDelta,
			Delta: text,
			Part:  ptrPart(model.NewReasoningPart(text, model.ReasoningVisible)),
		})
	}
	for _, call := range delta.ToolCalls {
		entry := a.toolCalls[call.Index]
		if entry == nil {
			entry = &chatToolCall{Index: call.Index}
			a.toolCalls[call.Index] = entry
		}
		if strings.TrimSpace(call.ID) != "" {
			entry.ID = strings.TrimSpace(call.ID)
		}
		if strings.TrimSpace(call.Type) != "" {
			entry.Type = strings.TrimSpace(call.Type)
		}
		if strings.TrimSpace(call.Function.Name) != "" {
			entry.Function.Name = strings.TrimSpace(call.Function.Name)
		}
		entry.Function.Arguments += call.Function.Arguments
	}
	return events
}

func (a *chatStreamAccumulator) message() model.Message {
	role := a.role
	if role == "" {
		role = model.RoleAssistant
	}
	out := model.Message{Role: role}
	if text := a.text.String(); text != "" {
		out.Parts = append(out.Parts, model.NewTextPart(text))
	}
	if text := a.reasoning.String(); text != "" {
		out.Parts = append(out.Parts, model.NewReasoningPart(text, model.ReasoningVisible))
	}
	indexes := make([]int, 0, len(a.toolCalls))
	for index := range a.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		call := a.toolCalls[index]
		if call == nil {
			continue
		}
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

func ptrPart(part model.Part) *model.Part {
	return &part
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
	StreamOptions   *chatStreamOptions  `json:"stream_options,omitempty"`
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

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type chatReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type chatThinking struct {
	Type string `json:"type"`
}

type chatTool struct {
	Raw      json.RawMessage `json:"-"`
	Type     string          `json:"type"`
	Function chatFunction    `json:"function"`
}

func (t chatTool) MarshalJSON() ([]byte, error) {
	if len(t.Raw) > 0 {
		return slices.Clone(t.Raw), nil
	}
	type payload struct {
		Type     string       `json:"type"`
		Function chatFunction `json:"function"`
	}
	return json.Marshal(payload{Type: t.Type, Function: t.Function})
}

type chatFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type chatToolCall struct {
	Index    int              `json:"index,omitempty"`
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

type chatStreamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Created int64  `json:"created"`
	Choices []struct {
		Index        int         `json:"index"`
		Delta        chatMessage `json:"delta"`
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

func chatDeltaContentText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return chatContentText(value)
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
		if raw, ok := model.ProviderToolPayload(spec, "openai", "openai-compatible"); ok {
			out = append(out, chatTool{Raw: raw})
			continue
		}
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
	return model.NormalizeToolInputString(input)
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

func (u chatUsage) hasAny() bool {
	return u.PromptTokens != 0 ||
		u.PromptTokensDetails.CachedTokens != 0 ||
		u.CompletionTokens != 0 ||
		u.CompletionTokensDetails.ReasoningTokens != 0 ||
		u.TotalTokens != 0
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
			Code    any    `json:"code"`
		} `json:"error"`
	}
	providerErr := model.ProviderError{
		Provider:   "openai",
		Operation:  operation,
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       strings.TrimSpace(string(body)),
	}
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Error.Message) != "" {
		providerErr.Message = payload.Error.Message
		providerErr.Type = payload.Error.Type
		providerErr.Code = providerErrorCode(payload.Error.Code)
	}
	return model.NewProviderError(providerErr)
}

var errStopSSE = errors.New("model/openai: stop sse")

func readSSE(reader io.Reader, onData func([]byte) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var dataLines [][]byte
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := bytes.Join(dataLines, []byte("\n"))
		dataLines = dataLines[:0]
		chunk := strings.TrimSpace(string(payload))
		if chunk == "" {
			return nil
		}
		if chunk == "[DONE]" {
			return errStopSSE
		}
		return onData([]byte(chunk))
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			if err := flush(); err != nil {
				if errors.Is(err, errStopSSE) {
					return nil
				}
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("model/openai: sse scanner: %w", err)
	}
	if err := flush(); err != nil && !errors.Is(err, errStopSSE) {
		return err
	}
	return nil
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

func stringPtrRawValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func providerErrorCode(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
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
