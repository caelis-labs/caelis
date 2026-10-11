package gatewayapp

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/google/uuid"
)

func (s *runtimeComposition) recordControlInteraction(ctx context.Context, principal appserver.Principal, request appserver.RecordInteractionRequest) (appserver.CommandResult, error) {
	active, err := s.checkControlCommandCAS(ctx, request.WriteBase)
	if err != nil {
		return sessionCommandResult(active), classifyControlPreDispatchError(err)
	}
	intent, ok := appserver.OperationIntentFromContext(ctx)
	if !ok || intent.Action != appserver.ActionConversationInteraction {
		return sessionCommandResult(active), classifyControlPreDispatchError(errors.New("gatewayapp: interaction operation intent is required"))
	}
	guard := session.ControlMutationGuard(session.ControlMutationPurposeInteraction)
	if request.RequireIdleRun {
		gateway := s.currentGateway()
		if gateway != nil {
			unblock, err := gateway.BlockSessionTurnAdmission(active.SessionRef)
			if err != nil {
				return sessionCommandResult(active), classifyControlPreDispatchError(err)
			}
			defer unblock()
			if _, running := gateway.ActiveTurn(active.SessionID); running {
				return sessionCommandResult(active), appserver.NewOutcomeError(appserver.OutcomeConflicted,
					errorcode.New(errorcode.Conflict, "gatewayapp: conversation input requires an idle Session"))
			}
		}
		// The Store checks the original producer's durable execution fence while
		// holding its append transaction lock, including another host's producer.
		guard = session.ControlMutationGuard(session.ControlMutationPurposeIdleInteraction)
	}
	messageID := uuid.NewString()
	event, err := s.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &active.Revision,
		MutationGuard: guard,
		Event: &session.Event{
			ID: messageID, MessageID: messageID,
			IdempotencyKey: "interaction:" + intent.PrincipalID + ":" + intent.OperationID,
			Type:           session.EventTypeInteraction, Visibility: session.VisibilityMirror,
			Actor: session.ActorRef{Kind: session.ActorKindUser, ID: strings.TrimSpace(principal.ID)},
			Scope: &session.EventScope{Source: "user_command"},
			Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeUserMessage), MessageID: messageID,
				Content: session.ProtocolTextContent(request.Text),
			}},
			Meta: map[string]any{"interaction_kind": "command", "interaction_operation_id": intent.OperationID, "interaction_intent_digest": intent.Digest},
		},
	})
	if err != nil && !session.IsCommitted(err) {
		return sessionCommandResult(active), classifyControlBackendError(err)
	}
	if event == nil || event.ID == "" {
		return sessionCommandResult(active), classifyControlBackendError(errors.New("gatewayapp: interaction append returned no durable identity"))
	}
	result := sessionCommandResult(active)
	result.Resource = &appserver.CommandResource{Kind: appserver.CommandResourceConversationMessage, Ref: event.ID}
	if updated, readErr := s.sessions.Session(ctx, active.SessionRef); readErr == nil {
		result.Revision = updated.Revision
	} else {
		result.Revision = 0
	}
	// The actual durable identity survives a post-commit observation failure.
	// Feed replay remains authoritative; no command effect has been dispatched.
	if feeds := s.authorities.controlFeeds; feeds != nil {
		if feed, feedErr := feeds.Session(active.SessionRef); feedErr == nil {
			publishCtx, cancel := operationObservationContext(ctx)
			if primeErr := feed.Prime(publishCtx); primeErr != nil {
				result.Detail = controlFeedCatchUpWarning
			}
			cancel()
		}
	}
	return result, nil
}

func operationObservationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), controlFeedPublishTimeout)
}

func (s *controlCommandBackend) recoverControlInteraction(ctx context.Context, intent appserver.OperationIntent) (appserver.CommandResult, bool, error) {
	ref := session.SessionRef{SessionID: intent.SessionID}
	// Command inputs live in the durable client mirror, outside canonical
	// model history. The owner must read that lane to recover an inserted ID.
	events, err := s.composition.sessions.Events(ctx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		return appserver.CommandResult{}, false, err
	}
	for _, event := range events {
		if event == nil || event.Type != session.EventTypeInteraction || event.IdempotencyKey != "interaction:"+intent.PrincipalID+":"+intent.OperationID || event.Meta["interaction_intent_digest"] != intent.Digest {
			continue
		}
		active, err := s.composition.sessions.Session(ctx, ref)
		if err != nil {
			return appserver.CommandResult{}, false, err
		}
		result := sessionCommandResult(active)
		result.Resource = &appserver.CommandResource{Kind: appserver.CommandResourceConversationMessage, Ref: event.ID}
		return result, true, nil
	}
	return appserver.CommandResult{}, false, nil
}
