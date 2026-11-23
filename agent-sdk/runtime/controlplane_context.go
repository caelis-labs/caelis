package runtime

import (
	"context"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) buildControllerTurnContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	excludeTurnID string,
) (controller.ContextRoute, error) {
	binding := activeSession.Controller
	if binding.Kind != session.ControllerKindACP {
		return controller.ContextRoute{SyncSeq: binding.ContextSyncSeq}, nil
	}
	if r == nil || r.controllerContextRouter == nil {
		return controller.ContextRoute{}, fmt.Errorf("agent-sdk/runtime: controller context router is unavailable")
	}
	route, err := r.controllerContextRouter.ControllerContext(ctx, controller.ControllerContextRequest{
		SessionRef:    session.NormalizeSessionRef(ref),
		SinceSeq:      binding.ContextSyncSeq,
		ExcludeTurnID: strings.TrimSpace(excludeTurnID),
	})
	if err != nil {
		return controller.ContextRoute{}, err
	}
	return controller.ContextRoute{
		Context:      agent.CloneContextTransfer(route.Context),
		FreshContext: agent.CloneContextTransfer(route.FreshContext),
		SyncSeq:      route.SyncSeq,
	}, nil
}

func (r *Runtime) buildParticipantPromptContext(
	ctx context.Context,
	ref session.SessionRef,
	sinceSeq uint64,
) (agent.ContextTransfer, error) {
	if r == nil || r.controllerContextRouter == nil {
		return agent.ContextTransfer{}, fmt.Errorf("agent-sdk/runtime: controller context router is unavailable")
	}
	transfer, err := r.controllerContextRouter.ParticipantContext(ctx, controller.ParticipantContextRequest{
		SessionRef: session.NormalizeSessionRef(ref),
		SinceSeq:   sinceSeq,
	})
	if err != nil {
		return agent.ContextTransfer{}, err
	}
	return agent.CloneContextTransfer(transfer), nil
}

func (r *Runtime) updateControllerContextCheckpoint(ctx context.Context, ref session.SessionRef) error {
	if r == nil || r.sessions == nil {
		return nil
	}
	if r.controllerContextRouter == nil {
		return fmt.Errorf("agent-sdk/runtime: controller context router is unavailable")
	}
	_, binding, err := r.controllerBindings(ctx, ref)
	if err != nil {
		return err
	}
	binding.ContextSyncSeq, err = r.controllerContextRouter.Checkpoint(ctx, ref, "")
	if err != nil {
		return err
	}
	_, err = r.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef:    ref,
		MutationGuard: session.RuntimeMutationGuard(ctx),
		Binding:       binding,
	})
	return err
}

func (r *Runtime) updateParticipantContextCheckpoint(ctx context.Context, ref session.SessionRef, participantID string) error {
	if r == nil || r.sessions == nil {
		return nil
	}
	if r.controllerContextRouter == nil {
		return fmt.Errorf("agent-sdk/runtime: controller context router is unavailable")
	}
	participantID = strings.TrimSpace(participantID)
	if participantID == "" {
		return nil
	}
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return err
	}
	binding, ok := participantBinding(activeSession, participantID)
	if !ok {
		return nil
	}
	binding.ContextSyncSeq, err = r.controllerContextRouter.Checkpoint(ctx, ref, "")
	if err != nil {
		return err
	}
	_, err = r.participantBindings.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef:           ref,
		ExpectedRevision:     &activeSession.Revision,
		MutationGuard:        session.RuntimeMutationGuard(ctx),
		ExpectedDelegationID: stringPointer(binding.DelegationID),
		Binding:              binding,
	})
	return err
}
