package local

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

const maxAgentMessageRevisionAttempts = 8

// AgentMessageService is the embedded AppServer owner of trusted source
// resolution and target Runtime activation for product ACP Agent messages.
type AgentMessageService struct {
	host    *gatewayapp.Stack
	deliver func(context.Context, gatewayapp.DeliverAgentMessageRequest) (gatewayapp.AgentMessageDelivery, error)
}

// NewAgentMessageService constructs the focused Agent-message capability.
func NewAgentMessageService(host *gatewayapp.Stack) (*AgentMessageService, error) {
	if host == nil || host.ControlClient() == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: Agent message host is required")
	}
	return &AgentMessageService{host: host, deliver: host.DeliverAgentMessage}, nil
}

// DeliverAgentMessage ignores all wire display identity when assigning the
// canonical Actor. The source must resolve from an exact participant/controller
// binding or from the durable parent Task relation of a managed child Session.
func (s *AgentMessageService) DeliverAgentMessage(ctx context.Context, principal appserver.Principal, req appserver.AgentMessageRequest) (appserver.AgentMessageResult, error) {
	if s == nil || s.host == nil {
		return appserver.AgentMessageResult{}, errors.New("app/gatewayapp/controladapter/local: Agent message service is unavailable")
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.MessageID = strings.TrimSpace(req.MessageID)
	req.To = strings.TrimSpace(req.To)
	req.Text = strings.TrimSpace(req.Text)
	req.DisplayFrom = strings.TrimSpace(req.DisplayFrom)
	if req.SessionID == "" || req.MessageID == "" || req.Text == "" {
		return appserver.AgentMessageResult{}, errors.New("app/gatewayapp/controladapter/local: Session ID, message ID, and text are required")
	}
	var lastErr error
	for range maxAgentMessageRevisionAttempts {
		result, err := s.deliverAgentMessageAttempt(ctx, principal, req)
		if !errors.Is(err, session.ErrRevisionConflict) {
			return result, err
		}
		lastErr = err
	}
	return appserver.AgentMessageResult{}, lastErr
}

// deliverAgentMessageAttempt resolves authority from one durable snapshot and
// fences the append with every revision used by that resolution. A revision
// conflict is safe to retry with the stable MessageID because it proves the
// append did not commit.
func (s *AgentMessageService) deliverAgentMessageAttempt(
	ctx context.Context,
	principal appserver.Principal,
	req appserver.AgentMessageRequest,
) (appserver.AgentMessageResult, error) {
	active, err := s.host.Sessions().Session(ctx, session.SessionRef{SessionID: req.SessionID})
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			return appserver.AgentMessageResult{}, appserver.ErrUnauthorized
		}
		return appserver.AgentMessageResult{}, err
	}
	if err := authorizeAgentMessagePrincipal(principal, active); err != nil {
		return appserver.AgentMessageResult{}, err
	}
	closed, err := appserver.IsSessionClosed(ctx, s.host.Sessions(), active.SessionRef)
	if err != nil {
		return appserver.AgentMessageResult{}, err
	}
	if closed {
		return appserver.AgentMessageResult{}, appserver.ErrSessionClosed
	}
	source, err := s.trustedAgentMessageSource(ctx, principal, active)
	if err != nil {
		return appserver.AgentMessageResult{}, err
	}
	expectedRevision := active.Revision
	deliver := s.deliver
	if deliver == nil {
		deliver = s.host.DeliverAgentMessage
	}
	delivery, err := deliver(ctx, gatewayapp.DeliverAgentMessageRequest{
		SessionRef:       active.SessionRef,
		ExpectedRevision: &expectedRevision,
		RelatedRevisions: source.relatedRevisions,
		Message: agentmessage.Request{
			MessageID: req.MessageID, To: req.To, Text: req.Text,
			From: source.actor, Scope: &source.scope, DisplayFrom: req.DisplayFrom,
		},
	})
	if err != nil {
		return appserver.AgentMessageResult{}, err
	}
	result := appserver.AgentMessageResult{
		MessageID: req.MessageID,
		Accepted:  delivery.Accepted,
		State:     delivery.State,
	}
	if delivery.Turn != nil {
		result.Target = appserver.TurnTarget{
			HandleID: delivery.Turn.HandleID(),
			RunID:    delivery.Turn.RunID(),
			TurnID:   delivery.Turn.TurnID(),
		}
		result.StartedTurn = true
	}
	return result, nil
}

type resolvedAgentMessageSource struct {
	actor            session.ActorRef
	scope            session.EventScope
	relatedRevisions []session.SessionRevisionPrecondition
}

func authorizeAgentMessagePrincipal(principal appserver.Principal, active session.Session) error {
	principalID := strings.TrimSpace(principal.ID)
	if principalID == "" {
		return appserver.ErrUnauthorized
	}
	if principal.HasRole("admin") || strings.TrimSpace(active.UserID) == principalID {
		return nil
	}
	for _, binding := range active.Participants {
		if participantBindingMatchesPrincipal(binding, principalID) {
			return nil
		}
	}
	if strings.TrimSpace(active.Controller.ControllerID) == principalID {
		return nil
	}
	return appserver.ErrUnauthorized
}

func (s *AgentMessageService) trustedAgentMessageSource(
	ctx context.Context,
	principal appserver.Principal,
	active session.Session,
) (resolvedAgentMessageSource, error) {
	principalID := strings.TrimSpace(principal.ID)
	if principalID == "" {
		return resolvedAgentMessageSource{}, errors.New("app/gatewayapp/controladapter/local: trusted Agent message principal is required")
	}
	for _, binding := range active.Participants {
		if !participantBindingMatchesPrincipal(binding, principalID) {
			continue
		}
		actor, scope := participantMessageSource(binding)
		return resolvedAgentMessageSource{actor: actor, scope: scope}, nil
	}
	controllerID := strings.TrimSpace(active.Controller.ControllerID)
	if controllerID != "" && principalID == controllerID {
		actor, scope := controllerMessageSource(active.Controller)
		return resolvedAgentMessageSource{actor: actor, scope: scope}, nil
	}

	if !principal.HasRole("admin") && strings.TrimSpace(active.UserID) != principalID {
		return resolvedAgentMessageSource{}, errors.New("app/gatewayapp/controladapter/local: trusted Agent message source is unavailable: target Session has no exact Agent binding")
	}
	actor, scope, parentRevision, err := s.managedChildParentSource(ctx, principal, active)
	if err == nil {
		return resolvedAgentMessageSource{
			actor: actor, scope: scope,
			relatedRevisions: []session.SessionRevisionPrecondition{{
				SessionRef: parentRevision.SessionRef, ExpectedRevision: parentRevision.Revision,
			}},
		}, nil
	}
	return resolvedAgentMessageSource{}, fmt.Errorf(
		"app/gatewayapp/controladapter/local: trusted Agent message source is unavailable: %w", err,
	)
}

func participantBindingMatchesPrincipal(binding session.ParticipantBinding, principalID string) bool {
	principalID = strings.TrimSpace(principalID)
	bindingID := strings.TrimSpace(binding.ID)
	return principalID != "" && bindingID != "" &&
		(principalID == bindingID || principalID == strings.TrimSpace(binding.SessionID))
}

func (s *AgentMessageService) managedChildParentSource(
	ctx context.Context,
	principal appserver.Principal,
	child session.Session,
) (session.ActorRef, session.EventScope, session.Session, error) {
	kind := agentMessageMetadataString(child.Metadata, sessionvisibility.MetadataSystemManagedAgent)
	if !strings.EqualFold(kind, sessionvisibility.SystemManagedAgentSubagent) {
		return session.ActorRef{}, session.EventScope{}, session.Session{}, errors.New("target Session has no exact Agent binding")
	}
	parentSessionID := agentMessageMetadataString(child.Metadata, sessionvisibility.MetadataSystemManagedParent)
	taskID := agentMessageMetadataString(child.Metadata, sessionvisibility.MetadataSystemManagedTask)
	childSessionID := strings.TrimSpace(child.SessionID)
	if parentSessionID == "" || taskID == "" || childSessionID == "" {
		return session.ActorRef{}, session.EventScope{}, session.Session{}, errors.New("managed child Session relation is incomplete")
	}
	parent, err := s.host.Sessions().Session(ctx, session.SessionRef{SessionID: parentSessionID})
	if err != nil {
		return session.ActorRef{}, session.EventScope{}, session.Session{}, fmt.Errorf("inspect managed child parent: %w", err)
	}
	if strings.TrimSpace(parent.UserID) != strings.TrimSpace(child.UserID) {
		return session.ActorRef{}, session.EventScope{}, session.Session{}, errors.New("managed child parent owner does not match target Session")
	}
	if !principal.HasRole("admin") && strings.TrimSpace(parent.UserID) != strings.TrimSpace(principal.ID) {
		return session.ActorRef{}, session.EventScope{}, session.Session{}, appserver.ErrUnauthorized
	}
	actor, scope, err := managedChildParentMessageSource(childSessionID, taskID, parent)
	return actor, scope, parent, err
}

func managedChildParentMessageSource(
	childSessionID string,
	taskID string,
	parent session.Session,
) (session.ActorRef, session.EventScope, error) {
	for _, binding := range parent.Participants {
		if binding.Kind != session.ParticipantKindSubagent ||
			strings.TrimSpace(binding.ID) == "" ||
			strings.TrimSpace(binding.SessionID) != strings.TrimSpace(childSessionID) ||
			strings.TrimSpace(binding.DelegationID) != strings.TrimSpace(taskID) {
			continue
		}
		if strings.TrimSpace(parent.Controller.ControllerID) == "" {
			return session.ActorRef{}, session.EventScope{}, errors.New("managed child parent controller binding is incomplete")
		}
		return session.ControllerExecutor(parent.Controller), session.EventScope{Source: "appserver_agent_message"}, nil
	}
	return session.ActorRef{}, session.EventScope{}, errors.New("managed child Task does not match its parent participant binding")
}

func participantMessageSource(binding session.ParticipantBinding) (session.ActorRef, session.EventScope) {
	return session.ParticipantExecutor(binding), session.EventScope{
		Source: "appserver_agent_message",
		Participant: session.ParticipantRef{
			ID: strings.TrimSpace(binding.ID), Kind: binding.Kind,
			Role: binding.Role, DelegationID: strings.TrimSpace(binding.DelegationID),
		},
	}
}

func controllerMessageSource(binding session.ControllerBinding) (session.ActorRef, session.EventScope) {
	return session.ControllerExecutor(binding), session.EventScope{
		Source: "appserver_agent_message",
		Controller: session.ControllerRef{
			Kind: binding.Kind, ID: strings.TrimSpace(binding.ControllerID), EpochID: strings.TrimSpace(binding.EpochID),
		},
	}
}

func agentMessageMetadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

var _ appserver.AgentMessageService = (*AgentMessageService)(nil)
