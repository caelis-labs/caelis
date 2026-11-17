package chat

import (
	"fmt"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/google/uuid"
)

func (a *Agent) completeCandidate(ctx agent.Context, candidate model.Message) (model.Message, *session.Event, error) {
	run := hookrun.FromContext(ctx)
	evidence := run.Evidence
	if a.outputStream != nil {
		evidence = a.outputStream.evidence
	}
	for _, h := range run.Hooks.Completion {
		d, err := hookrun.Call(ctx, "completion", h, evidence, candidate, model.CloneMessage, func(d agent.CompletionDecision) agent.CompletionDecision {
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
			if !run.FeedbackAvailable() {
				return bad("completion_limit")
			}

			m := model.Message{Role: model.RoleUser, Parts: model.CloneParts(d.Feedback)}
			if err := session.ValidateContextMessages([]model.Message{m}); err != nil {
				return bad(err.Error())
			}
			id := "hook-feedback:" + uuid.NewString()
			return candidate, &session.Event{ID: id, IdempotencyKey: id, Type: session.EventTypeContext, Visibility: session.VisibilityCanonical, Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: "hook/completion/" + h.Name}, Message: &m}, nil
		default:
			return bad("invalid completion kind")
		}
	}
	return candidate, nil, nil
}
