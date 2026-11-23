package runtime

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) prepareHookInputs(ctx context.Context, inputs []*session.Event) ([]*session.Event, error) {
	run := hookrun.FromContext(ctx)
	out := make([]*session.Event, 0, len(inputs))
	for _, input := range inputs {
		e := session.CloneEvent(input)
		if e == nil || e.Message == nil {
			return nil, fmt.Errorf("input hook requires canonical message")
		}
		// Prepared input is reused during overflow recovery and never transformed
		// again. The marker is typed Runtime-created evidence, not caller metadata.
		if e.InputEvidence != nil {
			out = append(out, e)
			continue
		}
		e.InputEvidence = &session.InputEvidence{Message: model.CloneMessage(*e.Message), Source: session.CloneActorRef(e.Actor)}
		var extra []*session.Event
		for _, h := range run.Hooks.Input {
			decision, err := hookrun.Call(ctx, "input", h, hook.InputContext{TurnContext: run.Evidence, InputSource: session.CloneActorRef(e.Actor)}, hook.InputView{Message: *e.Message}, func(v hook.InputView) hook.InputView { v.Message = model.CloneMessage(v.Message); return v }, hookrun.CloneInput)
			if err != nil {
				return nil, err
			}
			if decision.Message.Role != e.Message.Role || len(decision.Message.ToolCalls()) > 0 || len(decision.Message.ToolResults()) > 0 {
				return nil, &hook.Error{Phase: "input", Name: h.Name, Cause: fmt.Errorf("input role and protocol identity cannot change")}
			}
			if err := validateEarlyResponse(decision.Response); err != nil {
				return nil, &hook.Error{Phase: "input", Name: h.Name, Cause: err}
			}
			e.Message = &decision.Message
			e.InputEvidence.Hooks = append(e.InputEvidence.Hooks, h.Name)
			if len(decision.Context) > 0 {
				event, err := hookContextEvent(decision.Context, "input/"+h.Name)
				if err != nil {
					return nil, &hook.Error{Phase: "input", Name: h.Name, Cause: err}
				}
				extra = append(extra, event)
			}
			run.SetEarly(decision.Response, hook.OutputInputEarly)
		}
		out = append(out, e)
		out = append(out, extra...)
	}
	return out, nil
}

func (r *Runtime) runTurnStartHooks(ctx context.Context) ([]*session.Event, error) {
	run := hookrun.FromContext(ctx)
	var out []*session.Event
	for _, h := range run.Hooks.TurnStart {
		decision, err := hookrun.Transform(ctx, "turn_start", h.Name, h.Timeout, func(ctx context.Context) (hook.TurnStartDecision, error) { return h.Apply(ctx, run.Evidence) }, hookrun.CloneTurn)
		if err != nil {
			return nil, err
		}
		if err := validateEarlyResponse(decision.Response); err != nil {
			return nil, &hook.Error{Phase: "turn_start", Name: h.Name, Cause: err}
		}
		if len(decision.Context) > 0 {
			event, err := hookContextEvent(decision.Context, "turn_start/"+h.Name)
			if err != nil {
				return nil, &hook.Error{Phase: "turn_start", Name: h.Name, Cause: err}
			}
			out = append(out, event)
		}
		run.SetEarly(decision.Response, hook.OutputTurnEarly)
	}
	return out, nil
}

func validateEarlyResponse(m *model.Message) error {
	if m != nil && (m.Role != model.RoleAssistant || len(m.ToolCalls()) > 0 || len(m.ToolResults()) > 0) {
		return fmt.Errorf("early response must be an assistant final without tool calls")
	}
	return nil
}

func hookContextEvent(parts []model.Part, name string) (*session.Event, error) {
	m := model.Message{Role: model.RoleUser, Parts: model.CloneParts(parts)}
	if err := session.ValidateContextMessages([]model.Message{m}); err != nil {
		return nil, err
	}
	return &session.Event{Type: session.EventTypeContext, Visibility: session.VisibilityCanonical, Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: "hook/" + name}, Message: &m}, nil
}
