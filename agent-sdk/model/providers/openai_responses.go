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

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

const defaultOpenAIResponsesBaseURL = "https://api.openai.com/v1"

// openAIResponsesLLM uses the stateless Responses contract. Session history,
// including reasoning and tool results, is supplied by the caller on every turn;
// the adapter never depends on provider-stored responses or subscription auth.
type openAIResponsesLLM struct {
	api                   APIType
	name                  string
	provider              string
	baseURL               string
	token                 string
	auth                  AuthConfig
	headers               map[string]string
	client                *http.Client
	requestTimeout        time.Duration
	responseHeaderTimeout time.Duration
	firstEventTimeout     time.Duration
	idleTimeout           time.Duration
	maxOutputTok          int
	contextWindowTokens   int
	imageInput            bool
}

func newOpenAIResponses(cfg Config, token string) *openAIResponsesLLM {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultOpenAIResponsesBaseURL
	}
	provider := strings.TrimSpace(cfg.Provider)
	if provider == "" {
		provider = string(cfg.API)
	}
	return &openAIResponsesLLM{
		api: cfg.API, name: strings.TrimSpace(cfg.Model), provider: provider,
		baseURL: baseURL, token: token, auth: cfg.Auth, headers: cloneHeaders(cfg.Headers),
		client:                coalesceHTTPClient(cfg.HTTPClient),
		requestTimeout:        cfg.Timeout,
		responseHeaderTimeout: normalizeStreamResponseHeaderTimeout(cfg.StreamResponseHeaderTimeout),
		firstEventTimeout:     normalizeStreamFirstEventTimeout(cfg.StreamFirstEventTimeout),
		idleTimeout:           normalizeStreamIdleTimeout(cfg.StreamIdleTimeout),
		maxOutputTok:          cfg.MaxOutputTok,
		contextWindowTokens:   cfg.ContextWindowTokens,
		imageInput:            cfg.ImageInput,
	}
}

func (l *openAIResponsesLLM) Name() string             { return l.name }
func (l *openAIResponsesLLM) ProviderName() string     { return l.provider }
func (l *openAIResponsesLLM) ContextWindowTokens() int { return l.contextWindowTokens }
func (l *openAIResponsesLLM) ResetConnectionsForRetry(cause error) {
	resetHTTPConnectionsForRetry(l.client, cause)
}

func (l *openAIResponsesLLM) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		payload, err := l.buildRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			yield(nil, err)
			return
		}
		runCtx := ctx
		if !req.Stream && l.requestTimeout > 0 {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(ctx, l.requestTimeout)
			defer cancel()
		}
		httpReq, err := http.NewRequestWithContext(runCtx, http.MethodPost, l.baseURL+"/responses", bytes.NewReader(raw))
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json")
		if req.Stream {
			httpReq.Header.Set("Accept", "text/event-stream")
		}
		applyDefaultAttributionHeaders(httpReq, l.api)
		if l.auth.Type != AuthNone {
			applyDefaultAuthHeader(httpReq, Config{API: l.api, Auth: l.auth}, l.token, false)
		}
		applyConfiguredHeaders(httpReq, l.headers)
		var resp *http.Response
		if req.Stream {
			resp, err = doStreamingRequest(l.client, httpReq, l.responseHeaderTimeout)
		} else {
			resp, err = l.client.Do(httpReq)
		}
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= http.StatusMultipleChoices {
			yield(nil, statusError(resp))
			return
		}
		cfg := responsesStreamConfig{
			name: "openai responses", provider: l.provider, model: l.name,
			replayProvider: l.provider, contextWindowTokens: l.contextWindowTokens,
			firstEventTimeout: l.firstEventTimeout, idleTimeout: l.idleTimeout, plainReasoning: true,
		}
		if req.Stream {
			readResponsesStream(runCtx, resp.Body, req, cfg, yield)
			return
		}
		var wire openAICodexResponseWire
		if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
			yield(nil, fmt.Errorf("openai responses: decode response: %w", err))
			return
		}
		if wire.Usage != nil {
			model.RecordInvocationUsage(ctx, wire.Usage.toKernelUsage())
		}
		switch wire.Status {
		case "completed", "incomplete":
		case "failed":
			yield(nil, responsesStreamError(cfg.name, openAICodexStreamWire{Response: &wire}, nil))
			return
		default:
			yield(nil, errorcode.New(errorcode.Internal, fmt.Sprintf("openai responses: unexpected response status %q", wire.Status)))
			return
		}
		accumulator := newOpenAIResponsesAccumulator(l.provider)
		accumulator.plainReasoning = true
		response, err := cfg.response(&wire, accumulator)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: response}, nil)
	}
}

type openAIResponsesRequest struct {
	openAICodexRequest
	Text *openAIResponsesTextConfig `json:"text,omitempty"`
}

type openAIResponsesTextConfig struct {
	Format openAIResponsesTextFormat `json:"format"`
}

type openAIResponsesTextFormat struct {
	Type   string         `json:"type"`
	Name   string         `json:"name,omitempty"`
	Strict *bool          `json:"strict,omitempty"`
	Schema map[string]any `json:"schema,omitempty"`
}

func (l *openAIResponsesLLM) buildRequest(req *model.Request) (openAIResponsesRequest, error) {
	if req == nil {
		return openAIResponsesRequest{}, fmt.Errorf("model: request is nil")
	}
	if req.Reasoning.BudgetTokens > 0 {
		return openAIResponsesRequest{}, errorcode.New(errorcode.Unsupported, "openai responses: reasoning token budgets are unsupported")
	}
	for _, tool := range req.Tools {
		if tool.Kind != model.ToolSpecKindFunction {
			return openAIResponsesRequest{}, errorcode.New(errorcode.Unsupported, "openai responses: only function tools are supported")
		}
	}
	instructions, input, err := openAIResponsesInputs(req.Instructions, req.Messages, l.provider)
	if err != nil {
		return openAIResponsesRequest{}, err
	}
	payload := openAIResponsesRequest{openAICodexRequest: openAICodexRequest{
		Model: l.name, Input: input, Instructions: instructions,
		Tools:        openAICodexTools(req.Tools, l.api == APIOpenAI),
		MaxOutputTok: l.maxOutputTok, Store: false, Stream: req.Stream,
		Include: []string{"reasoning.encrypted_content"}, ServiceTier: req.ServiceTier,
	}}
	if len(payload.Tools) > 0 {
		payload.ToolChoice = "auto"
	}
	if req.DisableTools {
		payload.ToolChoice = "none"
	}
	if effort := strings.TrimSpace(req.Reasoning.Effort); effort != "" {
		payload.Reasoning = &openAICodexReasoning{Effort: effort}
		if l.api == APIOpenAI && effort != "none" {
			payload.Reasoning.Summary = "auto"
		}
	}
	if req.Output == nil {
		return payload, nil
	}
	if req.Output.MaxOutputTokens > 0 {
		payload.MaxOutputTok = req.Output.MaxOutputTokens
	}
	format := openAIResponsesTextFormat{}
	switch req.Output.Mode {
	case "", model.OutputModeText:
		return payload, nil
	case model.OutputModeJSON:
		format.Type = "json_object"
	case model.OutputModeSchema:
		if len(req.Output.JSONSchema) == 0 {
			return openAIResponsesRequest{}, &model.OutputSpecError{Mode: req.Output.Mode, Detail: "Responses schema output requires a JSON schema"}
		}
		strict := openAICompatStrictSchema(req.Output.JSONSchema)
		format = openAIResponsesTextFormat{Type: "json_schema", Name: "caelis_output", Strict: &strict, Schema: cloneAnyMap(req.Output.JSONSchema)}
	default:
		return openAIResponsesRequest{}, &model.OutputSpecError{Mode: req.Output.Mode, Detail: "Responses output mode is unsupported"}
	}
	payload.Text = &openAIResponsesTextConfig{Format: format}
	return payload, nil
}
