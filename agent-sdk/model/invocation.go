package model

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Invocation is one actual provider attempt. It contains no prompt or response
// text. A receipt at attempt termination does not prove crash-time completeness.
type Invocation struct {
	ID       string
	Provider string
	Model    string
	Usage    Usage
	Outcome  string
}

type invocationObserverKey struct{}

type invocationUsageKey struct{}
type invocationUsage struct {
	mu     sync.Mutex
	latest Usage
}

// RecordInvocationUsage retains a provider-normalized cumulative measurement
// as soon as it is decoded, even if no successful response is emitted later.
// It updates only the current actual attempt, never adds cumulative chunks,
// and is a no-op outside Generate's invocation accounting context.
func RecordInvocationUsage(ctx context.Context, usage Usage) {
	if !usage.IsReported() {
		return
	}
	if state, _ := ctx.Value(invocationUsageKey{}).(*invocationUsage); state != nil {
		state.mu.Lock()
		state.latest = usage
		state.mu.Unlock()
	}
}

// WithInvocationObserver installs a synchronous, caller-owned accounting sink.
// Provider payloads cannot install observers or choose invocation identities.
func WithInvocationObserver(ctx context.Context, observer func(Invocation)) context.Context {
	if observer == nil {
		return ctx
	}
	if parent, _ := ctx.Value(invocationObserverKey{}).(func(Invocation)); parent != nil {
		next := observer
		observer = func(in Invocation) { next(in); parent(in) }
	}
	return context.WithValue(ctx, invocationObserverKey{}, observer)
}

type invocationAdmissionKey struct{}

// WithInvocationAdmission installs an embedding-owned gate before each actual
// provider attempt, including retries. Rejection creates no invocation receipt.
func WithInvocationAdmission(ctx context.Context, admit func(context.Context, *Request) error) context.Context {
	if admit == nil {
		return ctx
	}
	if parent, ok := ctx.Value(invocationAdmissionKey{}).(func(context.Context, *Request) error); ok {
		next := admit
		admit = func(ctx context.Context, req *Request) error {
			if err := parent(ctx, req); err != nil {
				return err
			}
			return next(ctx, req)
		}
	}
	return context.WithValue(ctx, invocationAdmissionKey{}, admit)
}

// InvocationTracker is implemented by request gates and retry wrappers that
// delegate accounting to their actual provider attempts rather than counting
// their own admission or orchestration as an attempt.
type InvocationTracker interface{ TracksInvocations() }

// Generate observes each actual attempt, including unwrapped providers. Nested
// gates must implement InvocationTracker and delegate through Generate.
func Generate(ctx context.Context, llm LLM, req *Request) iter.Seq2[*StreamEvent, error] {
	if _, ok := llm.(InvocationTracker); ok {
		return llm.Generate(ctx, req)
	}
	if admit, ok := ctx.Value(invocationAdmissionKey{}).(func(context.Context, *Request) error); ok {
		if err := admit(ctx, CloneRequest(req)); err != nil {
			return func(yield func(*StreamEvent, error) bool) { yield(nil, err) }
		}
	}
	if observer, _ := ctx.Value(invocationObserverKey{}).(func(Invocation)); observer == nil {
		return llm.Generate(ctx, CloneRequest(req))
	}
	return func(yield func(*StreamEvent, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		receipt := Invocation{ID: uuid.NewString(), Model: strings.TrimSpace(llm.Name()), Outcome: "failed"}
		usage := &invocationUsage{}
		attemptCtx := context.WithValue(ctx, invocationUsageKey{}, usage)
		if provider, ok := llm.(interface{ ProviderName() string }); ok {
			receipt.Provider = strings.TrimSpace(provider.ProviderName())
		}
		var cause error
		defer func() {
			usage.mu.Lock()
			receipt.Usage = usage.latest
			usage.mu.Unlock()
			if errors.Is(cause, context.Canceled) || ctx.Err() != nil {
				receipt.Outcome = "cancelled"
			}
			if observer, _ := ctx.Value(invocationObserverKey{}).(func(Invocation)); observer != nil {
				observer(receipt)
			}
		}()
		for event, err := range llm.Generate(attemptCtx, CloneRequest(req)) {
			if event != nil && event.Response != nil {
				cloned := *event
				response := *event.Response
				response.InvocationID = receipt.ID
				cloned.Response = &response
				event = &cloned
				RecordInvocationUsage(attemptCtx, response.Usage)
				if receipt.Provider == "" {
					receipt.Provider = strings.TrimSpace(response.Provider)
				}
				if response.TurnComplete {
					receipt.Outcome = "completed"
				}
			}
			if err != nil {
				cause = err
				receipt.Outcome = "failed"
			}
			if !yield(event, err) {
				return
			}
			if err != nil {
				return
			}
		}
	}
}

// IsReported distinguishes an omitted measurement from an explicit zero. The
// nonzero fallback preserves providers written before Reported was introduced.
func (u Usage) IsReported() bool {
	return u.Reported || u.PromptTokens != 0 || u.CachedInputTokens != 0 || u.CompletionTokens != 0 || u.ReasoningTokens != 0 || u.TotalTokens != 0 || u.CostMicros != 0
}
