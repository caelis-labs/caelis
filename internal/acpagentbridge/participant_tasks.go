package acpagentbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/acppermission"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// acpParticipantTasks owns presentation subscriptions for explicit background
// Tasks. One Session observer survives prompt completion and handle follow-ups;
// closing it detaches delivery without cancelling Host work.
type acpParticipantTasks struct {
	agent     *RuntimeAgent
	sessionID string
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	wake      chan struct{}
	callbacks PromptCallbacks
	mux       *acpTaskStreamMux
	mu        sync.Mutex
	tasks     map[string]struct{}
	approvals map[eventstream.ApprovalRequestID]struct{}
}

func (a *RuntimeAgent) observeParticipantTask(ctx context.Context, child controlprompt.AgentRunResult, cb PromptCallbacks) error {
	if a.sessionClient == nil || a.taskStreamClient == nil {
		return errors.New("internal/acpagentbridge: background participant observation is unavailable")
	}
	sessionID, taskID := strings.TrimSpace(child.SessionID), strings.TrimSpace(child.TaskID)
	if sessionID == "" || taskID == "" {
		return errors.New("internal/acpagentbridge: background participant identity is incomplete")
	}
	a.mu.Lock()
	if a.participantTasks == nil {
		a.participantTasks = make(map[string]*acpParticipantTasks)
	}
	observer := a.participantTasks[sessionID]
	if observer == nil {
		deliveryCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		observer = &acpParticipantTasks{agent: a, sessionID: sessionID, ctx: deliveryCtx, cancel: cancel,
			done: make(chan struct{}), wake: make(chan struct{}, 1), callbacks: cb,
			mux:   newACPTaskStreamClientMux(deliveryCtx, a.taskStreamClient, sessionID),
			tasks: make(map[string]struct{}), approvals: make(map[eventstream.ApprovalRequestID]struct{}),
		}
		a.participantTasks[sessionID] = observer
		go observer.run()
	}
	observer.mu.Lock()
	observer.tasks[taskID] = struct{}{}
	observer.mu.Unlock()
	observer.mux.ObserveTask(taskID)
	a.mu.Unlock()
	select {
	case observer.wake <- struct{}{}:
	default:
	}
	return nil
}

func (a *RuntimeAgent) participantTaskObserver(sessionID, taskID string) *acpParticipantTasks {
	a.mu.Lock()
	observer := a.participantTasks[sessionID]
	a.mu.Unlock()
	if observer != nil && observer.contains(taskID) {
		return observer
	}
	return nil
}

func (o *acpParticipantTasks) contains(taskID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.tasks[taskID]
	return ok
}

func (a *RuntimeAgent) closeParticipantTasks(sessionID string) {
	a.mu.Lock()
	observer := a.participantTasks[sessionID]
	delete(a.participantTasks, sessionID)
	a.mu.Unlock()
	if observer != nil {
		observer.cancel()
		<-observer.done
	}
}

func (o *acpParticipantTasks) run() {
	defer close(o.done)
	defer o.cancel()
	defer o.mux.Close()
	defer func() {
		o.agent.mu.Lock()
		if o.agent.participantTasks[o.sessionID] == o {
			delete(o.agent.participantTasks, o.sessionID)
		}
		o.agent.mu.Unlock()
	}()
	if err := o.forward(); err != nil && o.ctx.Err() == nil {
		_ = emitACPNotice(o.ctx, o.callbacks, o.sessionID, eventstream.Envelope{
			Kind: eventstream.KindNotice, Notice: fmt.Sprintf("Background participant updates stopped: %v", err),
		}, "", nil)
	}
}

func (o *acpParticipantTasks) forward() error {
	reconnected, err := o.agent.sessionClient.Reconnect(o.ctx, appserver.ReconnectRequest{SessionID: o.sessionID, HistoryTurns: 1})
	if err != nil {
		return err
	}
	if reconnected.Subscription == nil {
		return errors.New("background participant approval feed is unavailable")
	}
	defer reconnected.Subscription.Close()
	approvalCtx, cancelApprovals := context.WithCancel(o.ctx)
	approvalDone := make(chan struct{})
	approvalErrors := make(chan error, 1)
	go func() {
		defer close(approvalDone)
		for {
			select {
			case <-approvalCtx.Done():
				return
			case <-o.wake:
				if err := o.approveCurrent(approvalCtx, ""); err != nil {
					approvalErrors <- err
					return
				}
			}
		}
	}()
	defer func() { cancelApprovals(); <-approvalDone }()
	assembler := &appserver.FeedDeliveryAssembler{}
	filter := newACPNarrativeFilter(true)
	for {
		select {
		case <-o.ctx.Done():
			return o.ctx.Err()
		case err := <-approvalErrors:
			return err
		case envelope, ok := <-o.mux.Events():
			if !ok {
				return nil
			}
			if err := o.agent.emitControlEnvelope(o.ctx, o.callbacks, o.sessionID, nil, envelope, filter); err != nil {
				return err
			}
		case delivery, ok := <-reconnected.Subscription.Deliveries():
			if !ok {
				if err := reconnected.Subscription.Err(); err != nil {
					return err
				}
				return errors.New("background participant approval feed ended")
			}
			events, _, err := assembler.Accept(delivery)
			if err != nil {
				return err
			}
			// Task streams own content. The Session feed only wakes an inspection of
			// the current approval head, never replays historical permission requests.
			inspect := delivery.Kind == appserver.FeedDeliverySync
			for _, env := range events {
				inspect = inspect || env.Kind == eventstream.KindRequestPermission || env.ApprovalRequestID != ""
			}
			if inspect {
				select {
				case o.wake <- struct{}{}:
				default:
				}
			}
		}
	}
}

// Both the foreground prompt and background feed can observe the same FIFO
// head. Claim its request ID once, then resolve only that exact Host target.
func (o *acpParticipantTasks) approveCurrent(ctx context.Context, requested eventstream.ApprovalRequestID) error {
	active, err := o.claimApproval(ctx, requested)
	if err != nil || active == nil {
		return err
	}
	defer func() {
		o.mu.Lock()
		delete(o.approvals, active.RequestID)
		o.mu.Unlock()
	}()
	wire, err := acppermission.EncodePermissionRequest(session.SessionRef{SessionID: o.sessionID}, active.Permission, nil)
	if err != nil {
		return err
	}
	payload, err := acppermission.DecodePermissionRequest(wire)
	if err != nil {
		return err
	}
	request, err := sdkPermissionRequestFromSchema(wire)
	if err != nil {
		return err
	}
	response, err := o.callbacks.RequestPermission(ctx, request)
	if err != nil {
		return err
	}
	decision := approvalDecisionFromACPResponse(active.RequestID, payload, response)
	current, err := o.agent.sessionClient.InspectSession(ctx, appserver.StateRequest{SessionID: o.sessionID})
	if err != nil {
		return err
	}
	if current.Approval.Active == nil || current.Approval.Active.RequestID != active.RequestID {
		return nil
	}
	result, err := o.agent.sessionClient.ResolveApproval(ctx, appserver.ResolveApprovalRequest{
		WriteBase: appserver.WriteBase{OperationID: newACPSessionOperationID("background-approval"), SessionID: o.sessionID,
			ExpectedRevision: &current.Revision, ExpectedControllerEpoch: current.Controller.EpochID},
		Target: active.Target, ApprovalRequestID: string(active.RequestID), Outcome: decision.Outcome,
		OptionID: decision.OptionID, Approved: decision.Approved, Reason: decision.Reason, ReviewText: decision.ReviewText,
	})
	return appserver.CommandMutationError(result, err)
}

// Inspect while holding the claim lock so a competing feed cannot reuse a
// stale head after the first response has already committed.
func (o *acpParticipantTasks) claimApproval(ctx context.Context, requested eventstream.ApprovalRequestID) (*appserver.ActiveApproval, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	state, err := o.agent.sessionClient.InspectSession(ctx, appserver.StateRequest{SessionID: o.sessionID})
	if err != nil {
		return nil, err
	}
	active := state.Approval.Active
	if active == nil || active.Scope != eventstream.ScopeSubagent || (requested != "" && requested != active.RequestID) {
		return nil, nil
	}
	if _, ok := o.tasks[active.ScopeID]; !ok {
		return nil, nil
	}
	if _, seen := o.approvals[active.RequestID]; seen {
		return nil, nil
	}
	if active.Permission == nil || active.Target.HandleID == "" {
		return nil, errors.New("background approval has no permission or owner target")
	}
	o.approvals[active.RequestID] = struct{}{}
	return active, nil
}
