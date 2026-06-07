package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/model"
)

const defaultOllamaBaseURL = "http://localhost:11434"

type OllamaConfig struct {
	Name                    string
	BaseURL                 string
	Model                   string
	Headers                 map[string]string
	HTTPClient              *http.Client
	Timeout                 time.Duration
	StreamFirstEventTimeout time.Duration
	MaxOutputTok            int
}

type OllamaProvider struct {
	name              string
	provider          string
	baseURL           string
	model             string
	headers           map[string]string
	client            *http.Client
	requestTimeout    time.Duration
	firstEventTimeout time.Duration
	maxOutputTokens   int
}

func NewOllama(cfg OllamaConfig) *OllamaProvider {
	return &OllamaProvider{
		name:              cfg.Name,
		provider:          "ollama",
		baseURL:           normalizeOllamaBaseURL(cfg.BaseURL),
		model:             strings.TrimSpace(cfg.Model),
		headers:           cloneHeaders(cfg.Headers),
		client:            coalesceHTTPClient(cfg.HTTPClient),
		requestTimeout:    cfg.Timeout,
		firstEventTimeout: normalizeStreamFirstEventTimeout(cfg.StreamFirstEventTimeout),
		maxOutputTokens:   cfg.MaxOutputTok,
	}
}

func DiscoverOllamaModels(ctx context.Context, cfg OllamaConfig) ([]RemoteModel, error) {
	if ctx == nil {
		return nil, fmt.Errorf("providers: context is required")
	}
	runCtx := ctx
	cancel := func() {}
	if cfg.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	}
	defer cancel()
	req, err := http.NewRequestWithContext(runCtx, http.MethodGet, normalizeOllamaBaseURL(cfg.BaseURL)+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
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
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	models := make([]RemoteModel, 0, len(payload.Models))
	for _, item := range payload.Models {
		name := firstNonEmpty(strings.TrimSpace(item.Name), strings.TrimSpace(item.Model))
		if name == "" {
			continue
		}
		models = append(models, RemoteModel{Name: name, Capabilities: []string{"text", "tools"}})
	}
	return normalizeRemoteModels(models), nil
}

func (p *OllamaProvider) Name() string { return p.model }

func (p *OllamaProvider) Generate(ctx context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	return func(yield func(model.ResponseEvent, error) bool) {
		stream := requestWantsStream(req)
		payload := p.buildRequest(req, stream)
		raw, err := json.Marshal(payload)
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
		httpReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(raw))
		if err != nil {
			yield(model.ResponseEvent{}, fmt.Errorf("create request: %w", err))
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
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
		if !stream {
			p.emitJSON(resp.Body, yield)
			return
		}
		p.emitStream(resp.Body, yield)
	}
}

func (p *OllamaProvider) buildRequest(req model.Request, stream bool) map[string]any {
	payload := map[string]any{
		"model":    p.model,
		"messages": ollamaMessages(req.Messages),
		"stream":   stream,
	}
	if len(req.Tools) > 0 {
		payload["tools"] = ollamaTools(req.Tools)
	}
	if think := ollamaThinkValue(req.Reasoning); think != nil {
		payload["think"] = *think
	}
	numPredict := p.maxOutputTokens
	if req.MaxTokens > 0 {
		numPredict = req.MaxTokens
	}
	if req.Output != nil && req.Output.MaxOutputTokens > 0 {
		numPredict = req.Output.MaxOutputTokens
	}
	if numPredict > 0 {
		payload["options"] = map[string]any{"num_predict": numPredict}
	}
	applyOllamaOutput(payload, req.Output)
	return payload
}

func (p *OllamaProvider) emitJSON(reader io.Reader, yield func(model.ResponseEvent, error) bool) {
	var out ollamaChatResponse
	if err := json.NewDecoder(reader).Decode(&out); err != nil {
		yield(model.ResponseEvent{}, err)
		return
	}
	for _, evt := range ollamaEvents(out) {
		if !yield(evt, nil) {
			return
		}
	}
}

func (p *OllamaProvider) emitStream(reader io.Reader, yield func(model.ResponseEvent, error) bool) {
	if err := readOllamaStreamWithFirstEventTimeout(reader, p.firstEventTimeout, func(chunk ollamaChatResponse) error {
		for _, evt := range ollamaEvents(chunk) {
			if !yield(evt, nil) {
				return errStopSSE
			}
		}
		return nil
	}); err != nil {
		yield(model.ResponseEvent{}, err)
	}
}

type ollamaChatResponse struct {
	Model           string            `json:"model"`
	Message         ollamaChatMessage `json:"message"`
	Done            bool              `json:"done"`
	PromptEvalCount int               `json:"prompt_eval_count"`
	EvalCount       int               `json:"eval_count"`
}

type ollamaChatMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content,omitempty"`
	Thinking  string           `json:"thinking,omitempty"`
	Images    []string         `json:"images,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
}

type ollamaToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

func ollamaEvents(resp ollamaChatResponse) []model.ResponseEvent {
	events := make([]model.ResponseEvent, 0, 3+len(resp.Message.ToolCalls))
	if resp.Message.Thinking != "" {
		events = append(events, model.ResponseEvent{ReasoningDelta: resp.Message.Thinking})
	}
	if resp.Message.Content != "" {
		events = append(events, model.ResponseEvent{TextDelta: resp.Message.Content})
	}
	for idx, call := range resp.Message.ToolCalls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" {
			continue
		}
		args, _ := toolArgsMap(string(call.Function.Arguments))
		events = append(events, model.ResponseEvent{ToolCall: &model.ToolCallDelta{
			CallID: fmt.Sprintf("ollama-call-%d", idx),
			Name:   name,
			Args:   args,
		}})
	}
	if resp.Done || resp.PromptEvalCount > 0 || resp.EvalCount > 0 {
		usage := ollamaUsage(resp)
		events = append(events, model.ResponseEvent{FinishReason: "stop", Usage: &usage})
	}
	return events
}

func ollamaMessages(messages []model.Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case model.RoleTool:
			for _, part := range msg.Content {
				if part.ToolResult == nil {
					continue
				}
				out = append(out, map[string]any{
					"role":      string(model.RoleTool),
					"content":   part.ToolResult.Content,
					"tool_name": part.ToolResult.CallID,
				})
			}
		default:
			entry := map[string]any{"role": string(msg.Role)}
			var content strings.Builder
			var thinking strings.Builder
			var toolCalls []map[string]any
			for _, part := range msg.Content {
				if part.Text != "" {
					content.WriteString(part.Text)
				}
				if part.Reasoning != nil && part.Reasoning.Visibility != model.ReasoningVisibilityRedacted {
					thinking.WriteString(part.Reasoning.Text)
				}
				if part.ToolUse != nil {
					args := part.ToolUse.ArgJSON
					if strings.TrimSpace(args) == "" {
						args = toolArgsRaw(part.ToolUse.Args)
					}
					var raw json.RawMessage = []byte(args)
					toolCalls = append(toolCalls, map[string]any{"function": map[string]any{
						"name":      part.ToolUse.Name,
						"arguments": raw,
					}})
				}
				if msg.Role == model.RoleUser && part.InlineData != nil && strings.HasPrefix(part.InlineData.MIMEType, "image/") {
					entry["images"] = appendStringAny(entry["images"], string(part.InlineData.Data))
				}
			}
			if content.String() != "" {
				entry["content"] = content.String()
			}
			if thinking.String() != "" {
				entry["thinking"] = thinking.String()
			}
			if len(toolCalls) > 0 {
				entry["tool_calls"] = toolCalls
			}
			out = append(out, entry)
		}
	}
	return out
}

func ollamaTools(tools []model.ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  schemaToMap(tool.Schema),
			},
		})
	}
	return out
}

func ollamaThinkValue(cfg model.ReasoningConfig) *bool {
	effort := strings.ToLower(strings.TrimSpace(cfg.Effort))
	if effort == "" {
		return nil
	}
	value := effort != "none"
	return &value
}

func applyOllamaOutput(payload map[string]any, output *model.OutputSpec) {
	if output == nil {
		return
	}
	switch output.Mode {
	case model.OutputModeJSON:
		payload["format"] = "json"
	case model.OutputModeSchema:
		if len(output.JSONSchema) > 0 {
			payload["format"] = cloneProviderMap(output.JSONSchema)
		} else {
			payload["format"] = "json"
		}
	}
	if _, ok := payload["format"]; ok {
		if _, hasThink := payload["think"]; !hasThink {
			payload["think"] = false
		}
	}
}

func ollamaUsage(resp ollamaChatResponse) model.Usage {
	return model.Usage{
		PromptTokens:     resp.PromptEvalCount,
		CompletionTokens: resp.EvalCount,
		TotalTokens:      resp.PromptEvalCount + resp.EvalCount,
	}
}

func readOllamaStreamWithFirstEventTimeout(reader io.Reader, timeout time.Duration, onChunk func(ollamaChatResponse) error) error {
	if timeout <= 0 {
		return readOllamaStream(reader, onChunk)
	}
	errCh := make(chan error, 1)
	firstEventCh := make(chan struct{}, 1)
	seenFirstEvent := false
	go func() {
		errCh <- readOllamaStream(reader, func(chunk ollamaChatResponse) error {
			if !seenFirstEvent {
				seenFirstEvent = true
				select {
				case firstEventCh <- struct{}{}:
				default:
				}
			}
			return onChunk(chunk)
		})
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case err := <-errCh:
			return err
		case <-firstEventCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return <-errCh
		case <-timer.C:
			if closer, ok := reader.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
			return newStreamFirstEventTimeoutError(timeout)
		}
	}
}

func readOllamaStream(reader io.Reader, onChunk func(ollamaChatResponse) error) error {
	dec := json.NewDecoder(reader)
	for {
		var chunk ollamaChatResponse
		if err := dec.Decode(&chunk); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := onChunk(chunk); err != nil {
			return err
		}
	}
}

func normalizeOllamaBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		base = defaultOllamaBaseURL
	}
	if strings.HasSuffix(strings.ToLower(base), "/v1") {
		base = strings.TrimRight(base[:len(base)-len("/v1")], "/")
	}
	if base == "" {
		return defaultOllamaBaseURL
	}
	return base
}

func appendStringAny(value any, next string) []string {
	var out []string
	if existing, ok := value.([]string); ok {
		out = append(out, existing...)
	}
	out = append(out, next)
	return out
}
