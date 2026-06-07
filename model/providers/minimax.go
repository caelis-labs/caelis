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

const defaultMiniMaxBaseURL = "https://api.minimaxi.com/anthropic"
const defaultMiniMaxMaxOutputTokens = 4096
const anthropicReplayKindThinkingSignature = "thinking_signature"

// MiniMaxConfig configures the MiniMax Anthropic-compatible provider.
type MiniMaxConfig struct {
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

// MiniMaxProvider implements MiniMax over its Anthropic-compatible Messages API.
type MiniMaxProvider struct {
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

func NewMiniMax(cfg MiniMaxConfig) *MiniMaxProvider {
	maxTokens := cfg.MaxOutputTok
	if maxTokens <= 0 {
		maxTokens = defaultMiniMaxMaxOutputTokens
	}
	return &MiniMaxProvider{
		name:              cfg.Name,
		provider:          "minimax",
		baseURL:           normalizeAnthropicBaseURL(cfg.BaseURL, defaultMiniMaxBaseURL),
		token:             cfg.Token,
		model:             cfg.Model,
		headers:           cloneHeaders(cfg.Headers),
		client:            coalesceHTTPClient(cfg.HTTPClient),
		requestTimeout:    cfg.Timeout,
		firstEventTimeout: normalizeStreamFirstEventTimeout(cfg.StreamFirstEventTimeout),
		maxOutputTokens:   maxTokens,
	}
}

func DiscoverMiniMaxModels(ctx context.Context, cfg MiniMaxConfig) ([]RemoteModel, error) {
	if ctx == nil {
		return nil, fmt.Errorf("providers: context is required")
	}
	runCtx := ctx
	cancel := func() {}
	if cfg.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	}
	defer cancel()
	req, err := http.NewRequestWithContext(runCtx, http.MethodGet, normalizeAnthropicBaseURL(cfg.BaseURL, defaultMiniMaxBaseURL)+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	if token := strings.TrimSpace(cfg.Token); token != "" {
		req.Header.Set("x-minimax-api-key", token)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	applyConfiguredHeaders(req, cfg.Headers)
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

func (p *MiniMaxProvider) Name() string { return p.model }

func (p *MiniMaxProvider) Generate(ctx context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	return func(yield func(model.ResponseEvent, error) bool) {
		payload := p.buildRequest(req)
		data, err := json.Marshal(payload)
		if err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("marshal request: %w", err))
			return
		}
		runCtx := ctx
		cancel := func() {}
		if p.requestTimeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, p.requestTimeout)
		}
		defer cancel()
		httpReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(data))
		if err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("create request: %w", err))
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("anthropic-version", "2023-06-01")
		if token := strings.TrimSpace(p.token); token != "" {
			httpReq.Header.Set("x-minimax-api-key", token)
		}
		applyConfiguredHeaders(httpReq, p.headers)

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
		if err := readSSEWithFirstEventTimeout(resp.Body, p.firstEventTimeout, func(data []byte) error {
			evt, ok, err := parseMiniMaxSSEEvent(data)
			if err != nil {
				if !yield(model.ResponseEvent{}, err) {
					return errStopSSE
				}
				return nil
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
			return
		}
	}
}

func (p *MiniMaxProvider) buildRequest(req model.Request) map[string]any {
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
		"stream":     true,
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

func miniMaxSystem(messages []model.Message) []map[string]any {
	var out []map[string]any
	for _, msg := range messages {
		if msg.Role != model.RoleSystem {
			continue
		}
		for _, part := range msg.Content {
			if strings.TrimSpace(part.Text) != "" {
				out = append(out, map[string]any{"type": "text", "text": part.Text})
			}
		}
	}
	return out
}

func miniMaxMessages(messages []model.Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case model.RoleSystem:
			continue
		case model.RoleUser:
			if blocks := miniMaxContentBlocks(msg.Content, true); len(blocks) > 0 {
				out = append(out, map[string]any{"role": "user", "content": blocks})
			}
		case model.RoleAssistant:
			if blocks := miniMaxContentBlocks(msg.Content, false); len(blocks) > 0 {
				out = append(out, map[string]any{"role": "assistant", "content": blocks})
			}
		case model.RoleTool:
			if blocks := miniMaxToolResultBlocks(msg.Content); len(blocks) > 0 {
				out = append(out, map[string]any{"role": "user", "content": blocks})
			}
		}
	}
	return out
}

func miniMaxContentBlocks(parts []model.Part, userRole bool) []map[string]any {
	blocks := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch {
		case part.Text != "":
			blocks = append(blocks, map[string]any{"type": "text", "text": part.Text})
		case part.Reasoning != nil && !userRole:
			if block := miniMaxThinkingBlock(part.Reasoning); block != nil {
				blocks = append(blocks, block)
			}
		case part.ToolUse != nil && !userRole:
			input := part.ToolUse.Args
			if len(input) == 0 && strings.TrimSpace(part.ToolUse.ArgJSON) != "" {
				var decoded map[string]any
				if json.Unmarshal([]byte(part.ToolUse.ArgJSON), &decoded) == nil {
					input = decoded
				}
			}
			if input == nil {
				input = map[string]any{}
			}
			blocks = append(blocks, map[string]any{
				"type":  "tool_use",
				"id":    part.ToolUse.CallID,
				"name":  part.ToolUse.Name,
				"input": input,
			})
		}
	}
	return blocks
}

func miniMaxThinkingBlock(reasoning *model.Reasoning) map[string]any {
	if reasoning == nil || reasoning.Visibility == model.ReasoningVisibilityRedacted {
		return nil
	}
	token := ""
	if reasoning.Replay != nil {
		token = strings.TrimSpace(reasoning.Replay.Token)
	}
	if reasoning.Text == "" && token == "" {
		return nil
	}
	return map[string]any{
		"type":      "thinking",
		"thinking":  reasoning.Text,
		"signature": token,
	}
}

func miniMaxToolResultBlocks(parts []model.Part) []map[string]any {
	blocks := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if part.ToolResult == nil {
			continue
		}
		block := map[string]any{
			"type":        "tool_result",
			"tool_use_id": part.ToolResult.CallID,
			"content":     part.ToolResult.Content,
		}
		if part.ToolResult.IsError {
			block["is_error"] = true
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func miniMaxTools(tools []model.ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": schemaToMap(tool.Schema),
		})
	}
	return out
}

func miniMaxThinking(reasoning model.ReasoningConfig) map[string]any {
	budget := reasoning.BudgetTokens
	switch strings.ToLower(strings.TrimSpace(reasoning.Effort)) {
	case "none":
		return nil
	case "low":
		if budget <= 0 {
			budget = 1024
		}
	case "medium":
		if budget <= 0 {
			budget = 4096
		}
	case "high":
		if budget <= 0 {
			budget = 8192
		}
	case "":
		if budget <= 0 {
			budget = 4096
		}
	}
	if budget <= 0 {
		return nil
	}
	if budget < 1024 {
		budget = 1024
	}
	return map[string]any{"type": "enabled", "budget_tokens": budget}
}

type miniMaxSSE struct {
	Type         string `json:"type"`
	ContentBlock struct {
		Type      string         `json:"type"`
		Text      string         `json:"text"`
		Thinking  string         `json:"thinking"`
		Signature string         `json:"signature"`
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Input     map[string]any `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		Thinking   string `json:"thinking"`
		Signature  string `json:"signature"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage miniMaxUsage `json:"usage"`
}

type miniMaxUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func parseMiniMaxSSEEvent(data []byte) (model.ResponseEvent, bool, error) {
	var evt miniMaxSSE
	if err := json.Unmarshal(data, &evt); err != nil {
		return model.ResponseEvent{}, false, fmt.Errorf("minimax SSE parse error: %w", err)
	}
	switch evt.Type {
	case "content_block_start":
		switch evt.ContentBlock.Type {
		case "text":
			if evt.ContentBlock.Text == "" {
				return model.ResponseEvent{}, false, nil
			}
			return model.ResponseEvent{TextDelta: evt.ContentBlock.Text}, true, nil
		case "thinking":
			out := model.ResponseEvent{ReasoningDelta: evt.ContentBlock.Thinking}
			if evt.ContentBlock.Signature != "" {
				out.Metadata = replayMetadata(evt.ContentBlock.Signature)
			}
			return out, out.ReasoningDelta != "" || out.Metadata != nil, nil
		case "tool_use":
			return model.ResponseEvent{ToolCall: &model.ToolCallDelta{
				CallID: evt.ContentBlock.ID,
				Name:   evt.ContentBlock.Name,
				Args:   cloneProviderMap(evt.ContentBlock.Input),
			}}, true, nil
		}
	case "content_block_delta":
		switch evt.Delta.Type {
		case "text_delta":
			if evt.Delta.Text == "" {
				return model.ResponseEvent{}, false, nil
			}
			return model.ResponseEvent{TextDelta: evt.Delta.Text}, true, nil
		case "thinking_delta":
			if evt.Delta.Thinking == "" {
				return model.ResponseEvent{}, false, nil
			}
			return model.ResponseEvent{ReasoningDelta: evt.Delta.Thinking}, true, nil
		case "signature_delta":
			if evt.Delta.Signature == "" {
				return model.ResponseEvent{}, false, nil
			}
			return model.ResponseEvent{Metadata: replayMetadata(evt.Delta.Signature)}, true, nil
		}
	case "message_delta":
		out := model.ResponseEvent{}
		if evt.Delta.StopReason != "" {
			out.FinishReason = evt.Delta.StopReason
		}
		if usage := evt.Usage.toModelUsage(); usage != nil {
			out.Usage = usage
		}
		return out, out.FinishReason != "" || out.Usage != nil, nil
	}
	return model.ResponseEvent{}, false, nil
}

func replayMetadata(token string) map[string]any {
	return map[string]any{
		"provider":     "minimax",
		"replay_kind":  anthropicReplayKindThinkingSignature,
		"replay_token": token,
	}
}

func (u miniMaxUsage) toModelUsage() *model.Usage {
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheCreationInputTokens == 0 && u.CacheReadInputTokens == 0 {
		return nil
	}
	return &model.Usage{
		PromptTokens:      u.InputTokens,
		CachedInputTokens: u.CacheReadInputTokens,
		CompletionTokens:  u.OutputTokens,
		TotalTokens:       u.InputTokens + u.OutputTokens,
	}
}

func normalizeAnthropicBaseURL(raw string, fallback string) string {
	base := strings.TrimSpace(raw)
	if base == "" {
		base = fallback
	}
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(strings.ToLower(base), "/v1") {
		base = strings.TrimRight(base[:len(base)-len("/v1")], "/")
	}
	if base == "" {
		return strings.TrimRight(fallback, "/")
	}
	return base
}
