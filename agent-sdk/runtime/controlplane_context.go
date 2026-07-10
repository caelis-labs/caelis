package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) buildControllerTurnContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	excludeTurnID string,
) (string, int, error) {
	binding := session.CloneControllerBinding(activeSession.Controller)
	if binding.Kind != session.ControllerKindACP {
		return "", binding.ContextSyncSeq, nil
	}
	return r.buildControllerHandoffContext(ctx, activeSession, ref, binding, binding.ContextSyncSeq, excludeTurnID)
}

func (r *Runtime) buildControllerHandoffContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	from session.ControllerBinding,
	sinceSeq int,
	excludeTurnID string,
) (string, int, error) {
	if r == nil || r.controllerContextRouter == nil {
		return "", 0, fmt.Errorf("agent-sdk/runtime: controller context router is unavailable")
	}
	route, err := r.controllerContextRouter.ControllerContext(ctx, controller.ControllerContextRequest{
		SessionRef:    session.NormalizeSessionRef(ref),
		Session:       session.CloneSession(activeSession),
		Controller:    session.CloneControllerBinding(from),
		SinceSeq:      sinceSeq,
		ExcludeTurnID: strings.TrimSpace(excludeTurnID),
	})
	if err != nil {
		return "", 0, err
	}
	return strings.TrimSpace(route.Prelude), route.SyncSeq, nil
}

func (r *Runtime) buildParticipantPromptContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	binding session.ParticipantBinding,
) (string, int, error) {
	if r == nil || r.controllerContextRouter == nil {
		return "", 0, fmt.Errorf("agent-sdk/runtime: controller context router is unavailable")
	}
	route, err := r.controllerContextRouter.ParticipantContext(ctx, controller.ParticipantContextRequest{
		SessionRef: session.NormalizeSessionRef(ref),
		Session:    session.CloneSession(activeSession),
		Binding:    session.CloneParticipantBinding(binding),
	})
	if err != nil {
		return "", 0, err
	}
	return strings.TrimSpace(route.Prelude), route.SyncSeq, nil
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
		SessionRef:    ref,
		MutationGuard: session.RuntimeMutationGuard(ctx),
		Binding:       binding,
	})
	return err
}
