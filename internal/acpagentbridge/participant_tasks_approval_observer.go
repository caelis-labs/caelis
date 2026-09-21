package acpagentbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func (o *acpParticipantTasks) wakeApprovals() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// One observer reconciles both feeds against the Host's current approval head.
// Permission calls have separate cancellation scopes so a settled head cannot
// block its successor. Completed attempts remain claimed until the head changes,
// including rejected or uncertain submissions that must not be resent.
func (o *acpParticipantTasks) forwardApprovals(ctx context.Context) {
	var active *appserver.ActiveApproval
	var response <-chan error
	cancelActive := func() {}
	var workers sync.WaitGroup
	defer func() {
		cancelActive()
		workers.Wait()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-response:
			response = nil
			cancelActive()
			o.reportApprovalError(ctx, err)
			o.wakeApprovals()
		case <-o.wake:
			state, err := o.agent.sessionClient.InspectSession(ctx, appserver.StateRequest{SessionID: o.sessionID})
			if err != nil {
				o.reportApprovalError(ctx, err)
				continue
			}
			head := state.Approval.Active
			if active != nil && !sameParticipantApproval(head, active) {
				cancelActive()
				active, response = nil, nil
			}
			if active != nil || head == nil || head.Scope != eventstream.ScopeSubagent || !o.contains(head.ScopeID) {
				continue
			}
			if head.Permission == nil || head.Target.HandleID == "" {
				o.reportApprovalError(ctx, errors.New("background approval has no permission or owner target"))
				continue
			}
			active = head
			requestCtx, cancel := context.WithCancel(ctx)
			cancelActive = cancel
			result := make(chan error, 1)
			response = result
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer cancel()
				result <- o.requestApproval(requestCtx, head)
			}()
		}
	}
}

func (o *acpParticipantTasks) reportApprovalError(ctx context.Context, err error) {
	if err != nil && ctx.Err() == nil {
		_ = emitACPNotice(ctx, o.callbacks, o.sessionID, eventstream.Envelope{
			Kind: eventstream.KindNotice, Notice: fmt.Sprintf("Background participant approval failed: %v", err),
		}, "", nil)
	}
}

func sameParticipantApproval(current, expected *appserver.ActiveApproval) bool {
	return current != nil && expected != nil && current.RequestID == expected.RequestID &&
		current.Scope == expected.Scope && current.ScopeID == expected.ScopeID && current.Target == expected.Target
}
