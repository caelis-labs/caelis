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
	binding := session.CloneControllerBinding(activeSession.Controller)
	if binding.Kind != session.ControllerKindACP {
		return controller.ContextRoute{SyncSeq: binding.ContextSyncSeq}, nil
	}
	return r.buildControllerHandoffContext(ctx, activeSession, ref, binding, binding.ContextSyncSeq, excludeTurnID)
}

func (r *Runtime) buildControllerHandoffContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	from session.ControllerBinding,
	sinceSeq uint64,
	excludeTurnID string,
) (controller.ContextRoute, error) {
	if r == nil || r.controllerContextRouter == nil {
		return controller.ContextRoute{}, fmt.Errorf("agent-sdk/runtime: controller context router is unavailable")
	}
	route, err := r.controllerContextRouter.ControllerContext(ctx, controller.ControllerContextRequest{
		SessionRef:    session.NormalizeSessionRef(ref),
		Session:       session.CloneSession(activeSession),
		Controller:    session.CloneControllerBinding(from),
		SinceSeq:      sinceSeq,
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
	activeSession session.Session,
	ref session.SessionRef,
	binding session.ParticipantBinding,
) (agent.ContextTransfer, uint64, error) {
	if r == nil || r.controllerContextRouter == nil {
		return agent.ContextTransfer{}, 0, fmt.Errorf("agent-sdk/runtime: controller context router is unavailable")
	}
	route, err := r.controllerContextRouter.ParticipantContext(ctx, controller.ParticipantContextRequest{
		SessionRef: session.NormalizeSessionRef(ref),
		Session:    session.CloneSession(activeSession),
		Binding:    session.CloneParticipantBinding(binding),
	})
	if err != nil {
		return agent.ContextTransfer{}, 0, err
	}
	return agent.CloneContextTransfer(route.Context), route.SyncSeq, nil
}

func (r *Runtime) updateControllerContextCheckpoint(ctx context.Context, ref session.SessionRef) error {
	if r == nil || r.sessions == nil {
		return nil
	}
	if r.controllerContextRouter == nil {
		return fmt.Errorf("agent-sdk/runtime: controller context router is unavailable")
	}
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return err
	}
	binding := session.CloneControllerBinding(activeSession.Controller)
	if provider, ok := r.controllers.(controller.BindingProvider); ok {
		live, found, liveErr := provider.ActiveControllerBinding(ctx, ref)
		if liveErr != nil {
			return liveErr
		}
		if !found {
			return fmt.Errorf("agent-sdk/runtime: active controller binding is unavailable")
		}
		if strings.TrimSpace(live.EpochID) != strings.TrimSpace(binding.EpochID) {
			return fmt.Errorf(
				"agent-sdk/runtime: live controller epoch %q does not match durable epoch %q",
				strings.TrimSpace(live.EpochID),
				strings.TrimSpace(binding.EpochID),
			)
		}
		binding = session.CloneControllerBinding(live)
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
	_, err = r.sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef:           ref,
		ExpectedRevision:     &activeSession.Revision,
		MutationGuard:        session.RuntimeMutationGuard(ctx),
		ExpectedDelegationID: stringPointer(binding.DelegationID),
		Binding:              binding,
	})
	return err
}
