package model

import (
	"context"
	"errors"
	"iter"
	"strings"

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
	if observer, _ := ctx.Value(invocationObserverKey{}).(func(Invocation)); observer == nil {
		return llm.Generate(ctx, CloneRequest(req))
	}
	return func(yield func(*StreamEvent, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		receipt := Invocation{ID: uuid.NewString(), Model: strings.TrimSpace(llm.Name()), Outcome: "failed"}
		if provider, ok := llm.(interface{ ProviderName() string }); ok {
			receipt.Provider = strings.TrimSpace(provider.ProviderName())
		}
		var cause error
		defer func() {
			if errors.Is(cause, context.Canceled) || ctx.Err() != nil {
				receipt.Outcome = "cancelled"
			}
			if observer, _ := ctx.Value(invocationObserverKey{}).(func(Invocation)); observer != nil {
				observer(receipt)
			}
		}()
		for event, err := range llm.Generate(ctx, CloneRequest(req)) {
			if event != nil && event.Response != nil {
				cloned := *event
				response := *event.Response
				response.InvocationID = receipt.ID
				cloned.Response = &response
				event = &cloned
				if response.Usage.IsReported() {
					receipt.Usage = response.Usage
				}
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
