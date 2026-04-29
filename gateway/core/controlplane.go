package core

import (
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func buildControlPlaneState(session sdksession.Session, runState sdkruntime.RunState, events []*sdksession.Event) ControlPlaneState {
	state := ControlPlaneState{
		SessionRef: session.SessionRef,
		Controller: ControllerState{
			Kind:            session.Controller.Kind,
			ControllerID:    session.Controller.ControllerID,
			AgentName:       session.Controller.AgentName,
			Label:           session.Controller.Label,
			EpochID:         session.Controller.EpochID,
			RemoteSessionID: session.Controller.RemoteSessionID,
			ContextSyncSeq:  session.Controller.ContextSyncSeq,
			AttachedAt:      session.Controller.AttachedAt,
			Source:          session.Controller.Source,
		},
		RunState: runState,
	}
	state.Continuity = buildContinuityState(session, events)
	if runState.ActiveRunID != "" || runState.WaitingApproval || runState.Status == sdkruntime.RunLifecycleStatusRunning {
		state.HasActiveTurn = true
	}
	if len(session.Participants) == 0 {
		return state
	}
	state.Participants = make([]ParticipantState, 0, len(session.Participants))
	for _, item := range session.Participants {
		state.Participants = append(state.Participants, ParticipantState{
			ID:            item.ID,
			Kind:          item.Kind,
			Role:          item.Role,
			AgentName:     item.AgentName,
			Label:         item.Label,
			SessionID:     item.SessionID,
			Source:        item.Source,
			ParentTurnID:  item.ParentTurnID,
			DelegationID:  item.DelegationID,
			AttachedAt:    item.AttachedAt,
			ControllerRef: item.ControllerRef,
		})
	}
	return state
}
