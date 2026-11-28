package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) ensureSessionController(
	ctx context.Context,
	activeSession session.Session,
	mutationGuard session.MutationGuard,
) (session.Session, error) {
	if r == nil || r.sessions == nil {
		return session.Session{}, fmt.Errorf("agent-sdk/runtime: session service is unavailable")
	}
	if activeSession.Controller.Kind != "" {
		return session.CloneSession(activeSession), nil
	}
	return r.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef:    activeSession.SessionRef,
		MutationGuard: mutationGuard,
		Binding:       r.kernelControllerBinding(),
	})
}

func (r *Runtime) kernelControllerBinding() session.ControllerBinding {
	return session.ControllerBinding{
		Kind:         session.ControllerKindKernel,
		ControllerID: "sdk-kernel",
		AgentName:    "local",
		Label:        "SDK Kernel",
		EpochID:      r.nextID("kernel", nil),
		AttachedAt:   r.now(),
		Source:       "runtime",
	}
}

func (r *Runtime) executeACPControllerTurn(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	req agent.RunRequest,
	runID string,
	turnID string,
	handle *runner,
	admission *steeringAdmission,
) {
	admitted := false
	defer func() {
		if !admitted {
			admission.resolve(controller.ErrNotActive)
		}
	}()
	defer func() {
		if err := r.finalizeTurn(ctx, ref, turnID); err != nil {
			handle.publishError(err)
		}
	}()

	// Host tools execute outside the ACP prompt goroutine. Keep this Runner's
	// fence until their cancellation cleanup and terminal receipts have settled.
	defer handle.controllerCalls.closeAndWait()

	inputEvents, err := r.prepareTurnInputs(ctx, activeSession, turnID, req)
	if err != nil {
		handle.publishError(err)
		return
	}
	if hookrun.FromContext(ctx).TakeEarly() != nil {
		handle.publishError(errorcode.New(errorcode.Unsupported, "external ACP Agent cannot enforce an early-response hook decision"))
		return
	}
	persistedInputs, err := r.appendInputEvents(ctx, ref, inputEvents)
	if err != nil {

		handle.publishError(err)
		return
	}
	for _, persisted := range persistedInputs {
		handle.publishEvent(persisted)
	}
	reader, _ := r.sessions.(model.AttachmentReader)
	parts, err := controllerInputParts(ctx, reader, ref.SessionID, persistedInputs)
	if err != nil {
		handle.publishError(err)
		return
	}
	turnReq := controller.TurnRequest{
		SessionRef:   ref,
		Session:      activeSession,
		TurnID:       turnID,
		ContentParts: parts,

		Mode:              r.policyMode(req.AgentSpec),
		ApprovalRequester: controllerApprovalRequester{runtime: r, requester: req.ApprovalRequester, sessionRef: ref, session: activeSession, runID: runID, turnID: turnID},
		CommitBinding: func(commitCtx context.Context) error {
			return r.commitControllerBinding(commitCtx, ref)
		},
	}
	contextRoute, err := r.buildControllerTurnContext(ctx, activeSession, ref, turnID)
	if err != nil {

		handle.publishError(err)
		return
	}
	turnReq.FreshContext = contextRoute.FreshContext
	turnReq.ContextSyncSeq = contextRoute.SyncSeq
	if contextRoute.SyncSeq > activeSession.Controller.ContextSyncSeq {
		turnReq.Context = contextRoute.Context
	}
	var toolFactOrdinal uint64
	newForwarding := func(forwardCtx context.Context) (agent.ControllerEventSession, error) {
		return r.controllerEventForwarder.BeginControllerEvents(forwardCtx, agent.ControllerEventForwardRequest{
			ActiveSession: activeSession,
			SessionRef:    ref,
			MutationGuard: session.RuntimeMutationGuard(ctx),
			TurnID:        turnID,
			Publisher:     handle,
			Normalize: func(active session.Session, turn string, event *session.Event) *session.Event {
				normalized := normalizeEvent(active, turn, event)
				if scopeRuntimeToolFactIdentity(normalized, runID, turnID, toolFactOrdinal+1) {
					toolFactOrdinal++
				}
				return normalized
			},
			IsUserEcho: isACPControllerUserEcho,
		})
	}
	var turnHandle controller.TurnHandle
	var forwarding agent.ControllerEventSession
	err = r.traceOperation(ctx, r.lifecycleEvent(ctx, agent.LifecycleRun, "acp", ""), func(runCtx context.Context) error {
		return r.traceOperation(runCtx, r.lifecycleEvent(runCtx, agent.LifecycleTurn, "acp", ""), func(turnCtx context.Context) error {
			var turnErr error
			forwarding, turnErr = newForwarding(turnCtx)
			if turnErr != nil {
				return turnErr
			}
			turnReq.Observer = forwarding
			turnHandle, turnErr = r.controllers.RunTurn(turnCtx, turnReq)
			if errors.Is(turnErr, controller.ErrNotActive) {
				if r.controllerRecovery == nil {
					return fmt.Errorf("agent-sdk/runtime: controller recovery coordinator is unavailable")
				}
				activeSession, turnErr = r.controllerRecovery.ReattachController(turnCtx, controller.RecoveryRequest{
					SessionRef:    ref,
					Session:       activeSession,
					ExcludeTurnID: turnID,
				})
				if turnErr == nil {
					// Recovery creates a new controller instance inside this same
					// admitted Turn. External tool calls must use its current epoch.
					r.registerActiveRun(ref, activeSession, turnID, handle, req)
					turnReq.Session = activeSession
					contextRoute, turnErr = r.buildControllerTurnContext(turnCtx, activeSession, ref, turnID)
					if turnErr == nil {
						turnReq.Context = contextRoute.Context
						turnReq.FreshContext = contextRoute.FreshContext
						turnReq.ContextSyncSeq = contextRoute.SyncSeq
						forwarding, turnErr = newForwarding(turnCtx)
						if turnErr == nil {
							turnReq.Observer = forwarding
							turnHandle, turnErr = r.controllers.RunTurn(turnCtx, turnReq)
						}
					}
				}
			}
			return turnErr
		})
	})
	if err != nil {

		handle.publishError(err)
		return
	}
	if turnHandle == nil {
		handle.publishError(errors.New("agent-sdk/runtime: main controller returned no Turn handle"))
		return
	}
	defer turnHandle.Close()
	admission.resolve(nil)
	admitted = true
	err = handle.waitController(ctx, turnHandle)
	handle.controllerCalls.closeAndWait()
	if err == nil && forwarding != nil {
		err = forwarding.Complete(ctx)
	} else if err == nil {
		err = errors.New("agent-sdk/runtime: controller event forwarding session is unavailable")
	}
	if err != nil {
		handle.publishError(err)
		return
	}
	if err := r.updateControllerContextCheckpoint(ctx, ref); err != nil {
		handle.publishError(err)
	}
}

// Cancellation requests shutdown. Only the controller's completion proves
// that it can no longer publish observations under this Turn's claim.
func (r *runner) waitController(ctx context.Context, remote controller.TurnHandle) error {
	cancel := sync.OnceValue(func() error { return remote.Cancel().Err })
	r.setCancelHook(cancel)
	stop := context.AfterFunc(ctx, func() { _ = cancel() })
	defer stop()
	if err := remote.WaitCompletion(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	return ctx.Err()
}
