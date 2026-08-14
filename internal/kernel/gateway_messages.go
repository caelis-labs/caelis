package kernel

import (
	"context"
	"fmt"

	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// DeliverAgentMessageRequest separates admission lifetime from the Runtime
// lifetime of a Turn started for an idle target Session.
type DeliverAgentMessageRequest struct {
	SessionRef       session.SessionRef
	RuntimeContext   context.Context
	Message          agentmessage.Request
	ExpectedRevision *uint64
	// RelatedRevisions fence cross-Session facts used to assign Message.From.
	RelatedRevisions []session.SessionRevisionPrecondition
}

// DeliverAgentMessage commits a participant-authored Context event before
// waking a live turn. When the Session is idle it starts one new turn whose
// invocation reads the already durable message from canonical history.
func (g *Gateway) DeliverAgentMessage(ctx context.Context, request DeliverAgentMessageRequest) (AgentMessageDelivery, error) {
	if g == nil || g.sessions == nil {
		return AgentMessageDelivery{}, fmt.Errorf("gateway: Agent message service is unavailable")
	}
	ref := session.NormalizeSessionRef(request.SessionRef)
	req := agentmessage.NormalizeRequest(request.Message)
	if req.MessageID == "" || req.Text == "" || !session.ActorRefHasIdentity(req.From) {
		return AgentMessageDelivery{}, fmt.Errorf("gateway: Agent message id, text, and source are required")
	}
	activeSession, err := g.sessions.Session(ctx, ref)
	if err != nil {
		return AgentMessageDelivery{}, wrapSessionError(err)
	}
	g.mu.Lock()
	handle := g.active[activeSession.SessionID]
	g.mu.Unlock()
	turnID := ""
	if handle != nil {
		turnID = handle.TurnID()
	}
	appendResult, err := agentmessage.AppendContextConditional(
		ctx, g.sessions, activeSession.SessionRef,
		session.ControlMutationGuard(session.ControlMutationPurposeAgentMessage),
		session.EventScope{
			TurnID: turnID, Source: "agent_message", Controller: controllerRefForMessage(activeSession.Controller),
			Executor: session.ControllerExecutor(activeSession.Controller),
		},
		req, request.ExpectedRevision, request.RelatedRevisions,
	)
	if err != nil {
		return AgentMessageDelivery{}, err
	}
	persisted := appendResult.Event
	if !appendResult.Appended {
		return AgentMessageDelivery{Accepted: true, State: agentmessage.StatePending}, nil
	}
	if handle != nil {
		err := handle.Submit(ctx, SubmitRequest{
			Kind: SubmissionKindAgentMessage, Text: req.Text, MessageID: req.MessageID,
			Actor: req.From, Scope: persisted.Scope, Metadata: persisted.Meta, Persisted: true,
		})
		if err != nil {
			//nolint:nilerr // The message is durably accepted; wake failure is pending so callers do not duplicate it.
			return AgentMessageDelivery{Accepted: true, State: agentmessage.StatePending}, nil
		}
		return AgentMessageDelivery{Accepted: true, State: agentmessage.StateDelivered}, nil
	}
	started, err := g.BeginTurn(ctx, BeginTurnRequest{
		SessionRef: activeSession.SessionRef, RuntimeContext: request.RuntimeContext, Surface: "agent_message",
	})
	if err != nil {
		var gatewayErr *Error
		if !As(err, &gatewayErr) || gatewayErr.Code != CodeActiveRunConflict {
			// Durable acceptance is already complete. Wakeup failure leaves the
			// message pending for the next Turn and must not invite a duplicate
			// delivery retry with a fresh message id.
			return AgentMessageDelivery{Accepted: true, State: agentmessage.StatePending}, nil
		}
		// A Turn may have won admission after the initial active lookup. The
		// message is already durable, so wake that Turn without persisting it a
		// second time.
		if submitErr := g.SubmitActiveTurn(ctx, SubmitActiveTurnRequest{
			SessionRef: activeSession.SessionRef, Kind: SubmissionKindAgentMessage,
			Text: req.Text, MessageID: req.MessageID, Actor: req.From,
			Scope: persisted.Scope, Metadata: persisted.Meta, Persisted: true,
		}); submitErr != nil {
			//nolint:nilerr // The message is durably accepted; wake failure is pending so callers do not duplicate it.
			return AgentMessageDelivery{Accepted: true, State: agentmessage.StatePending}, nil
		}
		return AgentMessageDelivery{Accepted: true, State: agentmessage.StateDelivered}, nil
	}
	return AgentMessageDelivery{Accepted: true, State: agentmessage.StateRunning, Turn: started.Handle}, nil
}

func controllerRefForMessage(binding session.ControllerBinding) session.ControllerRef {
	binding = session.CloneControllerBinding(binding)
	return session.ControllerRef{Kind: binding.Kind, ID: binding.ControllerID, EpochID: binding.EpochID}
}
