package runtime

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type autoCompactDecision struct {
	Reason        string
	Kind          compactionRecoveryKind
	Usage         compact.UsageSnapshot
	RequestTokens int
	Model         model.LLM
	Events        []*session.Event
}

type autoCompactRequiredError struct {
	decision autoCompactDecision
}

func (e *autoCompactRequiredError) Error() string {
	if e == nil {
		return ""
	}
	reason := strings.TrimSpace(e.decision.Reason)
	if reason == "" {
		reason = "context_watermark"
	}
	return fmt.Sprintf("agent-sdk/runtime: auto compact required before model request: %s", reason)
}

func autoCompactRequired(err error) (autoCompactDecision, bool) {
	var required *autoCompactRequiredError
	if !errors.As(err, &required) || required == nil {
		return autoCompactDecision{}, false
	}
	return required.decision, true
}

type autoCompactGatedLLM struct {
	inner      model.LLM
	runtime    *Runtime
	sessionRef session.SessionRef
}

type autoCompactGatedSearchLLM struct {
	*autoCompactGatedLLM
}

func (l *autoCompactGatedLLM) Name() string {
	if l == nil || l.inner == nil {
		return ""
	}
	return l.inner.Name()
}

func (l *autoCompactGatedLLM) ContextWindowTokens() int {
	if l == nil || l.inner == nil {
		return 0
	}
	if provider, ok := l.inner.(interface{ ContextWindowTokens() int }); ok {
		return provider.ContextWindowTokens()
	}
	return 0
}

func (l *autoCompactGatedLLM) Capabilities() model.Capabilities {
	capabilities, _ := model.CapabilitiesOf(l.inner)
	return capabilities
}

func (l *autoCompactGatedLLM) ProviderName() string {
	if l == nil || l.inner == nil {
		return ""
	}
	if provider, ok := l.inner.(interface{ ProviderName() string }); ok {
		return strings.TrimSpace(provider.ProviderName())
	}
	return ""
}

func (l *autoCompactGatedLLM) WebSearchUnavailableReason() string {
	if l == nil || l.inner == nil {
		return ""
	}
	if reasoner, ok := l.inner.(model.WebSearchAvailability); ok {
		return strings.TrimSpace(reasoner.WebSearchUnavailableReason())
	}
	return ""
}

func (l *autoCompactGatedLLM) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if l == nil || l.inner == nil {
			yield(nil, errors.New("model: llm is nil"))
			return
		}
		if decision, err := l.runtime.autoCompactDecisionBeforeModelRequest(ctx, l.sessionRef, l.inner, req); err != nil {
			yield(nil, err)
			return
		} else if strings.TrimSpace(decision.Reason) != "" {
			yield(nil, &autoCompactRequiredError{decision: decision})
			return
		}
		for event, err := range l.inner.Generate(ctx, model.CloneRequest(req)) {
			if err != nil {
				if decision, compact, decisionErr := l.runtime.autoCompactDecisionAfterModelRequestFailure(ctx, l.sessionRef, l.inner, req, err); decisionErr != nil {
					yield(nil, decisionErr)
					return
				} else if compact {
					yield(nil, &autoCompactRequiredError{decision: decision})
					return
				}
			}
			if !yield(event, err) {
				return
			}
		}
	}
}

func (l *autoCompactGatedSearchLLM) SearchWeb(ctx context.Context, req model.WebSearchRequest) (model.WebSearchResponse, error) {
	if l == nil || l.autoCompactGatedLLM == nil || l.inner == nil {
		return model.WebSearchResponse{}, errors.New("model: llm is nil")
	}
	searcher, ok := l.inner.(model.WebSearcher)
	if !ok {
		return model.WebSearchResponse{}, errors.New("model: web search is unavailable for this provider")
	}
	return searcher.SearchWeb(ctx, req)
}

func (r *Runtime) wrapModelForAutoCompaction(ref session.SessionRef, llm model.LLM) model.LLM {
	if r == nil || llm == nil || !r.compaction.Enabled {
		return llm
	}
	if _, ok := r.compactor.(compact.ForceEngine); !ok {
		return llm
	}
	switch llm.(type) {
	case *autoCompactGatedLLM, *autoCompactGatedSearchLLM:
		return llm
	}
	wrapped := &autoCompactGatedLLM{
		inner:      llm,
		runtime:    r,
		sessionRef: session.NormalizeSessionRef(ref),
	}
	if _, ok := llm.(model.WebSearcher); ok {
		return &autoCompactGatedSearchLLM{autoCompactGatedLLM: wrapped}
	}
	return wrapped
}

func (r *Runtime) autoCompactDecisionBeforeModelRequest(
	ctx context.Context,
	ref session.SessionRef,
	llm model.LLM,
	req *model.Request,
) (autoCompactDecision, error) {
	view, ok, err := r.autoCompactModelRequestView(ctx, ref, llm, req)
	if err != nil || !ok {
		return autoCompactDecision{}, err
	}
	trigger := evaluateWatermark(view.usage, r.compaction)
	if !trigger.ShouldCompact {
		return autoCompactDecision{}, nil
	}
	return autoCompactDecision{
		Reason:        modelRequestWatermarkReason(trigger.Reason),
		Kind:          compactionRecoveryKindWatermark,
		Usage:         view.usage,
		RequestTokens: view.requestTokens,
		Model:         llm,
		Events:        view.events,
	}, nil
}

func (r *Runtime) autoCompactDecisionAfterModelRequestFailure(
	ctx context.Context,
	ref session.SessionRef,
	llm model.LLM,
	req *model.Request,
	cause error,
) (autoCompactDecision, bool, error) {
	var exhausted *model.RetryExhaustedError
	if !errors.As(cause, &exhausted) {
		return autoCompactDecision{}, false, nil
	}
	view, ok, err := r.autoCompactModelRequestView(ctx, ref, llm, req)
	if err != nil || !ok {
		return autoCompactDecision{}, false, err
	}
	if !evaluateEmergencyWatermark(view.usage, r.compaction) {
		return autoCompactDecision{}, false, nil
	}
	return autoCompactDecision{
		Reason:        "model_request_retry_exhausted_high_water",
		Kind:          compactionRecoveryKindRetryExhausted,
		Usage:         view.usage,
		RequestTokens: view.requestTokens,
		Model:         llm,
		Events:        view.events,
	}, true, nil
}

type autoCompactModelRequestView struct {
	usage         compact.UsageSnapshot
	requestTokens int
	events        []*session.Event
}

func (r *Runtime) autoCompactModelRequestView(
	ctx context.Context,
	ref session.SessionRef,
	llm model.LLM,
	req *model.Request,
) (autoCompactModelRequestView, bool, error) {
	if r == nil || r.sessions == nil || llm == nil || req == nil || !r.compaction.Enabled {
		return autoCompactModelRequestView{}, false, nil
	}
	if err := ctx.Err(); err != nil {
		return autoCompactModelRequestView{}, false, err
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return autoCompactModelRequestView{}, false, err
	}
	events = mainInvocationEvents(events)
	delta := compactableEvents(events)
	if len(delta) == 0 || !autoCompactGateHasModelProgress(delta) {
		return autoCompactModelRequestView{}, false, nil
	}
	usage, requestTokens := usageForModelRequest(events, llm, req, r.compaction)
	if usage.EffectiveInputBudget <= 0 {
		return autoCompactModelRequestView{}, false, nil
	}
	return autoCompactModelRequestView{
		usage:         usage,
		requestTokens: requestTokens,
		events:        events,
	}, true, nil
}

// Model-request gates only compact after the current user message has model/tool
// progress. If no user event exists after the latest checkpoint, any model/tool
// progress in the delta remains eligible.
func autoCompactGateHasModelProgress(events []*session.Event) bool {
	start := 0
	for i := len(events) - 1; i >= 0; i-- {
		if session.EventTypeOf(events[i]) == session.EventTypeUser {
			start = i + 1
			break
		}
	}
	for _, event := range events[start:] {
		switch session.EventTypeOf(event) {
		case session.EventTypeAssistant, session.EventTypeToolCall, session.EventTypeToolResult:
			return true
		}
	}
	return false
}

func modelRequestWatermarkReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "context_limit":
		return "model_request_context_limit"
	case "context_watermark":
		return "model_request_context_watermark"
	default:
		return strings.TrimSpace(reason)
	}
}
