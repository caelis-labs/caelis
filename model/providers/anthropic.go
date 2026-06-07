package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/model"
)

const defaultAnthropicBaseURL = "https://api.anthropic.com"
const defaultAnthropicMaxOutputTokens = 1024

type AnthropicConfig struct {
	Name                    string
	BaseURL                 string
	Token                   string
	Model                   string
	Headers                 map[string]string
	HTTPClient              *http.Client
	Timeout                 time.Duration
	StreamFirstEventTimeout time.Duration
	MaxOutputTok            int
}

type AnthropicProvider struct {
	name              string
	provider          string
	baseURL           string
	token             string
	model             string
	headers           map[string]string
	client            *http.Client
	requestTimeout    time.Duration
	firstEventTimeout time.Duration
	maxOutputTokens   int
}

func NewAnthropic(cfg AnthropicConfig) *AnthropicProvider {
	maxTokens := cfg.MaxOutputTok
	if maxTokens <= 0 {
		maxTokens = defaultAnthropicMaxOutputTokens
	}
	return &AnthropicProvider{
		name:              cfg.Name,
		provider:          "anthropic",
		baseURL:           normalizeAnthropicBaseURL(cfg.BaseURL, defaultAnthropicBaseURL),
		token:             cfg.Token,
		model:             strings.TrimSpace(cfg.Model),
		headers:           cloneHeaders(cfg.Headers),
		client:            coalesceHTTPClient(cfg.HTTPClient),
		requestTimeout:    cfg.Timeout,
		firstEventTimeout: normalizeStreamFirstEventTimeout(cfg.StreamFirstEventTimeout),
		maxOutputTokens:   maxTokens,
	}
}

func DiscoverAnthropicModels(ctx context.Context, cfg AnthropicConfig) ([]RemoteModel, error) {
	if ctx == nil {
		return nil, fmt.Errorf("providers: context is required")
	}
	runCtx := ctx
	cancel := func() {}
	if cfg.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	}
	defer cancel()
	req, err := http.NewRequestWithContext(runCtx, http.MethodGet, normalizeAnthropicBaseURL(cfg.BaseURL, defaultAnthropicBaseURL)+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	applyAnthropicHeaders(req, cfg.Token, cfg.Headers)
	resp, err := coalesceHTTPClient(cfg.HTTPClient).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	var payload struct {
		Data []struct {
			ID                  string `json:"id"`
			ContextWindow       any    `json:"context_window"`
			MaxOutputTokens     any    `json:"max_output_tokens"`
			InputTokenLimit     any    `json:"input_token_limit"`
			OutputTokenLimit    any    `json:"output_token_limit"`
			Capabilities        any    `json:"capabilities"`
			SupportedMethods    any    `json:"supported_generation_methods"`
			SupportedParameters any    `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	models := make([]RemoteModel, 0, len(payload.Data))
	for _, item := range payload.Data {
		name := strings.TrimSpace(item.ID)
		if name == "" {
			continue
		}
		caps := appendUniqueStrings(nil, toStringSlice(item.Capabilities)...)
		caps = appendUniqueStrings(caps, toStringSlice(item.SupportedMethods)...)
		caps = appendUniqueStrings(caps, toStringSlice(item.SupportedParameters)...)
		models = append(models, RemoteModel{
			Name: name,
			ContextWindowTokens: firstPositiveInt(
				toInt(item.ContextWindow),
				toInt(item.InputTokenLimit),
			),
			MaxOutputTokens: firstPositiveInt(
				toInt(item.MaxOutputTokens),
				toInt(item.OutputTokenLimit),
			),
			Capabilities: caps,
		})
	}
	return normalizeRemoteModels(models), nil
}

func (p *AnthropicProvider) Name() string { return p.model }

func (p *AnthropicProvider) Generate(ctx context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	return func(yield func(model.ResponseEvent, error) bool) {
		stream := requestWantsStream(req)
		payload := p.buildRequest(req, stream)
		data, err := json.Marshal(payload)
		if err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("marshal request: %w", err))
			return
		}
		runCtx := ctx
		cancel := func() {}
		if !stream && p.requestTimeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, p.requestTimeout)
		}
		defer cancel()
		httpReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(data))
		if err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("create request: %w", err))
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if stream {
			httpReq.Header.Set("Accept", "text/event-stream")
		}
		applyAnthropicHeaders(httpReq, p.token, p.headers)
		resp, err := p.client.Do(httpReq)
		if err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("http request: %w", err))
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			yield(model.ResponseEvent{}, statusError(resp))
			return
		}
		if !stream {
			p.emitJSON(resp, yield)
			return
		}
		p.emitStream(resp, yield)
	}
}

func (p *AnthropicProvider) buildRequest(req model.Request, stream bool) map[string]any {
	maxTokens := p.maxOutputTokens
	if req.MaxTokens > 0 {
		maxTokens = req.MaxTokens
	}
	if req.Output != nil && req.Output.MaxOutputTokens > 0 {
		maxTokens = req.Output.MaxOutputTokens
	}
	payload := map[string]any{
		"model":      p.model,
		"max_tokens": maxTokens,
		"messages":   miniMaxMessages(req.Messages),
		"stream":     stream,
	}
	if system := miniMaxSystem(req.Messages); len(system) > 0 {
		payload["system"] = system
	}
	if len(req.Tools) > 0 {
		payload["tools"] = miniMaxTools(req.Tools)
	}
	if thinking := miniMaxThinking(req.Reasoning); thinking != nil {
		payload["thinking"] = thinking
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if len(req.Stop) > 0 {
		payload["stop_sequences"] = append([]string(nil), req.Stop...)
	}
	return payload
}

func (p *AnthropicProvider) emitJSON(resp *http.Response, yield func(model.ResponseEvent, error) bool) {
	var out anthropicMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		yield(model.ResponseEvent{}, err)
		return
	}
	for _, evt := range anthropicResponseEvents(out) {
		if !yield(evt, nil) {
			return
		}
	}
}

func (p *AnthropicProvider) emitStream(resp *http.Response, yield func(model.ResponseEvent, error) bool) {
	if err := readSSEWithFirstEventTimeout(resp.Body, p.firstEventTimeout, func(data []byte) error {
		evt, ok, err := parseAnthropicSSEEvent(data, p.provider)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if !yield(evt, nil) {
			return errStopSSE
		}
		return nil
	}); err != nil {
		yield(model.ResponseEvent{}, err)
	}
}

type anthropicMessageResponse struct {
	Model      string           `json:"model"`
	StopReason string           `json:"stop_reason"`
	Content    []anthropicBlock `json:"content"`
	Usage      miniMaxUsage     `json:"usage"`
}

type anthropicBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	Thinking  string         `json:"thinking"`
	Signature string         `json:"signature"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
}

func anthropicResponseEvents(out anthropicMessageResponse) []model.ResponseEvent {
	events := make([]model.ResponseEvent, 0, len(out.Content)+1)
	for _, block := range out.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				events = append(events, model.ResponseEvent{TextDelta: block.Text})
			}
		case "thinking":
			evt := model.ResponseEvent{ReasoningDelta: block.Thinking}
			if strings.TrimSpace(block.Signature) != "" {
				evt.Metadata = anthropicReplayMetadata("anthropic", block.Signature)
			}
			if evt.ReasoningDelta != "" || evt.Metadata != nil {
				events = append(events, evt)
			}
		case "tool_use":
			events = append(events, model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: block.ID,
				Name:   block.Name,
				Args:   cloneProviderMap(block.Input),
			}})
		}
	}
	usage := out.Usage.toModelUsage()
	events = append(events, model.ResponseEvent{FinishReason: out.StopReason, Usage: usage})
	return events
}

func parseAnthropicSSEEvent(data []byte, provider string) (model.ResponseEvent, bool, error) {
	evt, ok, err := parseMiniMaxSSEEvent(data)
	if evt.Metadata != nil {
		if token, _ := evt.Metadata["replay_token"].(string); token != "" {
			evt.Metadata = anthropicReplayMetadata(provider, token)
		}
	}
	return evt, ok, err
}

func anthropicReplayMetadata(provider string, token string) map[string]any {
	return map[string]any{
		"provider":     provider,
		"replay_kind":  anthropicReplayKindThinkingSignature,
		"replay_token": strings.TrimSpace(token),
	}
}

func applyAnthropicHeaders(req *http.Request, token string, headers map[string]string) {
	if token := strings.TrimSpace(token); token != "" {
		req.Header.Set("x-api-key", token)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	applyConfiguredHeaders(req, headers)
}
