// Package anthropic provides a core-native Anthropic Messages API provider.
package anthropic

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

const defaultBaseURL = "https://api.anthropic.com"
const defaultAPIVersion = "2023-06-01"
const replayKindThinkingSignature = "thinking_signature"

type Config struct {
	ID                     string
	BaseURL                string
	DefaultBaseURL         string
	APIKey                 string
	AuthHeader             string
	Model                  string
	MaxOutputTokens        int
	DefaultReasoningBudget int
	APIVersion             string
	Headers                map[string]string
	HTTPClient             *http.Client
}

type Provider struct {
	id                     string
	baseURL                string
	apiKey                 string
	authHeader             string
	model                  string
	maxOutputTokens        int
	defaultReasoningBudget int
	apiVersion             string
	headers                map[string]string
	client                 *http.Client
}

func New(cfg Config) (*Provider, error) {
	baseURL := normalizeBaseURL(firstNonEmpty(cfg.BaseURL, cfg.DefaultBaseURL, defaultBaseURL))
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("model/anthropic: invalid base url %q", baseURL)
	}
	authHeader := strings.TrimSpace(cfg.AuthHeader)
	if authHeader == "" {
		authHeader = "x-api-key"
	}
	apiVersion := strings.TrimSpace(cfg.APIVersion)
	if apiVersion == "" {
		apiVersion = defaultAPIVersion
	}
	maxTokens := cfg.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	id := strings.TrimSpace(cfg.ID)
	if id == "" {
		id = "anthropic"
	}
	return &Provider{
		id:                     id,
		baseURL:                baseURL,
		apiKey:                 strings.TrimSpace(cfg.APIKey),
		authHeader:             authHeader,
		model:                  strings.TrimSpace(cfg.Model),
		maxOutputTokens:        maxTokens,
		defaultReasoningBudget: cfg.DefaultReasoningBudget,
		apiVersion:             apiVersion,
		headers:                maps.Clone(cfg.Headers),
		client:                 client,
	}, nil
}

func normalizeBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		base = defaultBaseURL
	}
	if strings.HasSuffix(strings.ToLower(base), "/v1") {
		base = strings.TrimRight(base[:len(base)-len("/v1")], "/")
	}
	if base == "" {
		return defaultBaseURL
	}
	return base
}

func (p *Provider) ID() string {
	if p == nil || strings.TrimSpace(p.id) == "" {
		return "anthropic"
	}
	return p.id
}

func (p *Provider) Models(ctx context.Context) ([]model.ModelInfo, error) {
	if p == nil {
		return nil, errors.New("model/anthropic: provider is nil")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
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
	var payload modelsResponse
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
			Name:              firstNonEmpty(item.DisplayName, id),
			Provider:          p.ID(),
			SupportsToolCalls: true,
			SupportsImages:    true,
		})
	}
	return out, nil
}

func (p *Provider) Stream(ctx context.Context, req model.Request) (model.Stream, error) {
	if p == nil {
		return nil, errors.New("model/anthropic: provider is nil")
	}
	payload, err := p.messagesRequest(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
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
		return nil, responseError("messages", resp)
	}
	var completion messageResponse
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

func (p *Provider) messagesRequest(req model.Request) (messagesRequest, error) {
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		modelID = p.model
	}
	if modelID == "" {
		return messagesRequest{}, errors.New("model/anthropic: model is required")
	}
	system := append([]string(nil), req.Instructions...)
	messages := make([]messageParam, 0, len(req.Messages))
	for _, message := range req.Messages {
		if message.Role == model.RoleSystem {
			if text := contentText(message.Parts); text != "" {
				system = append(system, text)
			}
			continue
		}
		converted, ok := p.messageFromCore(message)
		if ok {
			messages = append(messages, converted)
		}
	}
	if len(messages) == 0 {
		return messagesRequest{}, errors.New("model/anthropic: at least one message is required")
	}
	payload := messagesRequest{
		Model:     modelID,
		MaxTokens: p.maxOutputTokens,
		System:    systemText(system),
		Messages:  messages,
		Tools:     toolParams(req.Tools),
		Thinking:  p.thinkingConfig(req.Reasoning),
	}
	if payload.MaxTokens <= 0 {
		payload.MaxTokens = 1024
	}
	return payload, nil
}

func (p *Provider) messageFromCore(message model.Message) (messageParam, bool) {
	switch message.Role {
	case model.RoleUser:
		blocks := contentBlocks(message.Parts, true)
		return messageParam{Role: "user", Content: blocks}, len(blocks) > 0
	case model.RoleAssistant:
		blocks := contentBlocks(message.Parts, false)
		return messageParam{Role: "assistant", Content: blocks}, len(blocks) > 0
	case model.RoleTool:
		blocks := toolResultBlocks(message.Parts)
		return messageParam{Role: "user", Content: blocks}, len(blocks) > 0
	default:
		return messageParam{}, false
	}
}

func (p *Provider) modelResponse(modelID string, completion messageResponse) (model.Response, error) {
	if len(completion.Content) == 0 {
		return model.Response{}, errors.New("model/anthropic: response contains no content")
	}
	message := model.Message{
		Role: model.RoleAssistant,
		Origin: &model.Origin{
			Provider:        p.ID(),
			Model:           firstNonEmpty(completion.Model, modelID),
			RawFinishReason: completion.StopReason,
		},
	}
	for _, block := range completion.Content {
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				message.Parts = append(message.Parts, model.NewTextPart(block.Text))
			}
		case "thinking":
			part := model.NewReasoningPart(block.Thinking, model.ReasoningVisible)
			if part.Reasoning != nil && strings.TrimSpace(block.Signature) != "" {
				part.Reasoning.Replay = &model.ReplayMeta{
					Provider: p.ID(),
					Kind:     replayKindThinkingSignature,
					Token:    strings.TrimSpace(block.Signature),
				}
			}
			message.Parts = append(message.Parts, part)
		case "redacted_thinking":
			part := model.NewReasoningPart("", model.ReasoningRedacted)
			if part.Reasoning != nil && strings.TrimSpace(block.Data) != "" {
				raw, _ := json.Marshal(map[string]string{"data": strings.TrimSpace(block.Data)})
				part.Reasoning.ProviderDetails = map[string]json.RawMessage{"anthropic": raw}
			}
			message.Parts = append(message.Parts, part)
		case "tool_use":
			message.Parts = append(message.Parts, model.Part{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    strings.TrimSpace(block.ID),
					Name:  strings.TrimSpace(block.Name),
					Input: normalizeToolInput(block.Input),
				},
			})
		}
	}
	usage := usageFromResponse(completion.Usage)
	message.Usage = &usage
	return model.Response{
		Message: message,
		Status:  model.ResponseCompleted,
		Usage:   &usage,
		Origin:  message.Origin,
	}, nil
}

func (p *Provider) thinkingConfig(cfg model.ReasoningConfig) *thinkingConfig {
	budget := cfg.Budget
	effort := strings.ToLower(strings.TrimSpace(cfg.Effort))
	if budget <= 0 {
		switch effort {
		case "low":
			budget = 1024
		case "medium":
			budget = 4096
		case "high", "max":
			budget = 8192
		}
	}
	if budget <= 0 && p.defaultReasoningBudget > 0 {
		budget = p.defaultReasoningBudget
	}
	if budget <= 0 {
		return nil
	}
	if budget < 1024 {
		budget = 1024
	}
	return &thinkingConfig{Type: "enabled", BudgetTokens: budget}
}

func (p *Provider) setHeaders(req *http.Request) {
	if req == nil {
		return
	}
	setHeaderDefault(req.Header, "User-Agent", caelisUserAgent())
	setHeaderDefault(req.Header, "anthropic-version", p.apiVersion)
	for key, value := range p.headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			req.Header.Set(key, value)
		}
	}
	if p.apiKey == "" {
		return
	}
	if strings.EqualFold(p.authHeader, "Authorization") {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
		return
	}
	req.Header.Set(p.authHeader, p.apiKey)
}

type messagesRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system,omitempty"`
	Messages  []messageParam  `json:"messages"`
	Tools     []toolParam     `json:"tools,omitempty"`
	Thinking  *thinkingConfig `json:"thinking,omitempty"`
}

type messageParam struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	Thinking  string         `json:"thinking,omitempty"`
	Signature string         `json:"signature,omitempty"`
	Data      string         `json:"data,omitempty"`
	Source    *sourceBlock   `json:"source,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     any            `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   []contentBlock `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

type sourceBlock struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type toolParam struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

type thinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type messageResponse struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Role       string                 `json:"role"`
	Model      string                 `json:"model"`
	StopReason string                 `json:"stop_reason"`
	Content    []contentBlockResponse `json:"content"`
	Usage      usage                  `json:"usage"`
}

type contentBlockResponse struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

type modelsResponse struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
}

func contentBlocks(parts []model.Part, userRole bool) []contentBlock {
	if len(parts) == 0 {
		return nil
	}
	out := make([]contentBlock, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case model.PartText:
			if part.Text != nil && strings.TrimSpace(part.Text.Text) != "" {
				out = append(out, contentBlock{Type: "text", Text: part.Text.Text})
			}
		case model.PartJSON:
			if part.JSON != nil && len(part.JSON.Value) > 0 {
				out = append(out, contentBlock{Type: "text", Text: strings.TrimSpace(string(part.JSON.Value))})
			}
		case model.PartReasoning:
			if userRole || part.Reasoning == nil {
				continue
			}
			text := strings.TrimSpace(part.Reasoning.VisibleText)
			token := ""
			if part.Reasoning.Replay != nil {
				token = strings.TrimSpace(part.Reasoning.Replay.Token)
			}
			if part.Reasoning.Visibility == model.ReasoningRedacted {
				if data := anthropicProviderDetail(part.Reasoning.ProviderDetails, "data"); data != "" {
					out = append(out, contentBlock{Type: "redacted_thinking", Data: data})
					continue
				}
			}
			if text != "" || token != "" {
				out = append(out, contentBlock{Type: "thinking", Thinking: text, Signature: token})
			}
		case model.PartToolUse:
			if userRole || part.ToolUse == nil {
				continue
			}
			out = append(out, contentBlock{
				Type:  "tool_use",
				ID:    strings.TrimSpace(part.ToolUse.ID),
				Name:  strings.TrimSpace(part.ToolUse.Name),
				Input: jsonRawToAny(part.ToolUse.Input),
			})
		case model.PartToolResult:
			if !userRole || part.ToolResult == nil {
				continue
			}
			out = append(out, toolResultBlock(*part.ToolResult))
		case model.PartMedia:
			if !userRole || part.Media == nil || part.Media.Modality != model.MediaImage {
				continue
			}
			if block, ok := imageBlock(part.Media); ok {
				out = append(out, block)
			}
		case model.PartFileRef:
			if part.FileRef != nil {
				if text := strings.TrimSpace(firstNonEmpty(part.FileRef.URI, part.FileRef.LocalRef, part.FileRef.FileID, part.FileRef.Name)); text != "" {
					out = append(out, contentBlock{Type: "text", Text: text})
				}
			}
		}
	}
	return out
}

func toolResultBlocks(parts []model.Part) []contentBlock {
	if len(parts) == 0 {
		return nil
	}
	out := make([]contentBlock, 0, len(parts))
	for _, part := range parts {
		if part.Kind == model.PartToolResult && part.ToolResult != nil {
			out = append(out, toolResultBlock(*part.ToolResult))
		}
	}
	return out
}

func toolResultBlock(result model.ToolResultPart) contentBlock {
	return contentBlock{
		Type:      "tool_result",
		ToolUseID: strings.TrimSpace(result.ToolCallID),
		Content:   toolResultContent(result.Content),
		IsError:   result.IsError,
	}
}

func toolResultContent(parts []model.Part) []contentBlock {
	if len(parts) == 0 {
		return []contentBlock{{Type: "text", Text: "{}"}}
	}
	out := make([]contentBlock, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case model.PartText:
			if part.Text != nil && strings.TrimSpace(part.Text.Text) != "" {
				out = append(out, contentBlock{Type: "text", Text: part.Text.Text})
			}
		case model.PartJSON:
			if part.JSON != nil && len(part.JSON.Value) > 0 {
				out = append(out, contentBlock{Type: "text", Text: strings.TrimSpace(string(part.JSON.Value))})
			}
		case model.PartMedia:
			if part.Media != nil && part.Media.Modality == model.MediaImage {
				if block, ok := imageBlock(part.Media); ok {
					out = append(out, block)
				}
			}
		case model.PartFileRef:
			if part.FileRef != nil {
				if text := strings.TrimSpace(firstNonEmpty(part.FileRef.URI, part.FileRef.LocalRef, part.FileRef.FileID, part.FileRef.Name)); text != "" {
					out = append(out, contentBlock{Type: "text", Text: text})
				}
			}
		}
	}
	if len(out) == 0 {
		return []contentBlock{{Type: "text", Text: "{}"}}
	}
	return out
}

func imageBlock(part *model.MediaPart) (contentBlock, bool) {
	if part == nil {
		return contentBlock{}, false
	}
	switch part.Source.Kind {
	case model.MediaInline:
		data := strings.TrimSpace(part.Source.Data)
		if data == "" {
			return contentBlock{}, false
		}
		mimeType := strings.TrimSpace(part.MimeType)
		if mimeType == "" {
			mimeType = "image/png"
		}
		return contentBlock{
			Type: "image",
			Source: &sourceBlock{
				Type:      "base64",
				MediaType: mimeType,
				Data:      data,
			},
		}, true
	case model.MediaURL:
		url := strings.TrimSpace(part.Source.URI)
		if url == "" {
			return contentBlock{}, false
		}
		return contentBlock{Type: "image", Source: &sourceBlock{Type: "url", URL: url}}, true
	default:
		return contentBlock{}, false
	}
}

func toolParams(specs []model.ToolSpec) []toolParam {
	if len(specs) == 0 {
		return nil
	}
	out := make([]toolParam, 0, len(specs))
	for _, spec := range specs {
		if spec.Kind != "" && spec.Kind != model.ToolSpecFunction {
			continue
		}
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		schema := maps.Clone(spec.InputSchema)
		if len(schema) == 0 {
			schema = map[string]any{"type": "object"}
		}
		if _, ok := schema["type"]; !ok {
			schema["type"] = "object"
		}
		out = append(out, toolParam{
			Name:        name,
			Description: strings.TrimSpace(spec.Description),
			InputSchema: schema,
		})
	}
	return out
}

func systemText(values []string) string {
	if len(values) == 0 {
		return ""
	}
	lines := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return strings.Join(lines, "\n\n")
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

func anthropicProviderDetail(details map[string]json.RawMessage, key string) string {
	if len(details) == 0 || strings.TrimSpace(key) == "" {
		return ""
	}
	raw, ok := details["anthropic"]
	if !ok || len(raw) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func jsonRawToAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return map[string]any{}
	}
	return value
}

func normalizeToolInput(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage(`{}`)
	}
	if json.Valid(trimmed) {
		return append(json.RawMessage(nil), trimmed...)
	}
	wrapped, _ := json.Marshal(map[string]any{"raw": string(trimmed)})
	return wrapped
}

func usageFromResponse(in usage) model.Usage {
	total := in.InputTokens + in.OutputTokens
	return model.Usage{
		InputTokens:       in.InputTokens,
		CachedInputTokens: in.CacheReadInputTokens,
		OutputTokens:      in.OutputTokens,
		TotalTokens:       total,
	}
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
		return fmt.Errorf("model/anthropic: %s failed: %s: %s", operation, resp.Status, payload.Error.Message)
	}
	return fmt.Errorf("model/anthropic: %s failed: %s", operation, resp.Status)
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

var _ model.Provider = (*Provider)(nil)
