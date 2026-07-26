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

const defaultXAIResponsesBaseURL = "https://cli-chat-proxy.grok.com/v1"

// xAIResponsesLLM implements xAI's Responses dialect without inheriting the
// ChatGPT-only headers, endpoint, or request-affinity semantics of Codex.
type xAIResponsesLLM struct {
	name                string
	provider            string
	baseURL             string
	headers             map[string]string
	client              *http.Client
	requestTimeout      time.Duration
	firstEventTimeout   time.Duration
	maxOutputTok        int
	contextWindowTokens int
}

func newXAIResponses(cfg Config) *xAIResponsesLLM {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultXAIResponsesBaseURL
	}
	return &xAIResponsesLLM{
		name:                strings.TrimSpace(cfg.Model),
		provider:            strings.TrimSpace(cfg.Provider),
		baseURL:             baseURL,
		headers:             cloneHeaders(cfg.Headers),
		client:              coalesceHTTPClient(cfg.HTTPClient),
		requestTimeout:      cfg.Timeout,
		firstEventTimeout:   normalizeStreamFirstEventTimeout(cfg.StreamFirstEventTimeout),
		maxOutputTok:        cfg.MaxOutputTok,
		contextWindowTokens: cfg.ContextWindowTokens,
	}
}

func (l *xAIResponsesLLM) Name() string {
	if l == nil {
		return ""
	}
	return l.name
}

func (l *xAIResponsesLLM) ProviderName() string {
	if l == nil {
		return ""
	}
	return l.provider
}

func (l *xAIResponsesLLM) ContextWindowTokens() int {
	if l == nil {
		return 0
	}
	return l.contextWindowTokens
}

func (l *xAIResponsesLLM) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if l == nil {
			yield(nil, fmt.Errorf("xai responses: model is nil"))
			return
		}
		payload, err := xAIResponsesRequestFromModel(req, l.name, l.maxOutputTok)
		if err != nil {
			yield(nil, err)
			return
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, l.baseURL+"/responses", bytes.NewReader(raw))
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("x-grok-model-override", l.name)
		applyDefaultAttributionHeaders(httpReq, APIXAIResponses)
		applyConfiguredHeaders(httpReq, l.headers)

		resp, err := l.client.Do(httpReq)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= http.StatusMultipleChoices {
			err := statusError(resp)
			if errorcode.Is(err, errorcode.Unauthenticated) || errorcode.Is(err, errorcode.PermissionDenied) {
				err = &xAIResponsesTerminalError{cause: err}
			}
			yield(nil, err)
			return
		}

		accumulator := newOpenAIResponsesAccumulator("xai")
		terminalSeen := false
		stopped := false
		err = readSSEWithFirstEventTimeout(resp.Body, l.firstEventTimeout, func(data []byte) error {
			var event openAICodexStreamWire
			if err := json.Unmarshal(data, &event); err != nil {
				return fmt.Errorf("xai responses: decode stream event: %w", err)
			}
			switch event.Type {
			case "response.output_item.added", "response.output_item.done":
				if event.Item != nil {
					accumulator.applyItem(*event.Item, event.OutputIndex)
				}
			case "response.output_text.delta":
				if event.Delta == "" {
					return nil
				}
				accumulator.appendText(event)
				if req.Stream && !yield(&model.StreamEvent{
					Type:      model.StreamEventPartDelta,
					PartDelta: &model.PartDelta{Index: event.OutputIndex, Kind: model.PartKindText, TextDelta: event.Delta},
				}, nil) {
					stopped = true
					return errStopSSE
				}
			case "response.reasoning_text.delta", "response.reasoning_summary.delta", "response.reasoning_summary_text.delta":
				if event.Delta == "" {
					return nil
				}
				delta := accumulator.appendReasoning(event)
				if req.Stream && !yield(&model.StreamEvent{
					Type:      model.StreamEventPartDelta,
					PartDelta: &model.PartDelta{Index: event.OutputIndex, Kind: model.PartKindReasoning, TextDelta: delta},
				}, nil) {
					stopped = true
					return errStopSSE
				}
			case "response.function_call_arguments.delta":
				if event.Delta == "" {
					return nil
				}
				accumulator.appendArguments(event)
				if req.Stream && !yield(&model.StreamEvent{
					Type:      model.StreamEventPartDelta,
					PartDelta: &model.PartDelta{Index: event.OutputIndex, Kind: model.PartKindToolUse, InputDelta: event.Delta},
				}, nil) {
					stopped = true
					return errStopSSE
				}
			case "response.completed", "response.incomplete":
				if event.Response == nil {
					return errorcode.New(errorcode.Internal, "xai responses: terminal response is empty")
				}
				for index, item := range event.Response.Output {
					accumulator.applyItem(item, index)
				}
				message, err := accumulator.message()
				if err != nil {
					return err
				}
				finishReason, rawFinishReason := openAICodexFinishReason(event.Response, accumulator.hasToolCall)
				usage := model.Usage{}
				if event.Response.Usage != nil {
					usage = event.Response.Usage.toKernelUsage()
				}
				responseModel := strings.TrimSpace(event.Response.Model)
				if responseModel == "" {
					responseModel = l.name
				}
				terminalSeen = true
				if !yield(&model.StreamEvent{
					Type: model.StreamEventTurnDone,
					Response: &model.Response{
						Message:             message,
						StepComplete:        true,
						TurnComplete:        true,
						Status:              model.ResponseStatusCompleted,
						FinishReason:        finishReason,
						RawFinishReason:     rawFinishReason,
						Usage:               usage,
						Model:               responseModel,
						Provider:            l.provider,
						ContextWindowTokens: l.contextWindowTokens,
					},
				}, nil) {
					stopped = true
				}
				return errStopSSE
			case "response.failed", "error":
				return xAIResponsesStreamError(event)
			}
			return nil
		})
		if stopped {
			return
		}
		if err != nil {
			yield(nil, err)
			return
		}
		if !terminalSeen {
			yield(nil, fmt.Errorf("xai responses: stream ended before a terminal response"))
		}
	}
}

func xAIResponsesRequestFromModel(req *model.Request, modelName string, maxOutputTok int) (openAICodexRequest, error) {
	if req == nil {
		return openAICodexRequest{}, fmt.Errorf("model: request is nil")
	}
	if req.Reasoning.BudgetTokens > 0 {
		return openAICodexRequest{}, errorcode.New(errorcode.Unsupported, "xai responses: reasoning token budgets are unsupported")
	}
	if req.Output != nil && req.Output.Mode != "" && req.Output.Mode != model.OutputModeText {
		return openAICodexRequest{}, &model.OutputSpecError{
			Mode:   req.Output.Mode,
			Detail: "xAI Responses structured output is not implemented",
		}
	}
	if req.Output != nil && req.Output.MaxOutputTokens > 0 {
		maxOutputTok = req.Output.MaxOutputTokens
	}
	instructions, input, err := openAIResponsesInputs(req.Instructions, req.Messages, "xai")
	if err != nil {
		return openAICodexRequest{}, err
	}
	tools := openAICodexTools(req.Tools)
	toolChoice := ""
	if len(tools) > 0 {
		toolChoice = "auto"
	}
	effort := strings.TrimSpace(req.Reasoning.Effort)
	var include []string
	var reasoning *openAICodexReasoning
	if effort != "" {
		include = []string{"reasoning.encrypted_content"}
		reasoning = &openAICodexReasoning{Effort: effort, Summary: "concise"}
	}
	return openAICodexRequest{
		Model:        strings.TrimSpace(modelName),
		Input:        input,
		Instructions: instructions,
		Tools:        tools,
		ToolChoice:   toolChoice,
		MaxOutputTok: maxOutputTok,
		Store:        false,
		Include:      include,
		Reasoning:    reasoning,
		Stream:       true,
	}, nil
}

type xAIResponsesTerminalError struct {
	cause error
}

func (e *xAIResponsesTerminalError) Error() string {
	if e == nil || e.cause == nil {
		return "xai responses: terminal authentication error"
	}
	return e.cause.Error()
}

func (e *xAIResponsesTerminalError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *xAIResponsesTerminalError) Retryable() bool { return false }

func (e *xAIResponsesTerminalError) ErrorCode() errorcode.Code {
	if e == nil {
		return errorcode.Unknown
	}
	return errorcode.CodeOf(e.cause)
}

func xAIResponsesStreamError(event openAICodexStreamWire) error {
	return fmt.Errorf("xai responses: %w", openAICodexStreamError(event))
}
