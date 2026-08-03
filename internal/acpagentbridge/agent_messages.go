package acpagentbridge

import (
	"context"
	"fmt"
	"strings"

	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp"
)

type acpMessageSender struct {
	sessionID string
	callbacks acp.MessageCallbacks
}

func (s acpMessageSender) SendMessage(ctx context.Context, raw agentmessage.Request) (agentmessage.Response, error) {
	if s.callbacks == nil {
		return agentmessage.Response{}, fmt.Errorf("internal/acpagentbridge: parent message callback is unavailable")
	}
	req := agentmessage.NormalizeRequest(raw)
	resp, err := s.callbacks.SessionMessage(ctx, acp.SessionMessageRequest{
		SessionID: strings.TrimSpace(s.sessionID), MessageID: req.MessageID,
		To: req.To, From: firstAgentMessageValue(req.From.Name, req.From.ID), Message: req.Text,
	})
	if err != nil {
		return agentmessage.Response{}, err
	}
	return agentmessage.Response{
		MessageID: resp.MessageID, Accepted: resp.Accepted, State: resp.State,
		TurnID: resp.TurnID, StartedTurn: resp.StartedTurn,
	}, nil
}

// SessionMessage implements the negotiated Caelis ACP v1 extension. An active
// turn accepts the message immediately; an idle target turn is streamed to
// completion before the request resolves so the spawning parent retains its
// normal completion barrier.
func (a *RuntimeAgent) SessionMessage(ctx context.Context, req acp.SessionMessageRequest, cb acp.PromptCallbacks) (acp.SessionMessageResponse, error) {
	if a == nil || a.agentMessages == nil {
		return acp.SessionMessageResponse{}, acp.ErrCapabilityUnsupported
	}
	if _, err := a.targetSession(ctx, req.SessionID); err != nil {
		return acp.SessionMessageResponse{}, err
	}
	messageID := strings.TrimSpace(req.MessageID)
	text := strings.TrimSpace(req.Message)
	if messageID == "" || text == "" {
		return acp.SessionMessageResponse{}, fmt.Errorf("internal/acpagentbridge: messageId and message are required")
	}
	from := strings.TrimSpace(req.From)
	if from == "" {
		from = agentmessage.Parent
	}
	messageReq := agentmessage.Request{
		MessageID: messageID, To: strings.TrimSpace(req.To), Text: text,
		From:  session.ActorRef{Kind: session.ActorKindParticipant, ID: from, Name: from},
		Scope: &session.EventScope{Source: "acp_agent_message"},
	}
	deliveryCtx := ctx
	if messageCallbacks, ok := cb.(acp.MessageCallbacks); ok {
		deliveryCtx = agentmessage.WithSender(deliveryCtx, acpMessageSender{
			sessionID: strings.TrimSpace(req.SessionID),
			callbacks: messageCallbacks,
		})
	}
	delivery, err := a.agentMessages(deliveryCtx, strings.TrimSpace(req.SessionID), messageReq)
	if err != nil {
		return acp.SessionMessageResponse{}, err
	}
	if delivery.Events == nil {
		return acp.SessionMessageResponse{
			MessageID: messageID, Accepted: delivery.Accepted, State: delivery.State,
			TurnID: delivery.TurnID, StartedTurn: delivery.StartedTurn,
		}, nil
	}
	if delivery.Close != nil {
		defer func() {
			_ = delivery.Close()
		}()
	}
	filter := newACPNarrativeFilter(true)
	for {
		select {
		case <-ctx.Done():
			if delivery.Cancel != nil {
				delivery.Cancel()
			}
			return acp.SessionMessageResponse{}, ctx.Err()
		case envelope, ok := <-delivery.Events:
			if !ok {
				return acp.SessionMessageResponse{
					MessageID: messageID, Accepted: delivery.Accepted, State: agentmessage.StateCompleted,
					TurnID: delivery.TurnID, StartedTurn: delivery.StartedTurn,
				}, nil
			}
			if cb != nil {
				if err := a.emitControlEnvelope(ctx, cb, req.SessionID, nil, envelope, filter); err != nil {
					return acp.SessionMessageResponse{}, err
				}
			}
		}
	}
}

var _ agentmessage.Sender = acpMessageSender{}

func firstAgentMessageValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
