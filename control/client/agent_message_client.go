package controlclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// AgentMessageRequest addresses one idempotent Agent-authored message. From is
// intentionally absent: the principal-aware service derives canonical Actor
// identity from trusted bindings. DisplayFrom is untrusted wire metadata only.
type AgentMessageRequest struct {
	SessionID   string `json:"session_id"`
	MessageID   string `json:"message_id"`
	To          string `json:"to,omitempty"`
	Text        string `json:"text"`
	DisplayFrom string `json:"display_from,omitempty"`
}

// AgentMessageResult reports durable delivery ownership and the optional Turn
// started for an idle target. Target is transport-neutral and never exposes a
// Runtime handle.
type AgentMessageResult struct {
	MessageID   string     `json:"message_id"`
	Accepted    bool       `json:"accepted,omitempty"`
	State       string     `json:"state,omitempty"`
	Target      TurnTarget `json:"target,omitempty"`
	StartedTurn bool       `json:"started_turn,omitempty"`
}

// AgentMessageService is the principal-aware AppServer capability for trusted
// Agent-message identity assignment, Runtime activation, and durable delivery.
type AgentMessageService interface {
	DeliverAgentMessage(context.Context, Principal, AgentMessageRequest) (AgentMessageResult, error)
}

// AgentMessageClient is the principal-bound transport-neutral delivery client.
type AgentMessageClient interface {
	DeliverAgentMessage(context.Context, AgentMessageRequest) (AgentMessageResult, error)
}

type boundAgentMessageClient struct {
	service   AgentMessageService
	principal Principal
}

// BindAgentMessageClient binds one trusted principal to Agent-message delivery.
func BindAgentMessageClient(service AgentMessageService, principal Principal) (AgentMessageClient, error) {
	if service == nil {
		return nil, errors.New("controlclient: Agent message service is required")
	}
	principal.ID = strings.TrimSpace(principal.ID)
	if principal.ID == "" {
		return nil, errors.New("controlclient: principal ID is required")
	}
	principal.Roles = append([]string(nil), principal.Roles...)
	return &boundAgentMessageClient{service: service, principal: principal}, nil
}

func (c *boundAgentMessageClient) DeliverAgentMessage(ctx context.Context, req AgentMessageRequest) (AgentMessageResult, error) {
	principal := c.principal
	principal.Roles = append([]string(nil), principal.Roles...)
	return c.service.DeliverAgentMessage(ctx, principal, req)
}

// AgentMessageDelivery couples durable acceptance to an optional target-
// filtered Turn observation. Closing Turn detaches only this observer.
type AgentMessageDelivery struct {
	Result AgentMessageResult
	Turn   TargetTurn
}

// AgentMessageTurnClient opens the authoritative Session feed before delivery,
// preserving fast output from an idle Session activated by an Agent message.
type AgentMessageTurnClient struct {
	sessions SessionClient
	messages AgentMessageClient
}

// NewAgentMessageTurnClient constructs the high-level delivery observer used by
// ACP and other clients that need to follow a message-authored Turn.
func NewAgentMessageTurnClient(sessions SessionClient, messages AgentMessageClient) (*AgentMessageTurnClient, error) {
	if sessions == nil || messages == nil {
		return nil, errors.New("controlclient: Session and Agent message clients are required")
	}
	return &AgentMessageTurnClient{sessions: sessions, messages: messages}, nil
}

// Deliver durably sends one message and returns target-filtered observation when
// delivery starts an idle Session Turn.
func (c *AgentMessageTurnClient) Deliver(ctx context.Context, request AgentMessageRequest) (AgentMessageDelivery, error) {
	if c == nil || c.sessions == nil || c.messages == nil {
		return AgentMessageDelivery{}, errors.New("controlclient: Agent message Turn client is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.MessageID = strings.TrimSpace(request.MessageID)
	request.To = strings.TrimSpace(request.To)
	request.Text = strings.TrimSpace(request.Text)
	request.DisplayFrom = strings.TrimSpace(request.DisplayFrom)
	if request.SessionID == "" || request.MessageID == "" || request.Text == "" {
		return AgentMessageDelivery{}, errors.New("controlclient: Agent message requires Session ID, message ID, and text")
	}

	feedCtx, stopFeed, reconnected, err := openTargetObservation(ctx, c.sessions, request.SessionID)
	if err != nil {
		return AgentMessageDelivery{}, err
	}
	closeObservation := func() {
		_ = reconnected.Subscription.Close()
		stopFeed()
	}
	result, err := c.messages.DeliverAgentMessage(ctx, request)
	if err != nil {
		closeObservation()
		return AgentMessageDelivery{}, err
	}
	if !result.StartedTurn {
		closeObservation()
		return AgentMessageDelivery{Result: result}, nil
	}
	if !validSessionTurnTarget(result.Target) {
		closeObservation()
		return AgentMessageDelivery{}, fmt.Errorf("controlclient: Agent message %q started a Turn without a complete target", result.MessageID)
	}

	turn := newTargetTurn(c.sessions, request.SessionID, reconnected, feedCtx, stopFeed, result.Target)
	turn.cancelFn = func(cancelCtx context.Context, reason string) error {
		_, cancelErr := c.sessions.Cancel(cancelCtx, CancelRequest{
			WriteBase: WriteBase{
				OperationID:             newSessionTurnOperationID("agent-message-cancel"),
				SessionID:               request.SessionID,
				ExpectedControllerEpoch: turn.controllerEpoch,
			},
			Target: result.Target,
			Reason: strings.TrimSpace(reason),
		})
		return cancelErr
	}
	go turn.relay()
	return AgentMessageDelivery{Result: result, Turn: turn}, nil
}

var _ AgentMessageClient = (*boundAgentMessageClient)(nil)
