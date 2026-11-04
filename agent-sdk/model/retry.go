package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"math/rand/v2"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

const (
	defaultLLMRequestMaxRetries       = -1
	defaultLLMRetryBaseDelay          = time.Second
	defaultLLMRetryMaxDelay           = time.Minute
	defaultRateLimitRequestMaxRetries = -1
	defaultRateLimitRetryBaseDelay    = 5 * time.Second
	defaultRateLimitRetryMaxDelay     = time.Minute
)

// RetryConfig controls retry behavior for one provider-neutral LLM call.
// Zero or negative retry counts recover until caller cancellation. Positive
// counts retain an embedding owner's finite retry budget.
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

// RetryLimitError lets one failure class clamp an explicitly finite retry count.
// Continuous recovery ignores latency-based count limits and retains the caller
// context as its lifecycle boundary.
type RetryLimitError interface {
	RetryMaxRetries() int
}

// RetryConnectionResetter is an optional provider capability used to evict
// pooled connections before replaying a failed request.
type RetryConnectionResetter interface {
	ResetConnectionsForRetry(error)
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
// alone. A failed request with provider-side tools has an unknown execution
// outcome even without semantic output and cannot be replayed automatically.
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
		hasProviderTool := UsesProviderExecutedTools(l.inner, req)
		stopped := false
		canRetryLastErr := true

		err := l.retryUntilOK(ctx, func(int) error {
			canRetryLastErr = true
			committed, attemptStopped, err := l.runAttempt(ctx, req, yield)
			if attemptStopped {
				stopped = true
				return nil
			}
			if err == nil {
				return nil
			}
			canRetryLastErr = !committed && !hasProviderTool
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

func (*retryingLLM) TracksInvocations() {}

func (l *retryingLLM) runAttempt(ctx context.Context, req *Request, yield func(*StreamEvent, error) bool) (bool, bool, error) {
	committed := false
	for event, err := range Generate(ctx, l.inner, req) {
		if err != nil {
			return committed, false, err
		}
		if event != nil {
			if isFinalResponse(event) {
				committed = true
			}
		} else {
			continue
		}
		if !yield(event, nil) {
			return committed, true, nil
		}
	}
	return committed, false, nil
}

// UsesProviderExecutedTools reports whether this exact request can execute tools
// at the provider. It does not infer configuration from general capabilities.
func UsesProviderExecutedTools(llm LLM, req *Request) bool {
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
	// SearchWeb is an explicit, model-visible tool action. It respects the
	// caller context and any explicitly configured provider request timeout.
	// Return one failed attempt to the model so it can decide whether a revised
	// or repeated query is useful; the Generate retry policy must not multiply
	// the latency of each search call.
	return searcher.SearchWeb(ctx, req)
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
	effectiveMaxRetries := -1
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
		if policy.maxRetries >= 0 && (effectiveMaxRetries < 0 || policy.maxRetries < effectiveMaxRetries) {
			effectiveMaxRetries = policy.maxRetries
		}
		policy.maxRetries = effectiveMaxRetries
		if policy.maxRetries >= 0 && attempt >= policy.maxRetries {
			return retryExhaustedError(policy, attempt, err)
		}
		delay := RetryDelayForAttempt(attempt, policy.baseDelay, policy.maxDelay)
		// Equal jitter avoids near-zero probes, including after the cap. A
		// server's minimum wait wins even when it exceeds our backoff cap.
		if delay > 1 {
			delay = delay/2 + time.Duration(rand.Int64N(int64(delay-delay/2)))
		}
		var after RetryAfterError
		if errors.As(err, &after) {
			delay = max(delay, after.RetryAfter())
		}
		if resetter, ok := l.inner.(RetryConnectionResetter); ok {
			resetter.ResetConnectionsForRetry(err)
		}
		if beforeRetry != nil {
			keepGoing, hookErr := beforeRetry(AttemptReset{
				Attempt:          attempt + 1,
				Retrying:         true,
				MaxRetries:       policy.maxRetries,
				RetryDelayMillis: max(int64(1), delay.Milliseconds()),
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

func retryExhaustedError(policy retryPolicy, retries int, err error) error {
	if err == nil {
		return nil
	}
	return &RetryExhaustedError{
		MaxRetries:   max(0, retries),
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

func (e *RetryExhaustedError) ErrorCode() errorcode.Code {
	if e != nil && e.Backpressure {
		return errorcode.Overloaded
	}
	return errorcode.Unavailable
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
	limit := func(maxRetries int) int {
		if maxRetries < 0 {
			return maxRetries
		}
		var limiter RetryLimitError
		if errors.As(err, &limiter) {
			if retryLimit := limiter.RetryMaxRetries(); retryLimit >= 0 && retryLimit < maxRetries {
				return retryLimit
			}
		}
		return maxRetries
	}
	if IsBackpressureLLMError(err) {
		return retryPolicy{
			maxRetries:   limit(cfg.RateLimitMaxRetries),
			baseDelay:    cfg.RateLimitBaseDelay,
			maxDelay:     cfg.RateLimitMaxDelay,
			backpressure: true,
		}
	}
	return retryPolicy{
		maxRetries: limit(cfg.MaxRetries),
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
		if delay >= maxDelay/2 {
			return maxDelay
		}
		delay *= 2
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

// IsRetryableLLMError classifies transport and structured transient failures.
// Unknown errors, rejected requests, authority and safety budgets are terminal.
// The caller's context is checked before this classifier, so a child request
// deadline can recover without extending a parent deadline or cancellation.
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
	switch errorcode.CodeOf(err) {
	case errorcode.Unavailable, errorcode.Timeout, errorcode.RateLimited, errorcode.Overloaded:
		return true
	case errorcode.Unknown:
		var network net.Error
		var dns *net.DNSError
		if errors.As(err, &dns) && dns.IsNotFound {
			return false
		}
		return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout() || dns != nil && dns.IsTemporary ||
			errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
			errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
			errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH)
	default:
		return false
	}
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
	code := errorcode.CodeOf(err)
	return code == errorcode.RateLimited || code == errorcode.Overloaded
}
