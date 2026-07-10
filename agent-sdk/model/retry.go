package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"strings"
	"time"
)

const (
	defaultLLMRequestMaxRetries       = 5
	defaultLLMRetryBaseDelay          = time.Second
	defaultLLMRetryMaxDelay           = 3 * time.Minute
	defaultRateLimitRequestMaxRetries = 7
	defaultRateLimitRetryBaseDelay    = 5 * time.Second
	defaultRateLimitRetryMaxDelay     = 3 * time.Minute
)

// RetryConfig controls retry behavior for one provider-neutral LLM call.
type RetryConfig struct {
	MaxRetries          int
	BaseDelay           time.Duration
	MaxDelay            time.Duration
	RateLimitMaxRetries int
	RateLimitBaseDelay  time.Duration
	RateLimitMaxDelay   time.Duration
}

type retryPolicy struct {
	maxRetries   int
	baseDelay    time.Duration
	maxDelay     time.Duration
	backpressure bool
}

// RetryableError lets providers mark native control-plane errors as retryable
// without forcing upper layers to parse provider-specific payloads.
type RetryableError interface {
	Retryable() bool
}

// BackpressureError lets providers mark retryable errors that should use the
// slower rate-limit/backpressure budget.
type BackpressureError interface {
	Backpressure() bool
}

// WithRetry wraps one LLM so each Generate call retries the provider request
// with the same caller context and a fresh clone of the same model request.
func WithRetry(llm LLM, cfg RetryConfig) LLM {
	if llm == nil {
		return nil
	}
	llm = unwrapRetryingLLM(llm)
	wrapped := &retryingLLM{
		inner: llm,
		cfg:   NormalizeRetryConfig(cfg),
	}
	if _, ok := llm.(WebSearcher); ok {
		return &retryingSearchLLM{retryingLLM: wrapped}
	}
	return wrapped
}

// NormalizeRetryConfig fills retry defaults.
func NormalizeRetryConfig(cfg RetryConfig) RetryConfig {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = defaultLLMRequestMaxRetries
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = defaultLLMRetryBaseDelay
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = defaultLLMRetryMaxDelay
	}
	if cfg.RateLimitMaxRetries <= 0 {
		cfg.RateLimitMaxRetries = defaultRateLimitRequestMaxRetries
	}
	if cfg.RateLimitBaseDelay <= 0 {
		cfg.RateLimitBaseDelay = defaultRateLimitRetryBaseDelay
	}
	if cfg.RateLimitMaxDelay <= 0 {
		cfg.RateLimitMaxDelay = defaultRateLimitRetryMaxDelay
	}
	return cfg
}

type retryingLLM struct {
	inner LLM
	cfg   RetryConfig
}

// retryingSearchLLM keeps WebSearcher optional on retry wrappers. retryingLLM
// itself must not implement SearchWeb because unsupported providers rely on a
// failed type assertion for non-error web_search fallback behavior.
type retryingSearchLLM struct {
	*retryingLLM
}

func unwrapRetryingLLM(llm LLM) LLM {
	switch wrapped := llm.(type) {
	case *retryingSearchLLM:
		if wrapped != nil && wrapped.retryingLLM != nil && wrapped.inner != nil {
			return wrapped.inner
		}
	case *retryingLLM:
		if wrapped != nil && wrapped.inner != nil {
			return wrapped.inner
		}
	}
	return llm
}

// ProviderExecutedToolUsage is an optional LLM capability for providers that
// can enable provider-executed tools implicitly from request/model policy. When
// implemented, retry handling uses this hook instead of inspecting req.Tools
// alone so it does not retry after semantic output from non-idempotent
// provider-side tools.
type ProviderExecutedToolUsage interface {
	UsesProviderExecutedTools(*Request) bool
}

func (l *retryingLLM) Name() string {
	if l == nil || l.inner == nil {
		return ""
	}
	return l.inner.Name()
}

func (l *retryingLLM) Capabilities() Capabilities {
	capabilities, _ := CapabilitiesOf(l.inner)
	return capabilities
}

func (l *retryingLLM) Generate(ctx context.Context, req *Request) iter.Seq2[*StreamEvent, error] {
	return func(yield func(*StreamEvent, error) bool) {
		if l == nil || l.inner == nil {
			yield(nil, errors.New("model: llm is nil"))
			return
		}
		hasProviderTool := usesProviderExecutedTools(l.inner, req)
		stopped := false
		canRetryLastErr := true

		err := l.retryUntilOK(ctx, func(int) error {
			canRetryLastErr = true
			committed, semanticDeltaEmitted, attemptStopped, err := l.runAttempt(ctx, req, yield)
			if attemptStopped {
				stopped = true
				return nil
			}
			if err == nil {
				return nil
			}
			canRetryLastErr = !committed && (!hasProviderTool || !semanticDeltaEmitted)
			return err
		}, func(reset AttemptReset) (bool, error) {
			resetEvent := &StreamEvent{
				Type:         StreamEventAttemptReset,
				AttemptReset: &reset,
			}
			if !yield(resetEvent, nil) {
				return false, nil
			}
			return true, nil
		}, func(err error) bool {
			return canRetryLastErr && IsRetryableLLMError(err)
		})
		if stopped {
			return
		}
		if err != nil {
			yield(nil, err)
		}
	}
}

func (l *retryingLLM) runAttempt(ctx context.Context, req *Request, yield func(*StreamEvent, error) bool) (bool, bool, bool, error) {
	committed := false
	semanticDeltaEmitted := false
	for event, err := range l.inner.Generate(ctx, CloneRequest(req)) {
		if err != nil {
			return committed, semanticDeltaEmitted, false, err
		}
		if event != nil {
			if hasStreamContent(event) {
				semanticDeltaEmitted = true
			}
			if isFinalResponse(event) {
				committed = true
			}
		} else {
			continue
		}
		if !yield(event, nil) {
			return committed, semanticDeltaEmitted, true, nil
		}
	}
	return committed, semanticDeltaEmitted, false, nil
}

func usesProviderExecutedTools(llm LLM, req *Request) bool {
	if usage, ok := llm.(ProviderExecutedToolUsage); ok {
		return usage.UsesProviderExecutedTools(CloneRequest(req))
	}
	return false
}

func isFinalResponse(event *StreamEvent) bool {
	if event == nil {
		return false
	}
	if event.Response != nil {
		if event.TurnComplete || event.StepComplete || event.Status == ResponseStatusCompleted {
			return true
		}
	}
	switch event.Type {
	case StreamEventMessageDone, StreamEventStepDone, StreamEventTurnDone:
		return true
	}
	return false
}

func hasStreamContent(event *StreamEvent) bool {
	if event == nil {
		return false
	}
	if event.PartDelta != nil {
		if event.PartDelta.TextDelta != "" || event.PartDelta.InputDelta != "" || event.PartDelta.Kind == PartKindToolUse {
			return true
		}
	}
	if event.Message != nil {
		if len(event.Message.Parts) > 0 {
			return true
		}
	}
	if event.Response != nil {
		return true
	}
	return false
}

func (l *retryingLLM) ProviderName() string {
	if l == nil || l.inner == nil {
		return ""
	}
	if provider, ok := l.inner.(interface{ ProviderName() string }); ok {
		return provider.ProviderName()
	}
	return ""
}

func (l *retryingLLM) WebSearchUnavailableReason() string {
	if l == nil || l.inner == nil {
		return ""
	}
	if reasoner, ok := l.inner.(WebSearchAvailability); ok {
		return strings.TrimSpace(reasoner.WebSearchUnavailableReason())
	}
	return ""
}

func (l *retryingLLM) ContextWindowTokens() int {
	if l == nil || l.inner == nil {
		return 0
	}
	if provider, ok := l.inner.(interface{ ContextWindowTokens() int }); ok {
		return provider.ContextWindowTokens()
	}
	return 0
}

func (l *retryingSearchLLM) SearchWeb(ctx context.Context, req WebSearchRequest) (WebSearchResponse, error) {
	if l == nil || l.retryingLLM == nil || l.inner == nil {
		return WebSearchResponse{}, errors.New("model: llm is nil")
	}
	searcher, ok := l.inner.(WebSearcher)
	if !ok {
		return WebSearchResponse{}, errors.New("model: web search is unavailable for this provider")
	}
	var resp WebSearchResponse
	err := l.retryUntilOK(ctx, func(int) error {
		var err error
		resp, err = searcher.SearchWeb(ctx, req)
		return err
	}, nil, nil)
	return resp, err
}

func (l *retryingLLM) retryUntilOK(
	ctx context.Context,
	run func(attempt int) error,
	beforeRetry func(AttemptReset) (bool, error),
	shouldRetry func(error) bool,
) error {
	if shouldRetry == nil {
		shouldRetry = IsRetryableLLMError
	}
	for attempt := 0; ; attempt++ {
		err := run(attempt)
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if !shouldRetry(err) {
			return err
		}
		policy := retryPolicyForError(l.cfg, err)
		if attempt >= policy.maxRetries {
			return retryExhaustedError(policy, err)
		}
		delay := RetryDelayForAttempt(attempt, policy.baseDelay, policy.maxDelay)
		if beforeRetry != nil {
			keepGoing, hookErr := beforeRetry(AttemptReset{
				Attempt:          attempt + 1,
				Retrying:         true,
				MaxRetries:       policy.maxRetries,
				RetryDelayMillis: delay.Milliseconds(),
			})
			if hookErr != nil {
				return hookErr
			}
			if !keepGoing {
				return nil
			}
		}
		if sleepErr := sleepRetryDelay(ctx, delay); sleepErr != nil {
			return sleepErr
		}
	}
}

func retryExhaustedError(policy retryPolicy, err error) error {
	if err == nil {
		return nil
	}
	return &RetryExhaustedError{
		MaxRetries:   policy.maxRetries,
		Backpressure: policy.backpressure,
		Cause:        err,
	}
}

// RetryExhaustedError reports that one model request exhausted its retry budget.
type RetryExhaustedError struct {
	MaxRetries   int
	Backpressure bool
	Cause        error
}

func (e *RetryExhaustedError) Error() string {
	if e == nil {
		return ""
	}
	message := retryExhaustedMessage(e.MaxRetries, e.Backpressure, true)
	if e.Cause == nil {
		return message
	}
	return fmt.Sprintf("%s: %v", message, e.Cause)
}

func (e *RetryExhaustedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// DisplayMessage returns the provider-detail-free retry failure summary.
func (e *RetryExhaustedError) DisplayMessage() string {
	if e == nil {
		return ""
	}
	return retryExhaustedMessage(e.MaxRetries, e.Backpressure, false)
}

func retryExhaustedMessage(maxRetries int, backpressure bool, includeRuntimePrefix bool) string {
	label := "request failed"
	if backpressure {
		label = "request hit provider backpressure"
	}
	if includeRuntimePrefix {
		label = "model: llm " + label
	} else {
		label = "model " + label
	}
	if maxRetries <= 0 {
		return label
	}
	return fmt.Sprintf("%s after %d retries", label, maxRetries)
}

func retryPolicyForError(cfg RetryConfig, err error) retryPolicy {
	cfg = NormalizeRetryConfig(cfg)
	if IsBackpressureLLMError(err) {
		return retryPolicy{
			maxRetries:   cfg.RateLimitMaxRetries,
			baseDelay:    cfg.RateLimitBaseDelay,
			maxDelay:     cfg.RateLimitMaxDelay,
			backpressure: true,
		}
	}
	return retryPolicy{
		maxRetries: cfg.MaxRetries,
		baseDelay:  cfg.BaseDelay,
		maxDelay:   cfg.MaxDelay,
	}
}

// RetryDelayForAttempt returns bounded exponential backoff for one retry index.
func RetryDelayForAttempt(retry int, baseDelay, maxDelay time.Duration) time.Duration {
	if retry < 0 {
		retry = 0
	}
	if baseDelay <= 0 {
		baseDelay = time.Second
	}
	if maxDelay <= 0 {
		maxDelay = baseDelay
	}
	delay := baseDelay
	for i := 0; i < retry; i++ {
		delay *= 2
		if delay >= maxDelay {
			return maxDelay
		}
	}
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func sleepRetryDelay(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// IsRetryableLLMError classifies model request failures that are safe to retry.
// Provider request errors are retried broadly because some compatible gateways
// occasionally return malformed 4xx responses for otherwise valid payloads. We
// still exclude caller cancellation and context overflow so control flow and
// compaction recovery stay immediate. Caller context deadlines are handled by
// retryUntilOK before consulting this classifier; provider-layer deadlines are
// retryable when the caller context is still active.
func IsRetryableLLMError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || IsContextOverflow(err) {
		return false
	}
	var retryable RetryableError
	if errors.As(err, &retryable) {
		return retryable.Retryable()
	}
	return true
}

// IsBackpressureLLMError classifies provider-side rate limit or overload.
func IsBackpressureLLMError(err error) bool {
	if err == nil {
		return false
	}
	var backpressure BackpressureError
	if errors.As(err, &backpressure) {
		return backpressure.Backpressure()
	}
	status, hasStatus := httpStatusCodeFromError(err)
	if hasStatus && (status == 429 || status == 529) {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if text == "" {
		return false
	}
	return strings.Contains(text, "rate limit") ||
		strings.Contains(text, "ratelimit") ||
		strings.Contains(text, "too many requests") ||
		strings.Contains(text, "overloaded_error") ||
		strings.Contains(text, "server overloaded") ||
		strings.Contains(text, "codefree server overloaded") ||
		strings.Contains(text, "retcode=51")
}

func httpStatusCodeFromError(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	text := strings.TrimSpace(err.Error())
	idx := strings.Index(strings.ToLower(text), "http status ")
	if idx < 0 {
		return 0, false
	}
	rest := text[idx+len("http status "):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	var status int
	if _, scanErr := fmt.Sscanf(rest[:end], "%d", &status); scanErr != nil || status <= 0 {
		return 0, false
	}
	return status, true
}

func isLikelyNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if text == "" {
		return false
	}
	for _, phrase := range []string{
		"connection reset",
		"connection refused",
		"connection aborted",
		"broken pipe",
		"unexpected eof",
		"eof",
		"no such host",
		"i/o timeout",
		"tls handshake timeout",
		"timeout awaiting response headers",
		"stream first event timeout",
		"temporary failure",
	} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}
