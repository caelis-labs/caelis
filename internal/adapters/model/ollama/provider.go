// Package ollama provides a core-native Ollama /api/chat model provider.
package ollama

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
)

const defaultBaseURL = "http://localhost:11434"

type Config struct {
	ID              string
	BaseURL         string
	Model           string
	MaxOutputTokens int
	HTTPClient      *http.Client
}

type Provider struct {
	id              string
	baseURL         string
	model           string
	maxOutputTokens int
	client          *http.Client
}

func New(cfg Config) (*Provider, error) {
	baseURL := normalizeBaseURL(cfg.BaseURL)
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("model/ollama: invalid base url %q", baseURL)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	id := strings.TrimSpace(cfg.ID)
	if id == "" {
		id = "ollama"
	}
	return &Provider{
		id:              id,
		baseURL:         baseURL,
		model:           strings.TrimSpace(cfg.Model),
		maxOutputTokens: cfg.MaxOutputTokens,
		client:          client,
	}, nil
}

func normalizeBaseURL(raw string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(raw), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if strings.HasSuffix(strings.ToLower(baseURL), "/v1") {
		baseURL = baseURL[:len(baseURL)-len("/v1")]
	}
	return baseURL
}

func (p *Provider) ID() string {
	if p == nil || strings.TrimSpace(p.id) == "" {
		return "ollama"
	}
	return p.id
}

func (p *Provider) Models(ctx context.Context) ([]model.ModelInfo, error) {
	if p == nil {
		return nil, errors.New("model/ollama: provider is nil")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError("list models", resp)
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	out := make([]model.ModelInfo, 0, len(payload.Models))
	for _, item := range payload.Models {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		out = append(out, model.ModelInfo{
			ID:                name,
			Name:              name,
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
		return nil, errors.New("model/ollama: provider is nil")
	}
	payload, err := p.chatRequest(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError("chat", resp)
	}
	var completion chatResponse
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

func (p *Provider) chatRequest(req model.Request) (chatRequest, error) {
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		modelID = p.model
	}
	if modelID == "" {
		return chatRequest{}, errors.New("model/ollama: model is required")
	}
	messages := make([]chatMessage, 0, len(req.Instructions)+len(req.Messages))
	for _, text := range req.Instructions {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			messages = append(messages, chatMessage{Role: "system", Content: trimmed})
		}
	}
	for _, message := range req.Messages {
		converted, ok := chatMessageFromCore(message)
		if ok {
			messages = append(messages, converted)
		}
	}
	if len(messages) == 0 {
		return chatRequest{}, errors.New("model/ollama: at least one message is required")
	}
	return chatRequest{
		Model:    modelID,
		Stream:   false,
		Messages: messages,
		Tools:    chatTools(req.Tools),
		Format:   outputFormat(req.Output),
		Think:    thinkValue(req),
		Options:  p.requestOptions(),
	}, nil
}

func (p *Provider) modelResponse(modelID string, completion chatResponse) (model.Response, error) {
	message, err := coreMessageFromChat(completion.Message)
	if err != nil {
		return model.Response{}, err
	}
	message.Role = model.RoleAssistant
	message.Origin = &model.Origin{
		Provider: p.ID(),
		Model:    firstNonEmpty(completion.Model, modelID),
	}
	usage := usageFromChat(completion)
	message.Usage = &usage
	return model.Response{
		Message: message,
		Status:  model.ResponseCompleted,
		Usage:   &usage,
		Origin:  message.Origin,
	}, nil
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []chatMessage  `json:"messages"`
	Tools    []chatTool     `json:"tools,omitempty"`
	Stream   bool           `json:"stream"`
	Format   any            `json:"format,omitempty"`
	Think    *bool          `json:"think,omitempty"`
	Options  *requestOption `json:"options,omitempty"`
}

type requestOption struct {
	NumPredict int `json:"num_predict,omitempty"`
}

type chatMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content,omitempty"`
	Thinking  string     `json:"thinking,omitempty"`
	Images    []string   `json:"images,omitempty"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"`
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

type toolCall struct {
	Function toolCallFunction `json:"function"`
}

type toolCallFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type chatResponse struct {
	Model           string      `json:"model"`
	Message         chatMessage `json:"message"`
	Done            bool        `json:"done"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	EvalCount       int         `json:"eval_count"`
}

func chatMessageFromCore(message model.Message) (chatMessage, bool) {
	switch message.Role {
	case model.RoleSystem:
		return chatMessage{Role: "system", Content: contentText(message.Parts)}, true
	case model.RoleUser:
		out := chatMessage{Role: "user", Content: contentText(message.Parts)}
		for _, part := range message.Parts {
			if part.Kind == model.PartMedia && part.Media != nil && part.Media.Modality == model.MediaImage {
				if image := imageData(part.Media); image != "" {
					out.Images = append(out.Images, image)
				}
			}
		}
		return out, out.Content != "" || len(out.Images) > 0
	case model.RoleAssistant:
		out := chatMessage{
			Role:     "assistant",
			Content:  contentText(message.Parts),
			Thinking: reasoningText(message.Parts),
		}
		for _, call := range message.ToolCalls() {
			name := strings.TrimSpace(call.Name)
			if name == "" {
				continue
			}
			input := strings.TrimSpace(string(call.Input))
			if input == "" {
				input = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, toolCall{Function: toolCallFunction{
				Name:      name,
				Arguments: normalizeToolInput(input),
			}})
		}
		return out, out.Content != "" || len(out.ToolCalls) > 0
	case model.RoleTool:
		result := firstToolResult(message.Parts)
		if result == nil {
			return chatMessage{}, false
		}
		return chatMessage{
			Role:     "tool",
			ToolName: strings.TrimSpace(result.Name),
			Content:  contentText(result.Content),
		}, true
	default:
		return chatMessage{}, false
	}
}

func coreMessageFromChat(message chatMessage) (model.Message, error) {
	out := model.Message{Role: model.RoleAssistant}
	if text := strings.TrimSpace(message.Content); text != "" {
		out.Parts = append(out.Parts, model.NewTextPart(text))
	}
	if text := strings.TrimSpace(message.Thinking); text != "" {
		out.Parts = append(out.Parts, model.NewReasoningPart(text, model.ReasoningVisible))
	}
	for idx, call := range message.ToolCalls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" {
			continue
		}
		input := strings.TrimSpace(string(call.Function.Arguments))
		if input == "" {
			input = "{}"
		}
		out.Parts = append(out.Parts, model.Part{
			Kind: model.PartToolUse,
			ToolUse: &model.ToolCall{
				ID:    fmt.Sprintf("ollama-call-%d", idx),
				Name:  name,
				Input: normalizeToolInput(input),
			},
		})
	}
	return out, nil
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

func imageData(part *model.MediaPart) string {
	if part == nil {
		return ""
	}
	switch part.Source.Kind {
	case model.MediaInline:
		return strings.TrimSpace(part.Source.Data)
	default:
		return ""
	}
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

func (p *Provider) requestOptions() *requestOption {
	if p == nil || p.maxOutputTokens <= 0 {
		return nil
	}
	return &requestOption{NumPredict: p.maxOutputTokens}
}

func outputFormat(output *model.OutputSpec) any {
	if output == nil {
		return nil
	}
	switch output.Mode {
	case model.OutputJSON:
		return "json"
	case model.OutputSchema:
		if len(output.JSONSchema) > 0 {
			return maps.Clone(output.JSONSchema)
		}
		return "json"
	default:
		return nil
	}
}

func thinkValue(req model.Request) *bool {
	effort := strings.ToLower(strings.TrimSpace(req.Reasoning.Effort))
	if effort != "" {
		value := effort != "none"
		return &value
	}
	if outputFormat(req.Output) != nil {
		value := false
		return &value
	}
	return nil
}

func normalizeToolInput(input string) json.RawMessage {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(raw)) {
		return append(json.RawMessage(nil), raw...)
	}
	wrapped, _ := json.Marshal(map[string]any{
		"raw": raw,
	})
	return wrapped
}

func usageFromChat(in chatResponse) model.Usage {
	total := in.PromptEvalCount + in.EvalCount
	return model.Usage{
		InputTokens:  in.PromptEvalCount,
		OutputTokens: in.EvalCount,
		TotalTokens:  total,
	}
}

func responseError(action string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	text := strings.TrimSpace(string(body))
	var payload struct {
		Error string `json:"error"`
	}
	message := text
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Error) != "" {
		message = payload.Error
	}
	return model.NewProviderError(model.ProviderError{
		Provider:   "ollama",
		Operation:  action,
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Message:    message,
		Body:       text,
	})
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
