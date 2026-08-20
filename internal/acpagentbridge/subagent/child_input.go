package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/authentication"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
	acpschema "github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/google/uuid"
)

// BindChildActivityObserver installs the sole output observer for one exact
// durable child endpoint. Rebinding has an independent journal-delivery fence
// so a failed observer can be replaced while a terminal producer is waiting
// for durable acknowledgement.
func (r *Runner) BindChildActivityObserver(
	ctx context.Context,
	target agent.ChildEndpointRef,
	afterCursor uint64,
	observer agent.ChildActivityObserver,
) error {
	if r == nil || observer == nil {
		return errorcode.New(errorcode.InvalidArgument, "internal/acpagentbridge/subagent: child activity observer is required")
	}
	if err := validateChildEndpointRef(target); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	slot, err := r.lookupChildSlot(target)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !slot.matchesTarget(target) {
		return childSlotTargetError(target)
	}
	slot.bindObserver(afterCursor, observer)
	return nil
}

// BindChildEndpoint installs process-local recovery state for one exact
// Runtime-validated durable binding without opening or resuming the remote
// Session. SubmitChildInput owns that later standard ACP effect.
func (r *Runner) BindChildEndpoint(
	ctx context.Context,
	target agent.ChildEndpointRef,
	spawn tasksubagent.SpawnContext,
) error {
	if r == nil {
		return errorcode.New(errorcode.FailedPrecondition, "internal/acpagentbridge/subagent: child runner is unavailable")
	}
	target = agent.NormalizeChildEndpointRef(target)
	if err := validateChildEndpointRef(target); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key := target.EndpointKey
	r.mu.Lock()
	if r.slots == nil {
		r.slots = map[string]*childSlot{}
	}
	if r.runs == nil {
		r.runs = map[string]*childRun{}
	}
	slot := r.slots[key]
	if slot == nil {
		done := make(chan struct{})
		close(done)
		run := &childRun{
			anchor: delegation.Anchor{
				TaskID: target.EndpointKey, SessionID: target.SessionID, AgentID: target.ParticipantID,
			},
			agentName: firstNonEmpty(target.Placement.Agent, target.Placement.Model),
			spawn:     spawn, taskID: target.EndpointKey, sink: spawn.Streams, completion: spawn.Completion,
			state: delegation.StateInterrupted, updatedAt: r.clock(), done: done,
		}
		slot = newChildSlot(target, run)
		r.slots[key] = slot
		r.runs[key] = run
	}
	r.mu.Unlock()

	if !slot.matchesTarget(target) {
		return childSlotTargetError(target)
	}
	run := slot.currentRun()
	if run == nil {
		return errorcode.New(errorcode.Conflict, "internal/acpagentbridge/subagent: child endpoint recovery state is unavailable")
	}
	run.mu.Lock()
	run.spawn = spawn
	run.sink = spawn.Streams
	run.completion = spawn.Completion
	run.mu.Unlock()
	if spawn.ActivityObserver != nil {
		slot.bindObserver(spawn.ActivityAfterCursor, spawn.ActivityObserver)
	}
	return nil
}

// SubmitChildInput routes ordinary conversation input to one exact child
// endpoint. It never claims or mutates a Task operation; Task observation is
// driven only by child activity output.
func (r *Runner) SubmitChildInput(ctx context.Context, raw agent.ChildInputRequest) (agent.ChildInputResult, error) {
	if r == nil {
		return agent.ChildInputResult{}, errorcode.New(errorcode.FailedPrecondition, "internal/acpagentbridge/subagent: child runner is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req := agent.CloneChildInputRequest(raw)
	if err := validateChildEndpointRef(req.Target); err != nil {
		return agent.ChildInputResult{}, err
	}
	if !session.ActorRefHasIdentity(req.Source) {
		return agent.ChildInputResult{}, errorcode.New(errorcode.InvalidArgument, "internal/acpagentbridge/subagent: trusted child input source is required")
	}
	prompt := acputil.BuildPromptParts(req.Input, req.ContentParts)
	if len(prompt) == 0 {
		return agent.ChildInputResult{}, errorcode.New(errorcode.InvalidArgument, "internal/acpagentbridge/subagent: child input is required")
	}
	slot, err := r.lookupChildSlot(req.Target)
	if err != nil {
		return agent.ChildInputResult{}, err
	}

	for {
		slot.opMu.Lock()
		wait := slot.pendingPromptDispatch()
		if wait == nil {
			wait = slot.pendingTerminalSettlement()
		}
		if wait != nil {
			slot.opMu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return agent.ChildInputResult{}, ctx.Err()
			}
		}
		break
	}
	if !slot.matchesTarget(req.Target) {
		slot.opMu.Unlock()
		return agent.ChildInputResult{}, childSlotTargetError(req.Target)
	}
	run := slot.currentRun()
	if run == nil {
		slot.opMu.Unlock()
		return agent.ChildInputResult{}, errorcode.New(errorcode.Conflict, "internal/acpagentbridge/subagent: child endpoint is detached")
	}
	run.mu.RLock()
	running := run.running || run.finishing
	run.mu.RUnlock()
	if running {
		result, inputErr := r.submitActiveChildInputLocked(ctx, slot, run, req, prompt)
		slot.opMu.Unlock()
		return result, inputErr
	}
	return r.submitIdleChildInput(ctx, slot, run, req, prompt)
}

func (r *Runner) lookupChildSlot(target agent.ChildEndpointRef) (*childSlot, error) {
	key := strings.TrimSpace(target.EndpointKey)
	if key == "" {
		return nil, errorcode.New(errorcode.InvalidArgument, "internal/acpagentbridge/subagent: child endpoint key is required")
	}
	r.mu.RLock()
	slot := r.slots[key]
	r.mu.RUnlock()
	if slot == nil {
		return nil, errorcode.New(errorcode.NotFound, fmt.Sprintf("internal/acpagentbridge/subagent: child endpoint %q is not active", key))
	}
	return slot, nil
}

func (r *Runner) submitActiveChildInputLocked(
	ctx context.Context,
	slot *childSlot,
	run *childRun,
	req agent.ChildInputRequest,
	prompt []json.RawMessage,
) (agent.ChildInputResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ChildInputResult{}, err
	}
	run.mu.RLock()
	supportsSteering := run.supportsSteering
	supportsImages := run.promptCapabilities.Image
	activityID := slot.activityCheckpoint().activityID
	run.mu.RUnlock()
	if !supportsSteering {
		return agent.ChildInputResult{}, errorcode.New(errorcode.Unsupported, "internal/acpagentbridge/subagent: child endpoint does not support ACP steering")
	}
	if acputil.ContentPartsContainImage(req.ContentParts) && !supportsImages {
		return agent.ChildInputResult{}, errorcode.New(errorcode.Unsupported, "internal/acpagentbridge/subagent: child endpoint does not support image input")
	}
	rpcCtx, cancelRPC := context.WithCancel(ctx)
	if !slot.beginSteering(cancelRPC) {
		cancelRPC()
		return agent.ChildInputResult{}, errorcode.New(errorcode.Conflict, "internal/acpagentbridge/subagent: child input state changed")
	}
	response, err := r.callChildSteering(rpcCtx, run, prompt)
	cancelRPC()
	if err != nil {
		if childInputEffectUnknown(err) {
			unknown := joinChildInputUnknown("internal/acpagentbridge/subagent: child steering outcome cannot be proven", err)
			slot.settleSteeringFrames(false)
			r.finishDriveLocked(context.WithoutCancel(ctx), run, "", unknown)
			return agent.ChildInputResult{}, unknown
		}
		slot.settleSteeringFrames(true)
		return agent.ChildInputResult{}, childInputProvenFailure("internal/acpagentbridge/subagent: child steering was rejected", err)
	}
	switch response.Outcome {
	case client.SessionSteeringInjected:
		slot.settleSteeringFrames(true)
		return agent.ChildInputResult{ActivityID: activityID}, nil
	case client.SessionSteeringFailed, client.SessionSteeringPromptRequired:
		slot.settleSteeringFrames(true)
		return agent.ChildInputResult{}, errorcode.New(errorcode.FailedPrecondition, fmt.Sprintf(
			"internal/acpagentbridge/subagent: child steering was not injected: %s", response.Outcome,
		))
	case client.SessionSteeringStartedNewTurn:
		unknown := errorcode.New(errorcode.UnknownOutcome, "internal/acpagentbridge/subagent: child steering started an unaddressed remote Turn")
		slot.settleSteeringFrames(false)
		r.finishDriveLocked(context.WithoutCancel(ctx), run, "", unknown)
		return agent.ChildInputResult{}, unknown
	default:
		unknown := errorcode.New(errorcode.UnknownOutcome, fmt.Sprintf(
			"internal/acpagentbridge/subagent: child steering returned unknown outcome %q", response.Outcome,
		))
		slot.settleSteeringFrames(false)
		r.finishDriveLocked(context.WithoutCancel(ctx), run, "", unknown)
		return agent.ChildInputResult{}, unknown
	}
}

func (r *Runner) callChildSteering(ctx context.Context, run *childRun, prompt []json.RawMessage) (client.SessionSteeringResponse, error) {
	run.mu.RLock()
	acpClient := run.client
	sessionID := strings.TrimSpace(run.anchor.SessionID)
	agentID := firstNonEmpty(run.agentName, run.anchor.AgentID)
	configured := controlagents.NormalizeAuthentication(run.configuredAuth)
	methods := controlagents.CloneAuthenticationMethods(run.authenticationMethods)
	run.mu.RUnlock()
	if acpClient == nil || sessionID == "" {
		return client.SessionSteeringResponse{}, errorcode.New(errorcode.Conflict, "internal/acpagentbridge/subagent: child steering transport is unavailable")
	}
	return authentication.RecoverConfiguredCall(
		ctx, acpClient, methods, agentID, configured,
		func(callCtx context.Context, activeClient *client.Client) (client.SessionSteeringResponse, error) {
			return activeClient.SteerPartsWithAbort(callCtx, sessionID, prompt, nil, func() {
				_ = activeClient.Close(context.Background())
			})
		},
	)
}

type childIdleCheckpoint struct {
	state           delegation.State
	outputPreview   string
	failureDetail   string
	result          string
	agentText       string
	actionSummary   subagentActionSummary
	finalAssistant  acpschema.FinalAssistantAccumulator
	updatedAt       time.Time
	running         bool
	finishing       bool
	cancelRequested bool
	cancelFailed    bool
	cancelResolved  chan struct{}
	done            chan struct{}
}

// promptAuthRetryFence owns the atomic handoff from a fully written prompt to
// an auth-required retry. The JSON-RPC response observer establishes the next
// slot reservation before the response is visible to its waiter.
type promptAuthRetryFence struct {
	mu     sync.Mutex
	slot   *childSlot
	cancel context.CancelFunc
	done   chan struct{}
	closed bool
}

func newPromptAuthRetryFence(slot *childSlot, dispatchDone chan struct{}, cancel context.CancelFunc) *promptAuthRetryFence {
	return &promptAuthRetryFence{slot: slot, cancel: cancel, done: dispatchDone}
}

func (f *promptAuthRetryFence) observeAuthRequired() error {
	if f == nil || f.slot == nil {
		return errorcode.New(errorcode.FailedPrecondition, "internal/acpagentbridge/subagent: authenticated prompt retry has no endpoint owner")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errorcode.New(errorcode.FailedPrecondition, "internal/acpagentbridge/subagent: authenticated prompt response owner is closed")
	}
	next, reserved := f.slot.transitionPromptDispatch(f.done, f.cancel)
	if !reserved {
		return errorcode.New(errorcode.Conflict, "internal/acpagentbridge/subagent: authenticated prompt retry conflicts with another dispatch")
	}
	f.done = next
	return nil
}

func (f *promptAuthRetryFence) current() chan struct{} {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.done
}

func (f *promptAuthRetryFence) finish(expected chan struct{}) {
	if f == nil || f.slot == nil || expected == nil {
		return
	}
	f.slot.opMu.Lock()
	f.finishLocked(expected)
	f.slot.opMu.Unlock()
}

// finishLocked releases one exact reservation while the caller owns slot.opMu.
func (f *promptAuthRetryFence) finishLocked(expected chan struct{}) {
	if f == nil || f.slot == nil || expected == nil {
		return
	}
	f.mu.Lock()
	f.slot.finishPromptDispatch(expected)
	if f.done == expected {
		f.done = nil
	}
	f.mu.Unlock()
}

func (f *promptAuthRetryFence) closeAndFinishCurrent() {
	if f == nil || f.slot == nil {
		return
	}
	f.slot.opMu.Lock()
	f.closeLocked()
	f.slot.opMu.Unlock()
}

// closeLocked disarms the response observer and clears whichever exact retry
// reservation it established. The caller owns slot.opMu.
func (f *promptAuthRetryFence) closeLocked() {
	if f == nil || f.slot == nil {
		return
	}
	f.mu.Lock()
	f.closed = true
	if f.done != nil {
		f.slot.finishPromptDispatch(f.done)
		f.done = nil
	}
	f.mu.Unlock()
}

func (f *promptAuthRetryFence) cancelOperation() {
	if f != nil && f.cancel != nil {
		f.cancel()
	}
}

func checkpointIdleRun(run *childRun) childIdleCheckpoint {
	actionSummary := run.actionSummary
	actionSummary.blocks = append([]subagentActivityBlock(nil), actionSummary.blocks...)
	return childIdleCheckpoint{
		state: run.state, outputPreview: run.outputPreview, failureDetail: run.failureDetail,
		result: run.result, agentText: run.agentText, updatedAt: run.updatedAt,
		actionSummary: actionSummary, finalAssistant: run.finalAssistant,
		running: run.running, finishing: run.finishing, cancelRequested: run.cancelRequested,
		cancelFailed: run.cancelFailed, cancelResolved: run.cancelResolved, done: run.done,
	}
}

func restoreIdleRun(run *childRun, checkpoint childIdleCheckpoint) {
	run.state = checkpoint.state
	run.outputPreview = checkpoint.outputPreview
	run.failureDetail = checkpoint.failureDetail
	run.result = checkpoint.result
	run.agentText = checkpoint.agentText
	run.actionSummary = checkpoint.actionSummary
	run.finalAssistant = checkpoint.finalAssistant
	run.updatedAt = checkpoint.updatedAt
	run.running = checkpoint.running
	run.finishing = checkpoint.finishing
	run.cancelRequested = checkpoint.cancelRequested
	run.cancelFailed = checkpoint.cancelFailed
	run.cancelResolved = checkpoint.cancelResolved
	run.done = checkpoint.done
}

func (r *Runner) submitIdleChildInput(
	ctx context.Context,
	slot *childSlot,
	run *childRun,
	req agent.ChildInputRequest,
	prompt []json.RawMessage,
) (agent.ChildInputResult, error) {
	// slot.opMu is held on entry.
	run.mu.RLock()
	state := run.state
	run.mu.RUnlock()
	if !delegationStateCanStartTurn(state) {
		slot.opMu.Unlock()
		return agent.ChildInputResult{}, errorcode.New(errorcode.Conflict, fmt.Sprintf(
			"internal/acpagentbridge/subagent: child endpoint is %s", state,
		))
	}
	if state != delegation.StateCompleted {
		recovery := childInputReconnectRequest(run, req.Target)
		var err error
		run, err = r.reconnectChildEndpointLocked(ctx, run.anchor, recovery, slot)
		if err != nil {
			slot.opMu.Unlock()
			return agent.ChildInputResult{}, childInputProvenFailure("internal/acpagentbridge/subagent: resume child endpoint", err)
		}
	}
	run.mu.RLock()
	acpClient := run.client
	sessionID := strings.TrimSpace(run.anchor.SessionID)
	supportsImages := run.promptCapabilities.Image
	producerCtx := run.ctx
	run.mu.RUnlock()
	if acpClient == nil || sessionID == "" {
		slot.opMu.Unlock()
		return agent.ChildInputResult{}, errorcode.New(errorcode.Conflict, "internal/acpagentbridge/subagent: child prompt transport is unavailable")
	}
	if acputil.ContentPartsContainImage(req.ContentParts) && !supportsImages {
		slot.opMu.Unlock()
		return agent.ChildInputResult{}, errorcode.New(errorcode.Unsupported, "internal/acpagentbridge/subagent: child endpoint does not support image input")
	}
	prepared, err := acpClient.PreparePromptParts(sessionID, prompt, nil)
	if err != nil {
		slot.opMu.Unlock()
		return agent.ChildInputResult{}, childInputProvenFailure("internal/acpagentbridge/subagent: prepare child prompt", err)
	}
	if producerCtx == nil {
		producerCtx = detachedChildContext(ctx)
	}
	responseCtx, cancelResponse := context.WithCancel(producerCtx)
	activityCheckpoint := slot.activityCheckpoint()
	activityID := uuid.NewString()
	run.mu.Lock()
	runCheckpoint := checkpointIdleRun(run)
	run.state = delegation.StateRunning
	run.running = true
	run.outputPreview = ""
	run.actionSummary.reset()
	run.failureDetail = ""
	run.result = ""
	run.agentText = ""
	run.finalAssistant.Reset()
	run.updatedAt = r.clock()
	run.finishing = false
	run.cancelRequested = false
	run.cancelFailed = false
	run.cancelResolved = nil
	run.done = make(chan struct{})
	run.mu.Unlock()
	slot.beginActivity(activityID, run)
	dispatchCtx, cancelDispatch := context.WithCancel(ctx)
	dispatchDone := slot.beginPromptDispatch(cancelDispatch)
	fence := newPromptAuthRetryFence(slot, dispatchDone, cancelResponse)
	if observeErr := prepared.ObserveAuthRequired(fence.observeAuthRequired); observeErr != nil {
		fence.closeLocked()
		run.mu.Lock()
		restoreIdleRun(run, runCheckpoint)
		run.mu.Unlock()
		slot.restoreActivity(activityCheckpoint, run)
		prepared.Abandon()
		cancelDispatch()
		cancelResponse()
		slot.opMu.Unlock()
		return agent.ChildInputResult{}, childInputProvenFailure("internal/acpagentbridge/subagent: observe child prompt response", observeErr)
	}
	slot.opMu.Unlock()

	dispatchErr := prepared.DispatchWithAbort(dispatchCtx, func() {
		_ = acpClient.Close(context.Background())
	})
	cancelDispatch()

	slot.opMu.Lock()
	fence.finishLocked(dispatchDone)
	if dispatchErr != nil {
		cancelResponse()
		if jsonrpc.DispatchMayHaveCommitted(dispatchErr) {
			unknown := joinChildInputUnknown("internal/acpagentbridge/subagent: child prompt dispatch outcome cannot be proven", dispatchErr)
			terminalDone := r.finishDriveLocked(context.WithoutCancel(ctx), run, "", unknown)
			slot.opMu.Unlock()
			if fence.current() != nil {
				go func() {
					if terminalDone != nil {
						<-terminalDone
					}
					fence.closeAndFinishCurrent()
				}()
			}
			return agent.ChildInputResult{}, unknown
		}
		run.mu.Lock()
		restoreIdleRun(run, runCheckpoint)
		run.mu.Unlock()
		slot.restoreActivity(activityCheckpoint, run)
		prepared.Abandon()
		fence.closeLocked()
		slot.opMu.Unlock()
		return agent.ChildInputResult{}, dispatchErr
	}
	go r.drivePreparedPrompt(responseCtx, run, prepared, prompt, fence)
	slot.opMu.Unlock()
	return agent.ChildInputResult{ActivityID: activityID, StartedActivity: true}, nil
}

func childInputReconnectRequest(run *childRun, target agent.ChildEndpointRef) *tasksubagent.ReconnectRequest {
	if run == nil {
		return nil
	}
	run.mu.RLock()
	spawn := run.spawn
	selector := strings.TrimSpace(run.agentName)
	run.mu.RUnlock()
	return &tasksubagent.ReconnectRequest{
		Spawn:  spawn,
		Target: delegation.Target{Selector: selector, Placement: target.Placement},
	}
}

func (r *Runner) drivePreparedPrompt(
	ctx context.Context,
	run *childRun,
	prepared *client.PromptCall,
	prompt []json.RawMessage,
	fence *promptAuthRetryFence,
) {
	defer fence.cancelOperation()
	defer fence.closeAndFinishCurrent()
	run.mu.RLock()
	acpClient := run.client
	sessionID := strings.TrimSpace(run.anchor.SessionID)
	agentID := firstNonEmpty(run.agentName, run.anchor.AgentID)
	configured := controlagents.NormalizeAuthentication(run.configuredAuth)
	methods := controlagents.CloneAuthenticationMethods(run.authenticationMethods)
	run.mu.RUnlock()
	var slot *childSlot
	if fence != nil {
		slot = fence.slot
	}
	first := true
	resp, err := authentication.RecoverConfiguredCall(
		ctx, acpClient, methods, agentID, configured,
		func(callCtx context.Context, activeClient *client.Client) (client.PromptResponse, error) {
			if first {
				first = false
				return prepared.Wait(callCtx)
			}
			retryDispatchDone := fence.current()
			if slot == nil || retryDispatchDone == nil {
				return client.PromptResponse{}, errorcode.New(
					errorcode.FailedPrecondition,
					"internal/acpagentbridge/subagent: authenticated prompt retry has no endpoint reservation",
				)
			}
			slot.opMu.Lock()
			if callErr := callCtx.Err(); callErr != nil {
				slot.opMu.Unlock()
				return client.PromptResponse{}, callErr
			}
			retry, prepareErr := activeClient.PreparePromptParts(sessionID, prompt, nil)
			if prepareErr != nil {
				slot.opMu.Unlock()
				return client.PromptResponse{}, prepareErr
			}
			if observeErr := retry.ObserveAuthRequired(fence.observeAuthRequired); observeErr != nil {
				retry.Abandon()
				slot.opMu.Unlock()
				return client.PromptResponse{}, observeErr
			}
			slot.opMu.Unlock()
			if dispatchErr := retry.DispatchWithAbort(callCtx, func() {
				_ = activeClient.Close(context.Background())
			}); dispatchErr != nil {
				if jsonrpc.DispatchMayHaveCommitted(dispatchErr) {
					return client.PromptResponse{}, joinChildInputUnknown("internal/acpagentbridge/subagent: authenticated child prompt retry is ambiguous", dispatchErr)
				}
				return client.PromptResponse{}, dispatchErr
			}
			fence.finish(retryDispatchDone)
			return retry.Wait(callCtx)
		},
	)
	if err != nil && ctx.Err() == nil && (childConnectionError(err) || jsonrpc.DispatchMayHaveCommitted(err)) {
		err = joinChildInputUnknown("internal/acpagentbridge/subagent: child prompt response outcome cannot be proven", err)
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if slot := run.childSlot(); errorcode.Is(err, errorcode.UnknownOutcome) && slot != nil {
		slot.quarantineOutput(run)
	}
	r.finishDrive(ctx, run, resp.StopReason, err)
}

func childInputEffectUnknown(err error) bool {
	if err == nil {
		return false
	}
	if jsonrpc.DispatchMayHaveCommitted(err) || childConnectionError(err) {
		return true
	}
	return false
}

func childConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "connection closed before response") ||
		strings.Contains(text, "file already closed") ||
		strings.Contains(text, "use of closed file")
}

var (
	_ agent.ChildInputRunner            = (*Runner)(nil)
	_ agent.ChildActivityObserverBinder = (*Runner)(nil)
	_ agent.ChildEndpointBinder         = (*Runner)(nil)
)
