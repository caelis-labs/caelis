package runtime

import (
	"context"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// SubmitAgentInputBatch validates every source and the common recipient under
// the participant mutation lock, then transfers the entire ordered input in one
// admission. No member is dispatched if any attachment changed.
func (r *Runtime) SubmitAgentInputBatch(
	ctx context.Context,
	ref session.SessionRef,
	target string,
	entries []agent.AgentInputBatchEntry,
	submitParent func(context.Context, session.Session, []agent.AgentCommunicationInput) error,
) error {
	if r == nil || r.sessions == nil {
		return errorcode.New(errorcode.FailedPrecondition, "Agent messaging is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	target = strings.TrimSpace(target)
	if len(entries) == 0 || target == "" {
		return errorcode.New(errorcode.InvalidArgument, "Message target and content are required")
	}
	r.participantMu.Lock()
	defer r.participantMu.Unlock()
	ref = session.NormalizeSessionRef(ref)
	active, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return err
	}
	parent := strings.EqualFold(target, agent.AgentInputParent)
	var recipient session.ParticipantBinding
	if !parent {
		if r.tasks == nil {
			return errorcode.New(errorcode.FailedPrecondition, "Target Agent messaging is unavailable")
		}
		recipient, err = resolveChildInputBinding(active, target)
		if err != nil {
			return err
		}
	}
	messages := make([]agent.AgentCommunicationInput, len(entries))
	for i, entry := range entries {
		var source session.ActorRef
		var binding *session.ParticipantBinding
		if entry.Participant != nil {
			source, binding, err = resolveExactParticipantInputSource(active, *entry.Participant)
		} else if entry.Message.Source.Kind == session.ActorKindController && !parent {
			source, binding, err = resolveTrustedChildInputSource(active, entry.Message.Source)
		} else {
			return errorcode.New(errorcode.PermissionDenied, "Source Agent binding is required")
		}
		if err != nil {
			return err
		}
		if binding != nil && binding.ID == recipient.ID {
			return errorcode.New(errorcode.Conflict, "An Agent cannot send a message to itself")
		}
		messages[i] = agent.CloneAgentCommunicationInputs([]agent.AgentCommunicationInput{entry.Message})[0]
		messages[i].Source = source
		if strings.TrimSpace(messages[i].Input) == "" && len(messages[i].ContentParts) == 0 {
			return errorcode.New(errorcode.InvalidArgument, "Message content is required")
		}
	}
	if parent {
		if submitParent == nil {
			return errorcode.New(errorcode.FailedPrecondition, "Main Agent messaging is unavailable")
		}
		return submitParent(ctx, active, messages)
	}
	_, err = r.submitChildInputLocked(ctx, ref, active, agent.ChildInputCommand{Target: target}, session.ActorRef{}, nil, messages)
	return err
}
