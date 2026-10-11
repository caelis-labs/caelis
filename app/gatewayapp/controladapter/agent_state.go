package controladapter

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// The canonical Control snapshot works for idle profiles as well as live
// ordinary Runtimes. It keeps observations independent of execution assembly.
func (d *assembler) agentStateForSession(ctx context.Context, ref session.SessionRef) (kernel.ControlPlaneState, string, error) {
	if d.deps.Agent.SessionStateFn == nil {
		gw, err := d.gatewayControlPlane()
		if err != nil {
			return kernel.ControlPlaneState{}, "", err
		}
		state, err := gw.ControlPlaneState(ctx, kernel.ControlPlaneStateRequest{SessionRef: ref})
		return state, "", err
	}
	native, err := d.deps.Agent.SessionStateFn(ctx, ref)
	if err != nil {
		return kernel.ControlPlaneState{}, "", err
	}
	state := kernel.ControlPlaneState{SessionRef: ref, HasActiveTurn: native.Run.Active,
		Controller: kernel.ControllerState{Kind: native.Controller.Kind,
			ControllerID: native.Controller.ControllerID, AgentName: native.Controller.AgentName,
			Label: native.Controller.Label, EpochID: native.Controller.EpochID}}
	for _, item := range native.Participants {
		state.Participants = append(state.Participants, kernel.ParticipantState{ID: item.ID,
			Kind: item.Kind, Role: item.Role, AgentName: item.AgentName, Label: item.Label,
			Source: item.Source, SessionID: item.SessionID})
	}
	kind := ""
	if native.Run.Active {
		kind = string(native.Run.Kind)
	}
	return state, kind, nil
}
