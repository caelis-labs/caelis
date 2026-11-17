package chat

import (
	"fmt"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (a *Agent) completeCandidate(ctx agent.Context, candidate model.Message, rounds *int) (model.Message, *session.Event, error) {
	run := hookrun.FromContext(ctx)
	for _, h := range run.Hooks.Completion {
		d, err := hookrun.Call(ctx, "completion", h, run.Evidence, candidate, model.CloneMessage, func(d agent.CompletionDecision) agent.CompletionDecision {
			d.Message = hookrun.CloneMessagePointer(d.Message)
			d.Feedback = model.CloneParts(d.Feedback)
			return d
		})
		if err != nil {
			return model.Message{}, nil, err
		}
		bad := func(reason string) (model.Message, *session.Event, error) {
			return model.Message{}, nil, &agent.HookError{Phase: "completion", Name: h.Name, Cause: fmt.Errorf("%s", reason)}
		}
		switch d.Kind {
		case agent.CompletionAccept, agent.CompletionReject:
			if d.Message != nil || len(d.Feedback) > 0 {
				return bad("accept/reject cannot carry replacement or feedback")
			}
			if d.Kind == agent.CompletionReject {
				return bad("candidate rejected")
			}
		case agent.CompletionReplace:
			if d.Message == nil || len(d.Feedback) > 0 || d.Message.Role != model.RoleAssistant || len(d.Message.ToolCalls()) > 0 || len(d.Message.ToolResults()) > 0 {
				return bad("replacement requires one assistant final")
			}
			candidate = *d.Message
		case agent.CompletionContinue:
			if d.Message != nil || len(d.Feedback) == 0 {
				return bad("continue requires only feedback")
			}
			limit := run.Hooks.CompletionLimit
			if limit == 0 {
				limit = 2
			}
			if *rounds >= limit {
				return bad("completion_limit")
			}
			m := model.Message{Role: model.RoleUser, Parts: model.CloneParts(d.Feedback)}
			if err := session.ValidateContextMessages([]model.Message{m}); err != nil {
				return bad(err.Error())
			}
			(*rounds)++
			return candidate, &session.Event{Type: session.EventTypeContext, Visibility: session.VisibilityCanonical, Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: "hook/completion/" + h.Name}, Message: &m}, nil
		default:
			return bad("invalid completion kind")
		}
	}
	return candidate, nil, nil
}
