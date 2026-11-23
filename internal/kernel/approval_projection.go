package kernel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

const approvalProjectionTimeout = 5 * time.Second

func (g *Gateway) approvalProjector(ref session.SessionRef, turnID string) func(*approval.Request, eventstream.ApprovalRequestID) (*session.Event, error) {
	return func(req *approval.Request, requestID eventstream.ApprovalRequestID) (*session.Event, error) {
		return g.projectApprovalRequest(req, ref, turnID, requestID)
	}
}

func (g *Gateway) approvalSettlementProjector(ref session.SessionRef, turnID string) func(*approval.Request, eventstream.ApprovalRequestID, string) (*session.Event, error) {
	return func(req *approval.Request, requestID eventstream.ApprovalRequestID, state string) (*session.Event, error) {
		return g.projectApprovalSettlement(req, ref, turnID, requestID, state)
	}
}

func (g *Gateway) projectApprovalRequest(req *approval.Request, fallbackRef session.SessionRef, fallbackTurnID string, requestID eventstream.ApprovalRequestID) (*session.Event, error) {
	if g == nil || g.sessions == nil || req == nil {
		return nil, fmt.Errorf("gateway: approval persistence is unavailable")
	}
	payload := approval.DisplayPayloadFromRuntimeRequest(*req)
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
	ctx, cancel := context.WithTimeout(context.Background(), approvalProjectionTimeout)
	defer cancel()
	events, err := g.sessions.Events(ctx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		return nil, err
	}
	var source *session.Event
	for _, fact := range events {
		if fact.Approval != nil && fact.Approval.RequestID == string(requestID) && (source == nil || source.Approval.Revision < fact.Approval.Revision) {
			source = fact
		}
	}
	if source == nil || source.Approval.Status != session.ApprovalRecordPending {
		return nil, fmt.Errorf("approval request %q has no pending Runtime fact", requestID)
	}
	if source.Approval.Presentation != nil {
		shown := session.CloneProtocolApproval(*source.Approval.Presentation)
		event.Protocol.Permission = &shown
	}
	event.ID = source.ID
	event.Seq = source.Seq
	event.Time = source.Time
	return event, nil

}

func approvalChildOrigin(req *approval.Request, origin *EventOrigin, requestID eventstream.ApprovalRequestID) *session.EventChildOrigin {
	if req == nil || origin == nil {
		return nil
	}
	scopeID := strings.TrimSpace(origin.ScopeID)
	switch origin.Scope {
	case EventScopeSubagent:
		parent := approvalParentToolRelation(req)
		if scopeID == "" {
			return nil
		}
		var parentTool session.EventParentTool
		if parent != nil && strings.TrimSpace(parent.ToolCallID) != "" {
			parentTool = session.EventParentTool{CallID: strings.TrimSpace(parent.ToolCallID), Name: strings.TrimSpace(parent.ToolName)}
		}
		taskID := firstNonEmpty(metadataString(req.Metadata, "task_id"), metadataString(req.Metadata, "parent_task_id"), scopeID)
		return &session.EventChildOrigin{
			Scope: session.EventChildScopeSubagent, ScopeID: scopeID, TaskID: taskID,
			DelegationID:  firstNonEmpty(metadataString(req.Metadata, "delegation_id"), taskID),
			ParticipantID: strings.TrimSpace(origin.ParticipantID), ACPSessionID: strings.TrimSpace(origin.ParticipantSessionID),
			SourceEventID: strings.TrimSpace(string(requestID)),
			ParentTool:    parentTool,
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

func (g *Gateway) projectApprovalSettlement(req *approval.Request, fallbackRef session.SessionRef, fallbackTurnID string, requestID eventstream.ApprovalRequestID, state string) (*session.Event, error) {
	if g == nil || g.sessions == nil {
		return nil, fmt.Errorf("gateway: approval settlement projection is unavailable")
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
	event.Visibility = session.VisibilityUIOnly
	return event, nil

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
