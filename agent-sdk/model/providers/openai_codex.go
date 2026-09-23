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
	name                  string
	provider              string
	baseURL               string
	headers               map[string]string
	client                *http.Client
	requestTimeout        time.Duration
	responseHeaderTimeout time.Duration
	firstEventTimeout     time.Duration
	idleTimeout           time.Duration
	contextWindowTokens   int
	imageInput            bool
}

func newOpenAICodex(cfg Config) *openAICodexLLM {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultOpenAICodexBaseURL
	}
	return &openAICodexLLM{
		name:                  strings.TrimSpace(cfg.Model),
		provider:              strings.TrimSpace(cfg.Provider),
		baseURL:               baseURL,
		headers:               cloneHeaders(cfg.Headers),
		client:                coalesceHTTPClient(cfg.HTTPClient),
		requestTimeout:        cfg.Timeout,
		responseHeaderTimeout: normalizeStreamResponseHeaderTimeout(cfg.StreamResponseHeaderTimeout),
		firstEventTimeout:     normalizeStreamFirstEventTimeout(cfg.StreamFirstEventTimeout),
		idleTimeout:           normalizeStreamIdleTimeout(cfg.StreamIdleTimeout),
		contextWindowTokens:   cfg.ContextWindowTokens,
		imageInput:            cfg.ImageInput,
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

func (l *openAICodexLLM) ResetConnectionsForRetry(cause error) {
	if l != nil && l.client != nil {
		resetHTTPConnectionsForRetry(l.client, cause)
	}
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

		resp, err := doStreamingRequest(l.client, httpReq, l.responseHeaderTimeout)
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

		readResponsesStream(ctx, resp.Body, req, responsesStreamConfig{
			name:                "openai codex",
			provider:            l.provider,
			model:               l.name,
			replayProvider:      openAICodexReplayProvider,
			contextWindowTokens: l.contextWindowTokens,
			firstEventTimeout:   l.firstEventTimeout,
			idleTimeout:         l.idleTimeout,
			terminalErrorCodes:  openAICodexTerminalErrorCodes,
		}, yield)
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
