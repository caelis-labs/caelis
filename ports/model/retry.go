package model

import (
	"context"
	"encoding/json"
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
	if wrapped, ok := llm.(*retryingLLM); ok {
		llm = wrapped.inner
	}
	return &retryingLLM{
		inner: llm,
		cfg:   NormalizeRetryConfig(cfg),
	}
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

func (l *retryingLLM) Name() string {
	if l == nil || l.inner == nil {
		return ""
	}
	return l.inner.Name()
}

func (l *retryingLLM) Generate(ctx context.Context, req *Request) iter.Seq2[*StreamEvent, error] {
	return func(yield func(*StreamEvent, error) bool) {
		if l == nil || l.inner == nil {
			yield(nil, errors.New("model: llm is nil"))
			return
		}
		hasProviderTool := hasProviderExecutedTools(req)

		for attempt := 0; ; attempt++ {
			committed, semanticDeltaEmitted, stopped, err := l.runAttempt(ctx, req, yield)
			if stopped {
				return
			}
			if err == nil {
				return
			}
			cannotRetry := committed || (hasProviderTool && semanticDeltaEmitted)
			if cannotRetry || !IsRetryableLLMError(err) {
				yield(nil, err)
				return
			}
			policy := retryPolicyForError(l.cfg, err)
			if attempt >= policy.maxRetries {
				yield(nil, retryExhaustedError(policy, err))
				return
			}
			resetEvent := &StreamEvent{
				Type: StreamEventAttemptReset,
				AttemptReset: &AttemptReset{
					Attempt:  attempt + 1,
					Cause:    err.Error(),
					Retrying: true,
				},
			}
			if !yield(resetEvent, nil) {
				return
			}
			if sleepErr := sleepRetryDelay(ctx, RetryDelayForAttempt(attempt, policy.baseDelay, policy.maxDelay)); sleepErr != nil {
				yield(nil, sleepErr)
				return
			}
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

func hasProviderExecutedTools(req *Request) bool {
	if req == nil {
		return false
	}
	for _, spec := range req.Tools {
		if spec.Kind == ToolSpecKindProviderExecuted || spec.ProviderExecuted != nil {
			return true
		}
	}
	return false
}

func isFinalResponse(event *StreamEvent) bool {
	if event == nil {
		return false
	}
	if event.Response != nil {
		if event.Response.TurnComplete || event.Response.StepComplete || event.Response.Status == ResponseStatusCompleted {
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

func (l *retryingLLM) ContextWindowTokens() int {
	if l == nil || l.inner == nil {
		return 0
	}
	if provider, ok := l.inner.(interface{ ContextWindowTokens() int }); ok {
		return provider.ContextWindowTokens()
	}
	return 0
}

func retryExhaustedError(policy retryPolicy, err error) error {
	if err == nil {
		return nil
	}
	if policy.backpressure {
		return fmt.Errorf("model: llm request hit provider backpressure after %d retries: %w", policy.maxRetries, err)
	}
	return fmt.Errorf("model: llm request failed after %d retries: %w", policy.maxRetries, err)
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

// IsRetryableLLMError classifies transient provider and transport failures.
func IsRetryableLLMError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || IsContextOverflow(err) {
		return false
	}
	var retryable RetryableError
	if errors.As(err, &retryable) {
		return retryable.Retryable()
	}
	if status, ok := httpStatusCodeFromError(err); ok {
		if status >= 500 {
			return true
		}
		switch status {
		case 408, 409, 425, 429:
			return true
		default:
			return false
		}
	}
	return IsBackpressureLLMError(err) || isLikelyNetworkError(err)
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

// CloneRequest returns a deep copy of one provider-neutral model request.
func CloneRequest(in *Request) *Request {
	if in == nil {
		return nil
	}
	out := *in
	out.Instructions = CloneParts(in.Instructions)
	out.Messages = CloneMessages(in.Messages)
	out.Tools = CloneToolSpecs(in.Tools)
	out.Output = CloneOutputSpec(in.Output)
	return &out
}

// CloneOutputSpec returns a deep copy of one output spec.
func CloneOutputSpec(in *OutputSpec) *OutputSpec {
	if in == nil {
		return nil
	}
	out := *in
	out.JSONSchema = cloneJSONMap(in.JSONSchema)
	return &out
}

// CloneToolSpecs returns a deep copy of model-visible tool declarations.
func CloneToolSpecs(in []ToolSpec) []ToolSpec {
	if len(in) == 0 {
		return nil
	}
	out := make([]ToolSpec, 0, len(in))
	for _, spec := range in {
		cp := spec
		if spec.Function != nil {
			fn := *spec.Function
			fn.Parameters = cloneJSONMap(spec.Function.Parameters)
			cp.Function = &fn
		}
		if spec.ProviderDefined != nil {
			defined := *spec.ProviderDefined
			defined.ProviderDetails = cloneRawMessageMap(spec.ProviderDefined.ProviderDetails)
			cp.ProviderDefined = &defined
		}
		if spec.ProviderExecuted != nil {
			executed := *spec.ProviderExecuted
			executed.ProviderDetails = cloneRawMessageMap(spec.ProviderExecuted.ProviderDetails)
			cp.ProviderExecuted = &executed
		}
		if spec.MCP != nil {
			mcp := *spec.MCP
			cp.MCP = &mcp
		}
		out = append(out, cp)
	}
	return out
}

func cloneRawMessageMap(in map[string]json.RawMessage) map[string]json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for key, value := range in {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}

func cloneJSONMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneJSONValue(value)
	}
	return out
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneJSONMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneJSONValue(item)
		}
		return out
	default:
		return typed
	}
}
