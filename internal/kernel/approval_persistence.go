package kernel

import (
	"context"
	"fmt"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

const approvalPersistenceTimeout = 5 * time.Second

func (g *Gateway) approvalPersister(ref session.SessionRef, turnID string) func(*agent.ApprovalRequest, eventstream.ApprovalRequestID) (*session.Event, error) {
	return func(req *agent.ApprovalRequest, requestID eventstream.ApprovalRequestID) (*session.Event, error) {
		return g.persistApprovalRequest(req, ref, turnID, requestID)
	}
}

func (g *Gateway) approvalSettler(ref session.SessionRef, turnID string) func(*agent.ApprovalRequest, eventstream.ApprovalRequestID, string) (*session.Event, error) {
	return func(req *agent.ApprovalRequest, requestID eventstream.ApprovalRequestID, state string) (*session.Event, error) {
		return g.persistApprovalSettlement(req, ref, turnID, requestID, state)
	}
}

func (g *Gateway) persistApprovalRequest(req *agent.ApprovalRequest, fallbackRef session.SessionRef, fallbackTurnID string, requestID eventstream.ApprovalRequestID) (*session.Event, error) {
	if g == nil || g.sessions == nil || req == nil {
		return nil, fmt.Errorf("gateway: approval persistence is unavailable")
	}
	payload := approval.PayloadFromRuntimeRequest(*req)
	permission := approval.ProtocolApprovalFromPayload(payload)
	if permission == nil {
		return nil, fmt.Errorf("gateway: approval request %q has no normalized permission payload", requestID)
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	if strings.TrimSpace(ref.SessionID) == "" {
		ref = session.NormalizeSessionRef(fallbackRef)
	}
	origin := canonicalOriginFromApproval(req, ref, fallbackTurnID)
	event := &session.Event{
		IdempotencyKey:    "approval-request:" + ref.SessionID + ":" + string(requestID),
		Type:              session.EventTypeCustom,
		Visibility:        session.VisibilityMirror,
		ApprovalRequestID: strings.TrimSpace(string(requestID)),
		Actor:             session.ActorRef{Kind: session.ActorKindSystem, Name: "control"},
		Scope: &session.EventScope{
			TurnID: firstNonEmpty(strings.TrimSpace(req.TurnID), strings.TrimSpace(fallbackTurnID)),
			Source: "approval",
		},
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodRequestPermission, Permission: permission,
		},
	}
	if origin != nil {
		event.Actor.Name = firstNonEmpty(strings.TrimSpace(origin.Actor), event.Actor.Name)
		event.Scope.Source = firstNonEmpty(strings.TrimSpace(origin.Source), event.Scope.Source)
		event.Scope.Participant = session.ParticipantRef{
			ID: strings.TrimSpace(origin.ParticipantID), Kind: session.ParticipantKind(strings.TrimSpace(origin.ParticipantKind)),
		}
		event.Scope.ACP.SessionID = strings.TrimSpace(origin.ParticipantSessionID)
		event.ChildOrigin = approvalChildOrigin(req, origin, requestID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), approvalPersistenceTimeout)
	defer cancel()
	return g.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeApproval), Event: event,
	})
}

func approvalChildOrigin(req *agent.ApprovalRequest, origin *EventOrigin, requestID eventstream.ApprovalRequestID) *session.EventChildOrigin {
	if req == nil || origin == nil {
		return nil
	}
	scopeID := strings.TrimSpace(origin.ScopeID)
	switch origin.Scope {
	case EventScopeSubagent:
		parent := approvalParentToolRelation(req)
		if scopeID == "" || parent == nil || strings.TrimSpace(parent.ToolCallID) == "" {
			return nil
		}
		taskID := firstNonEmpty(metadataString(req.Metadata, "task_id"), metadataString(req.Metadata, "parent_task_id"), scopeID)
		return &session.EventChildOrigin{
			Scope: session.EventChildScopeSubagent, ScopeID: scopeID, TaskID: taskID,
			DelegationID:  firstNonEmpty(metadataString(req.Metadata, "delegation_id"), taskID),
			ParticipantID: strings.TrimSpace(origin.ParticipantID), ACPSessionID: strings.TrimSpace(origin.ParticipantSessionID),
			SourceEventID: strings.TrimSpace(string(requestID)),
			ParentTool:    session.EventParentTool{CallID: strings.TrimSpace(parent.ToolCallID), Name: strings.TrimSpace(parent.ToolName)},
		}
	case EventScopeParticipant:
		if scopeID == "" {
			return nil
		}
		return &session.EventChildOrigin{
			Scope: session.EventChildScopeParticipant, ScopeID: scopeID,
			ParticipantID: strings.TrimSpace(origin.ParticipantID), ACPSessionID: strings.TrimSpace(origin.ParticipantSessionID),
			SourceEventID: strings.TrimSpace(string(requestID)),
		}
	default:
		return nil
	}
}

func (g *Gateway) persistApprovalSettlement(req *agent.ApprovalRequest, fallbackRef session.SessionRef, fallbackTurnID string, requestID eventstream.ApprovalRequestID, state string) (*session.Event, error) {
	if g == nil || g.sessions == nil {
		return nil, fmt.Errorf("gateway: approval settlement persistence is unavailable")
	}
	ref := session.NormalizeSessionRef(fallbackRef)
	if req != nil && strings.TrimSpace(req.SessionRef.SessionID) != "" {
		ref = session.NormalizeSessionRef(req.SessionRef)
	}
	state = strings.TrimSpace(state)
	event := &session.Event{
		IdempotencyKey:    "approval-settlement:" + ref.SessionID + ":" + string(requestID) + ":" + state,
		Type:              session.EventTypeLifecycle,
		Visibility:        session.VisibilityMirror,
		ApprovalRequestID: strings.TrimSpace(string(requestID)),
		Actor:             session.ActorRef{Kind: session.ActorKindSystem, Name: "control"},
		Scope:             &session.EventScope{TurnID: strings.TrimSpace(fallbackTurnID), Source: "approval"},
		Lifecycle:         &session.EventLifecycle{Status: approvalSettlementStatus(state), Reason: state},
	}
	if req != nil {
		origin := canonicalOriginFromApproval(req, ref, fallbackTurnID)
		if origin != nil {
			event.Scope.TurnID = firstNonEmpty(strings.TrimSpace(req.TurnID), event.Scope.TurnID)
			event.Scope.Participant = session.ParticipantRef{ID: strings.TrimSpace(origin.ParticipantID), Kind: session.ParticipantKind(strings.TrimSpace(origin.ParticipantKind))}
			event.Scope.ACP.SessionID = strings.TrimSpace(origin.ParticipantSessionID)
			event.ChildOrigin = approvalChildOrigin(req, origin, eventstream.ApprovalRequestID(string(requestID)+":settlement:"+state))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), approvalPersistenceTimeout)
	defer cancel()
	return g.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeApproval), Event: event,
	})
}

func approvalSettlementStatus(state string) string {
	switch strings.TrimSpace(state) {
	case "resolved":
		return eventstream.LifecycleStateCompleted
	case "cancelled", "closed":
		return eventstream.LifecycleStateCancelled
	default:
		return eventstream.LifecycleStateInterrupted
	}
}
