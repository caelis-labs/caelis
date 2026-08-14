package acpagentbridge

import (
	"context"
	"fmt"
	"strings"

	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
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
	if a == nil || (a.agentMessageTurns == nil && (a.agentMessages == nil || a.agentMessageSource == nil)) {
		return acp.SessionMessageResponse{}, acp.ErrCapabilityUnsupported
	}
	activeSession, err := a.targetSession(ctx, req.SessionID)
	if err != nil {
		return acp.SessionMessageResponse{}, err
	}
	messageID := strings.TrimSpace(req.MessageID)
	text := strings.TrimSpace(req.Message)
	if messageID == "" || text == "" {
		return acp.SessionMessageResponse{}, fmt.Errorf("internal/acpagentbridge: messageId and message are required")
	}
	deliveryCtx := ctx
	if messageCallbacks, ok := cb.(acp.MessageCallbacks); ok {
		deliveryCtx = agentmessage.WithSender(deliveryCtx, acpMessageSender{
			sessionID: strings.TrimSpace(req.SessionID),
			callbacks: messageCallbacks,
		})
	}

	var delivery AgentMessageDelivery
	if a.agentMessageTurns != nil {
		observed, deliverErr := a.agentMessageTurns.Deliver(deliveryCtx, controlclient.AgentMessageRequest{
			SessionID:   strings.TrimSpace(req.SessionID),
			MessageID:   messageID,
			To:          strings.TrimSpace(req.To),
			Text:        text,
			DisplayFrom: strings.TrimSpace(req.From),
		})
		if deliverErr != nil {
			return acp.SessionMessageResponse{}, deliverErr
		}
		delivery = AgentMessageDelivery{
			Accepted:    observed.Result.Accepted,
			State:       observed.Result.State,
			TurnID:      observed.Result.Target.TurnID,
			StartedTurn: observed.Result.StartedTurn,
		}
		if observed.Turn != nil {
			delivery.Events = observed.Turn.Events()
			delivery.Err = observed.Turn.Err
			delivery.Close = observed.Turn.Close
		}
	} else {
		actor, scope, sourceErr := a.agentMessageSource(deliveryCtx, activeSession)
		if sourceErr != nil {
			return acp.SessionMessageResponse{}, sourceErr
		}
		if !session.ActorRefHasIdentity(actor) {
			return acp.SessionMessageResponse{}, fmt.Errorf("internal/acpagentbridge: trusted Agent message source is unavailable")
		}
		delivery, err = a.agentMessages(deliveryCtx, strings.TrimSpace(req.SessionID), agentmessage.Request{
			MessageID:   messageID,
			To:          strings.TrimSpace(req.To),
			Text:        text,
			From:        actor,
			Scope:       scope,
			DisplayFrom: strings.TrimSpace(req.From),
		})
		if err != nil {
			return acp.SessionMessageResponse{}, err
		}
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
			return acp.SessionMessageResponse{}, ctx.Err()
		case envelope, ok := <-delivery.Events:
			if !ok {
				if delivery.Err != nil {
					if err := delivery.Err(); err != nil {
						return acp.SessionMessageResponse{}, fmt.Errorf(
							"internal/acpagentbridge: observe Agent message target Turn: %w", err,
						)
					}
				}
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
