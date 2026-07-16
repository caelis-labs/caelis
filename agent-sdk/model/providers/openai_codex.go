package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

const (
	defaultOpenAICodexBaseURL           = "https://chatgpt.com/backend-api/codex"
	openAICodexRequestAffinityMaxLength = 64
)

type openAICodexLLM struct {
	name                string
	provider            string
	baseURL             string
	headers             map[string]string
	client              *http.Client
	firstEventTimeout   time.Duration
	contextWindowTokens int
}

func newOpenAICodex(cfg Config) *openAICodexLLM {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultOpenAICodexBaseURL
	}
	return &openAICodexLLM{
		name:                strings.TrimSpace(cfg.Model),
		provider:            strings.TrimSpace(cfg.Provider),
		baseURL:             baseURL,
		headers:             cloneHeaders(cfg.Headers),
		client:              coalesceHTTPClient(cfg.HTTPClient),
		firstEventTimeout:   normalizeStreamFirstEventTimeout(cfg.StreamFirstEventTimeout),
		contextWindowTokens: cfg.ContextWindowTokens,
	}
}

func (l *openAICodexLLM) Name() string {
	if l == nil {
		return ""
	}
	return l.name
}

func (l *openAICodexLLM) ProviderName() string {
	if l == nil {
		return ""
	}
	return l.provider
}

func (l *openAICodexLLM) ContextWindowTokens() int {
	if l == nil {
		return 0
	}
	return l.contextWindowTokens
}

func (l *openAICodexLLM) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if l == nil {
			yield(nil, fmt.Errorf("openai codex: model is nil"))
			return
		}
		payload, err := openAICodexRequestFromModel(req, l.name)
		if err != nil {
			yield(nil, err)
			return
		}
		requestAffinity := ""
		if metadata, ok := model.ProviderRequestMetadataFromContext(ctx); ok {
			requestAffinity = openAICodexRequestAffinity(metadata.SessionAffinity)
			payload.PromptCache = requestAffinity
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
		setHeaderDefault(httpReq.Header, "originator", "caelis")
		if requestAffinity != "" {
			setHeaderDefault(httpReq.Header, "session-id", requestAffinity)
		}
		applyDefaultAttributionHeaders(httpReq, APIOpenAICodex)
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
				err = &openAICodexTerminalError{cause: err}
			}
			yield(nil, err)
			return
		}

		accumulator := newOpenAICodexAccumulator()
		terminalSeen := false
		stopped := false
		err = readSSEWithFirstEventTimeout(resp.Body, l.firstEventTimeout, func(data []byte) error {
			var event openAICodexStreamWire
			if err := json.Unmarshal(data, &event); err != nil {
				return fmt.Errorf("openai codex: decode stream event: %w", err)
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
				accumulator.appendReasoning(event)
				if req.Stream && !yield(&model.StreamEvent{
					Type:      model.StreamEventPartDelta,
					PartDelta: &model.PartDelta{Index: event.OutputIndex, Kind: model.PartKindReasoning, TextDelta: event.Delta},
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
					return errorcode.New(errorcode.Internal, "openai codex: terminal response is empty")
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
				return openAICodexStreamError(event)
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
			yield(nil, fmt.Errorf("openai codex: stream ended before a terminal response"))
		}
	}
}

func openAICodexRequestAffinity(sessionAffinity string) string {
	key := strings.TrimSpace(sessionAffinity)
	if len(key) <= openAICodexRequestAffinityMaxLength {
		return key
	}
	// The Codex backend uses session-id as request affinity and may project it
	// into the downstream prompt_cache_key. Keep the header and body on the
	// same stable value within the Responses API's 64-character limit.
	return fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
}

type openAICodexTerminalError struct {
	cause error
}

func (e *openAICodexTerminalError) Error() string {
	if e == nil || e.cause == nil {
		return "openai codex: terminal authentication error"
	}
	return e.cause.Error()
}

func (e *openAICodexTerminalError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *openAICodexTerminalError) Retryable() bool { return false }

func (e *openAICodexTerminalError) ErrorCode() errorcode.Code {
	if e == nil {
		return errorcode.Unknown
	}
	return errorcode.CodeOf(e.cause)
}

type openAICodexProviderError struct {
	code         string
	message      string
	errorCode    errorcode.Code
	retryable    bool
	backpressure bool
}

func (e *openAICodexProviderError) Error() string {
	if e == nil {
		return "openai codex: provider error"
	}
	if e.code != "" && e.message != "" {
		return "openai codex: " + e.code + ": " + e.message
	}
	if e.message != "" {
		return "openai codex: " + e.message
	}
	if e.code != "" {
		return "openai codex: " + e.code
	}
	return "openai codex: provider error"
}

func (e *openAICodexProviderError) Retryable() bool {
	return e != nil && e.retryable
}

func (e *openAICodexProviderError) Backpressure() bool {
	return e != nil && e.backpressure
}

func (e *openAICodexProviderError) ErrorCode() errorcode.Code {
	if e == nil {
		return errorcode.Unknown
	}
	return e.errorCode
}

func openAICodexStreamError(event openAICodexStreamWire) error {
	code := strings.TrimSpace(event.Code)
	message := strings.TrimSpace(event.Message)
	if event.Response != nil && event.Response.Error != nil {
		if code == "" {
			code = strings.TrimSpace(event.Response.Error.Code)
		}
		if message == "" {
			message = strings.TrimSpace(event.Response.Error.Message)
		}
	}
	lowerCode := strings.ToLower(code)
	providerErr := &openAICodexProviderError{code: code, message: message, errorCode: errorcode.InvalidArgument}
	switch {
	case lowerCode == "context_length_exceeded" || looksLikeContextOverflow(message, http.StatusBadRequest):
		return &model.ContextOverflowError{Cause: providerErr}
	case strings.Contains(lowerCode, "rate_limit"):
		providerErr.errorCode = errorcode.RateLimited
		providerErr.retryable = true
		providerErr.backpressure = true
	case strings.Contains(lowerCode, "overload"):
		providerErr.errorCode = errorcode.Overloaded
		providerErr.retryable = true
		providerErr.backpressure = true
	case strings.Contains(lowerCode, "server_error") || strings.Contains(lowerCode, "unavailable"):
		providerErr.errorCode = errorcode.Unavailable
		providerErr.retryable = true
	case strings.Contains(lowerCode, "auth"):
		providerErr.errorCode = errorcode.Unauthenticated
	case strings.Contains(lowerCode, "permission"):
		providerErr.errorCode = errorcode.PermissionDenied
	}
	return providerErr
}
