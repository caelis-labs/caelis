package runtime

import (
	"context"
	"errors"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// Run executes one agent turn for one existing session.
func (r *Runtime) Run(
	ctx context.Context,
	req agent.RunRequest,
) (out agent.RunResult, runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRunInput(req); err != nil {
		return agent.RunResult{}, err
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	if ref.SessionID == "" {
		return agent.RunResult{}, session.ErrInvalidSession
	}
	runID := r.nextID("run", r.runIDGenerator)
	turnID := strings.TrimSpace(req.TurnID)
	if turnID == "" {
		turnID = r.nextID("turn", nil)
	}
	if err := r.beginRun(ref, runID); err != nil {
		return agent.RunResult{}, err
	}
	runCtx, releaseWriter, err := r.sessions.AcquireWriter(ctx, ref, runID)
	if err != nil {
		r.unregisterActiveRun(runID)
		return agent.RunResult{}, err
	}
	runCtx, cancel := context.WithCancel(runCtx)
	started := false
	finish := func() error {
		cancel()
		err := releaseWriter()
		r.unregisterActiveRun(runID)
		return err
	}
	defer func() {
		if !started {
			runErr = errors.Join(runErr, finish())
		}
	}()
	activeSession, err := r.sessions.Session(runCtx, ref)
	if err != nil {
		return agent.RunResult{}, err
	}
	activeSession, err = r.ensureSessionController(runCtx, activeSession, session.RuntimeMutationGuard(runCtx))
	if err != nil {
		return agent.RunResult{}, err
	}
	isACP := activeSession.Controller.Kind == session.ControllerKindACP
	if isACP && r.controllers == nil {
		return agent.RunResult{}, errors.New("agent-sdk/runtime: ACP controller backend is not configured")
	}
	if r.hooks.HasLoopPhases() {
		_, supports := r.agentFactory.(interface{ SupportsLoopHooks() })
		if isACP || req.Agent != nil || !supports {
			return agent.RunResult{}, errorcode.New(errorcode.Unsupported, "configured loop hook phases are unavailable inside this Agent")
		}
	}
	if !isACP && req.Agent == nil {
		if err := validateRunRequestAgentSpec(req); err != nil {
			return agent.RunResult{}, err
		}
	}
	if err := r.reconcileTurnPlan(runCtx, ref, turnID); err != nil {
		return agent.RunResult{}, err
	}
	if err := r.repairToolPairs(runCtx, ref); err != nil {
		return agent.RunResult{}, err
	}
	runCtx = withLifecycleScope(runCtx, lifecycleScope{sessionRef: ref, runID: runID, turnID: turnID})
	runCtx = hookrun.WithRun(runCtx, r.hooks, agent.HookContext{SessionRef: ref, RunID: runID, TurnID: turnID, Purpose: r.hookPurpose})
	hookrun.FromContext(runCtx).Trace = r.lifecycle
	if !isACP {
		runCtx = context.WithValue(runCtx, taskScopeKey{}, newTaskContinuationScope())
	}
	handle := newRunner(runCtx, runID, cancel, req.SourceObserver)
	var execute func()
	if isACP {
		handle.controllerCalls = newControllerCallScope(runCtx)
		admission := newSteeringAdmission()
		if err := handle.setSubmissionHandler(runCtx, r.controllerSteeringHandler(runCtx, ref, turnID, handle, admission)); err != nil {
			return agent.RunResult{}, err
		}
		r.registerActiveRun(ref, activeSession, turnID, handle, req)
		execute = func() { r.executeACPControllerTurn(runCtx, activeSession, ref, req, runID, turnID, handle, admission) }
	} else {
		r.registerActiveRun(ref, activeSession, turnID, handle)
		execute = func() { r.executeKernelTurn(runCtx, activeSession, ref, runID, turnID, req, handle) }
	}
	started = true
	go func() {
		defer handle.finish(finish)
		execute()
	}()
	return agent.RunResult{Session: activeSession, Handle: handle}, nil
}

func validateRunInput(req agent.RunRequest) error {
	switch normalizeInputKind(req.InputKind) {
	case agent.SubmissionKindConversation:
		if len(req.Inputs) > 0 {
			return errorcode.New(errorcode.InvalidArgument, "agent-sdk/runtime: conversation turns cannot carry Agent communication batches")
		}
		return nil
	case agent.SubmissionKindAgentCommunication:
		if len(req.Inputs) > 0 {
			if err := agent.ValidateSubmissionInputs(agent.Submission{
				Kind: req.InputKind, Text: strings.TrimSpace(req.Input), DisplayInput: strings.TrimSpace(req.DisplayInput),
				ContentParts: req.ContentParts, Actor: req.InputActor, Inputs: req.Inputs,
			}); err != nil {
				return errorcode.Wrap(errorcode.InvalidArgument, "agent-sdk/runtime: "+err.Error(), err)
			}
			return nil
		}
		if err := session.ValidateAgentCommunicationActor(req.InputActor); err != nil {
			return errorcode.Wrap(errorcode.InvalidArgument, "agent-sdk/runtime: invalid Agent communication", err)
		}
		return nil
	default:
		return errorcode.New(errorcode.InvalidArgument, "agent-sdk/runtime: unsupported input kind")
	}
}

func (r *Runtime) beginRun(ref session.SessionRef, runID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, current := range r.activeRunners {
		if current.ref.SessionID == ref.SessionID || id == runID {
			return &agent.RunConflictError{SessionRef: ref, ActiveRunID: id}
		}
	}
	r.activeRunners[runID] = activeRun{ref: ref}

	return nil
}

func (r *Runtime) executeKernelTurn(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runID string,
	turnID string,
	req agent.RunRequest,
	handle *runner,
) {
	inputEvents, inputErr := buildRunInputEvents(activeSession, turnID, req)
	if inputErr != nil {
		handle.publishError(inputErr)
		return
	}
	inputEvents, inputErr = r.prepareHookInputs(ctx, inputEvents)
	if inputErr == nil {
		var start []*session.Event
		start, inputErr = r.runTurnStartHooks(ctx)
		inputEvents = append(inputEvents, start...)
	}
	if inputErr == nil && hookrun.FromContext(ctx).HasEarly() {
		_, supports := r.agentFactory.(interface{ SupportsLoopHooks() })
		if req.Agent != nil || !supports {
			inputErr = errorcode.New(errorcode.Unsupported, "this external Agent cannot enforce an early-response hook decision")
		}
	}
	if inputErr != nil {
		handle.publishError(inputErr)
		return
	}
	lifecycleErr := r.traceOperation(ctx, r.lifecycleEvent(ctx, agent.LifecycleRun, "", ""), func(runCtx context.Context) error {
		return r.traceOperation(runCtx, r.lifecycleEvent(runCtx, agent.LifecycleTurn, "", ""), func(turnCtx context.Context) error {
			runErr := r.runWithOverflowRecovery(turnCtx, activeSession, ref, runID, turnID, req, inputEvents, handle)
			scope := taskScopeFromContext(turnCtx)
			scope.stopCodeMode()
			if runErr != nil {
				scope.revoke(runErr, true)
			}
			return errors.Join(runErr, scope.join())
		})
	})
	if err := errors.Join(lifecycleErr, r.finalizeTurn(ctx, ref, turnID)); err != nil {
		handle.publishError(err)
	}
}
