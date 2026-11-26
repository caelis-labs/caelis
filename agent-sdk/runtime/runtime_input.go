package runtime

import (
	"context"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/internal/runtimeinput"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) prepareTurnInputs(ctx context.Context, active session.Session, turnID string, req agent.RunRequest) ([]*session.Event, error) {
	events, err := buildRunInputEvents(active, turnID, req)
	if err != nil {
		return nil, err
	}
	events, err = r.prepareHookInputs(ctx, events)
	if err != nil {
		return nil, err
	}
	start, err := r.runTurnStartHooks(ctx)
	if err != nil {
		return nil, err
	}
	return append(events, start...), nil
}

func buildRunInputEvents(
	activeSession session.Session,
	turnID string,
	req agent.RunRequest,
) ([]*session.Event, error) {
	inputs := communicationInputsFromRunRequest(req)
	if len(inputs) == 0 {
		return nil, nil
	}
	events := make([]*session.Event, 0, len(inputs))
	for index, item := range inputs {
		event, err := runtimeinput.NewEvent(normalizeInputKind(req.InputKind), item, nil)
		if err != nil {
			return nil, err
		}
		if event == nil {
			continue
		}
		scope := defaultScope(activeSession, turnID)
		event.Scope = &scope
		event.IdempotencyKey = "turn-input:" + strings.TrimSpace(turnID)
		if req.InputCompaction != nil {
			cloned := *req.InputCompaction
			cloned.UserEvidence = append([]string(nil), cloned.UserEvidence...)
			event.Compaction = &cloned
		}
		if len(inputs) > 1 {
			event.IdempotencyKey = fmt.Sprintf("turn-input:%s:%d", strings.TrimSpace(turnID), index)
		}
		events = append(events, event)
	}
	return events, nil
}

func communicationInputsFromRunRequest(req agent.RunRequest) []agent.AgentCommunicationInput {
	if len(req.Inputs) > 0 {
		return agent.CloneAgentCommunicationInputs(req.Inputs)
	}
	if strings.TrimSpace(req.Input) == "" && len(req.ContentParts) == 0 {
		return nil
	}
	return []agent.AgentCommunicationInput{{
		Source:       session.CloneActorRef(req.InputActor),
		Input:        req.Input,
		DisplayInput: req.DisplayInput,
		ContentParts: append([]model.ContentPart(nil), req.ContentParts...),
	}}
}

func normalizeInputKind(kind agent.SubmissionKind) agent.SubmissionKind {
	if kind == "" {
		return agent.SubmissionKindConversation
	}
	return kind
}

func buildSteeringInputEvents(active session.Session, turnID string, submission agent.Submission) ([]*session.Event, error) {
	if err := agent.ValidateSubmissionInputs(submission); err != nil {
		return nil, err
	}
	events, err := buildRunInputEvents(active, turnID, agent.RunRequest{
		InputKind: submission.Kind, Input: submission.Text,
		DisplayInput: session.ResolveDisplayInput(submission.DisplayInput, submission.Metadata),
		ContentParts: submission.ContentParts, InputActor: submission.Actor, Inputs: submission.Inputs,
	})
	for _, event := range events {
		// A steering admission is distinct from the initial turn-input identity.
		event.IdempotencyKey = ""
		preserveSteeringCorrelation(event, submission.Metadata)
	}
	return events, err
}

// controllerInputParts projects the same effective messages passed to Session
// persistence into content supported by the ACP bridge.
func controllerInputParts(events []*session.Event) []model.ContentPart {
	var parts []model.ContentPart
	for _, event := range events {
		if event.Message != nil {
			parts = append(parts, model.ContentPartsFromParts(event.Message.Parts)...)
		}
	}
	return parts
}

// preserveSteeringCorrelation carries Control's input receipt identity through
// the canonical append. Correlation is separate from the Session-owned message
// identity and the typed Actor/Scope; submission metadata cannot replace them.
func preserveSteeringCorrelation(event *session.Event, metadata map[string]any) {
	if event == nil {
		return
	}
	for _, key := range []string{"operation_id", "steering_operation_id"} {
		value, ok := metadata[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		if event.Meta == nil {
			event.Meta = make(map[string]any, 2)
		}
		event.Meta[key] = value
	}
}

// appendInputEvents commits one admission before any of its events are published.
// A multi-message admission requires atomic storage; partial input is not a
// recoverable lifecycle record and must never fall back to individual appends.
func (r *Runtime) appendInputEvents(ctx context.Context, ref session.SessionRef, events []*session.Event) ([]*session.Event, error) {
	if len(events) == 0 {
		return nil, nil
	}
	guard := session.RuntimeMutationGuard(ctx)
	if len(events) == 1 {
		event, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, MutationGuard: guard, Event: events[0]})
		if err != nil {
			return nil, err
		}
		return []*session.Event{event}, nil
	}
	return r.sessions.AppendEvents(ctx, session.AppendEventsRequest{SessionRef: ref, MutationGuard: guard, Events: events})
}

// localInputContext keeps safe-point persistence and final admission closure
// with the producing Runtime, without exposing a Session writer to the Agent.
type localInputContext struct {
	agent.Context
	runner        *runner
	contextSource func() (hook.ContextSource, error)
	contextEdit   func(string, string, hook.ContextEdit, func(context.Context) (func(), error)) ([]*session.Event, error)
	commit        func(agent.Submission) ([]model.Message, error)
}

func (c *localInputContext) DrainFinalSubmissions() ([]agent.Submission, error) {
	taskScopeFromContext(c.Context).stopCodeMode()
	if !taskScopeFromContext(c.Context).wait(c.Context, c.runner.inputReadySignal()) {
		return c.runner.drainSubmissions(), nil
	}
	if scope := asyncScope(c.Context); scope != nil {
		if err := scope.drain(); err != nil {
			return nil, err
		}
	}
	return c.runner.drainFinalSubmissions(), nil
}

func (c *localInputContext) CommitSubmission(s agent.Submission) ([]model.Message, error) {
	return c.commit(s)
}

func (c *localInputContext) ContextEditSource() (hook.ContextSource, error) {
	return c.contextSource()
}
func (c *localInputContext) CommitContextEdit(logical, name string, edit hook.ContextEdit, validate func(context.Context) (func(), error)) ([]*session.Event, error) {
	return c.contextEdit(logical, name, edit, validate)
}
