package runtime

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const (
	defaultGuardrailTimeout  = 5 * time.Second
	maxOutstandingGuardrails = 32
)

type lifecycleScopeContextKey struct{}

type lifecycleScope struct {
	sessionRef session.SessionRef
	runID      string
	turnID     string
}

func withLifecycleScope(ctx context.Context, scope lifecycleScope) context.Context {
	return context.WithValue(ctx, lifecycleScopeContextKey{}, scope)
}

func lifecycleScopeFromContext(ctx context.Context) lifecycleScope {
	if ctx == nil {
		return lifecycleScope{}
	}
	scope, _ := ctx.Value(lifecycleScopeContextKey{}).(lifecycleScope)
	return scope
}

func (r *Runtime) lifecycleEvent(ctx context.Context, operation agent.LifecycleOperation, name, stepID string) agent.LifecycleEvent {
	scope := lifecycleScopeFromContext(ctx)
	return agent.LifecycleEvent{
		Operation:  operation,
		SessionRef: scope.sessionRef,
		RunID:      scope.runID,
		TurnID:     scope.turnID,
		StepID:     strings.TrimSpace(stepID),
		Name:       strings.TrimSpace(name),
	}
}

func (r *Runtime) executeLifecycle(ctx context.Context, event agent.LifecycleEvent, next agent.LifecycleNext) error {
	options := agent.LifecycleOptions{}
	if r != nil {
		options = r.lifecycle
	}
	return agent.ExecuteLifecycle(ctx, event, options, next)
}

func validateGuardrailSpecs(specs []agent.GuardrailSpec) error {
	for i, spec := range specs {
		if spec.Guardrail == nil {
			return fmt.Errorf("agent-sdk/runtime: guardrail %d is nil", i)
		}
		if spec.Timeout < 0 {
			return fmt.Errorf("agent-sdk/runtime: guardrail %q timeout cannot be negative", spec.Guardrail.Name())
		}
		switch spec.OnFailure {
		case "", agent.GuardrailFailClosed, agent.GuardrailFailOpen:
		default:
			return fmt.Errorf("agent-sdk/runtime: guardrail %q has invalid failure policy %q", spec.Guardrail.Name(), spec.OnFailure)
		}
	}
	return nil
}

func (r *Runtime) applyGuardrails(ctx context.Context, activeSession session.Session, req agent.RunRequest) (agent.RunRequest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil || len(r.guardrails) == 0 {
		return req, nil
	}
	current := agent.GuardrailInput{
		SessionRef:   session.NormalizeSessionRef(activeSession.SessionRef),
		Input:        req.Input,
		DisplayInput: req.DisplayInput,
		ContentParts: cloneContentParts(req.ContentParts),
	}
	for _, spec := range r.guardrails {
		guardrailName := strings.TrimSpace(spec.Guardrail.Name())
		timeout := spec.Timeout
		if timeout == 0 {
			timeout = defaultGuardrailTimeout
		}
		guardCtx, cancel := context.WithTimeout(ctx, timeout)
		var next agent.GuardrailInput
		event := agent.LifecycleEvent{
			Operation:  agent.LifecycleGuardrail,
			SessionRef: current.SessionRef,
			Name:       guardrailName,
		}
		err := r.executeLifecycle(guardCtx, event, func(callCtx context.Context) error {
			select {
			case r.guardrailSlots <- struct{}{}:
			default:
				return errorcode.New(errorcode.ResourceExhausted, fmt.Sprintf("agent-sdk/runtime: guardrail outstanding limit reached (%d)", maxOutstandingGuardrails))
			}
			type applyResult struct {
				input agent.GuardrailInput
				err   error
			}
			done := make(chan applyResult, 1)
			input := cloneGuardrailInput(current)
			go func() {
				result := applyResult{}
				defer func() {
					defer func() { <-r.guardrailSlots }()
					if recovered := recover(); recovered != nil {
						result.err = fmt.Errorf("agent-sdk/runtime: guardrail %q panic: %v", guardrailName, recovered)
					}
					done <- result
				}()
				result.input, result.err = spec.Guardrail.ApplyGuardrail(callCtx, input)
			}()
			select {
			case result := <-done:
				next = result.input
				return result.err
			case <-callCtx.Done():
				return callCtx.Err()
			}
		})
		cancel()
		if err != nil {
			var rejection *agent.GuardrailRejectionError
			if errors.As(err, &rejection) || spec.OnFailure != agent.GuardrailFailOpen {
				return agent.RunRequest{}, err
			}
			continue
		}
		current = cloneGuardrailInput(next)
		current.SessionRef = session.NormalizeSessionRef(activeSession.SessionRef)
	}
	req.Input = current.Input
	req.DisplayInput = current.DisplayInput
	req.ContentParts = cloneContentParts(current.ContentParts)
	return req, nil
}

func cloneGuardrailInput(in agent.GuardrailInput) agent.GuardrailInput {
	in.SessionRef = session.NormalizeSessionRef(in.SessionRef)
	in.ContentParts = cloneContentParts(in.ContentParts)
	return in
}

func cloneContentParts(in []model.ContentPart) []model.ContentPart {
	return append([]model.ContentPart(nil), in...)
}

type lifecycleLLM struct {
	inner   model.LLM
	runtime *Runtime
}

type lifecycleSearchLLM struct{ *lifecycleLLM }

func (l *lifecycleLLM) Name() string {
	if l == nil || l.inner == nil {
		return ""
	}
	return l.inner.Name()
}

func (l *lifecycleLLM) Capabilities() model.Capabilities {
	capabilities, _ := model.CapabilitiesOf(l.inner)
	return capabilities
}

func (l *lifecycleLLM) ContextWindowTokens() int {
	if provider, ok := l.inner.(interface{ ContextWindowTokens() int }); ok {
		return provider.ContextWindowTokens()
	}
	return 0
}

func (l *lifecycleLLM) ProviderName() string {
	if provider, ok := l.inner.(interface{ ProviderName() string }); ok {
		return strings.TrimSpace(provider.ProviderName())
	}
	return ""
}

func (l *lifecycleLLM) WebSearchUnavailableReason() string {
	if provider, ok := l.inner.(model.WebSearchAvailability); ok {
		return strings.TrimSpace(provider.WebSearchUnavailableReason())
	}
	return ""
}

func (l *lifecycleLLM) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if l == nil || l.inner == nil {
			yield(nil, errors.New("model: llm is nil"))
			return
		}
		event := l.runtime.lifecycleEvent(ctx, agent.LifecycleModel, l.inner.Name(), "")
		err := l.runtime.executeLifecycle(ctx, event, func(callCtx context.Context) error {
			for streamEvent, streamErr := range l.inner.Generate(callCtx, model.CloneRequest(req)) {
				if streamErr != nil {
					return streamErr
				}
				if !yield(streamEvent, nil) {
					return nil
				}
			}
			return nil
		})
		if err != nil {
			yield(nil, err)
		}
	}
}

func (l *lifecycleSearchLLM) SearchWeb(ctx context.Context, req model.WebSearchRequest) (model.WebSearchResponse, error) {
	searcher, ok := l.inner.(model.WebSearcher)
	if !ok {
		return model.WebSearchResponse{}, errors.New("model: web search is unavailable for this provider")
	}
	var response model.WebSearchResponse
	event := l.runtime.lifecycleEvent(ctx, agent.LifecycleModel, l.inner.Name(), "web_search")
	err := l.runtime.executeLifecycle(ctx, event, func(callCtx context.Context) error {
		var searchErr error
		response, searchErr = searcher.SearchWeb(callCtx, req)
		return searchErr
	})
	return response, err
}

func (r *Runtime) wrapModelForLifecycle(llm model.LLM) model.LLM {
	if llm == nil {
		return nil
	}
	wrapped := &lifecycleLLM{inner: llm, runtime: r}
	if _, ok := llm.(model.WebSearcher); ok {
		return &lifecycleSearchLLM{lifecycleLLM: wrapped}
	}
	return wrapped
}

type lifecycleTool struct {
	inner   tool.Tool
	runtime *Runtime
}

func (t lifecycleTool) Definition() tool.Definition {
	return tool.CloneDefinition(t.inner.Definition())
}

func (t lifecycleTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	var result tool.Result
	event := t.runtime.lifecycleEvent(ctx, agent.LifecycleTool, call.Name, call.ID)
	err := t.runtime.executeLifecycle(ctx, event, func(callCtx context.Context) error {
		var callErr error
		result, callErr = t.inner.Call(callCtx, call)
		return callErr
	})
	return result, err
}

func (r *Runtime) wrapToolsForLifecycle(tools []tool.Tool) []tool.Tool {
	if len(tools) == 0 {
		return tools
	}
	out := make([]tool.Tool, 0, len(tools))
	for _, item := range tools {
		if item != nil {
			out = append(out, lifecycleTool{inner: item, runtime: r})
		}
	}
	return out
}
