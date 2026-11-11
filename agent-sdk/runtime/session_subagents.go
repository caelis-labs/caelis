package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
)

// CancelSessionSubagents revokes current participant input admissions, removes
// their durable bindings, and requests cancellation of the Session's managed
// child activities through the existing Task cancellation saga. Task records and
// canonical history remain readable. A later explicit spawn creates new work.
//
// It joins admissions and Spawns accepted before cancellation, including a Spawn
// whose child identity arrives after cancellation. The returned endpoints include
// those late identities so Control can validate and cancel descendant Sessions.
// It does not cancel the Session's own Run or declare a producer stopped: only a
// producer completion or explicit terminal Wait result settles a running Task.
// The caller must cancel the parent Run before invoking this method and retain
// its ordinary producer completion/fence lifetime. A context error means admission
// cleanup or cancellation reconciliation is incomplete.
func (r *Runtime) CancelSessionSubagents(ctx context.Context, ref session.SessionRef) ([]agent.ChildEndpointRef, error) {
	if r == nil || r.sessions == nil || r.tasks == nil || r.tasks.store == nil {
		return nil, fmt.Errorf("session subagent lifecycle is unavailable")
	}
	if _, ok := r.tasks.store.(taskapi.CASStore); !ok {
		return nil, fmt.Errorf("session subagent cancellation requires task.CASStore")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ref = session.NormalizeSessionRef(ref)
	ctx = session.ContextWithControlMutation(ctx, session.ControlMutationPurposeSubagentCancellation)

	r.participantMu.Lock()
	entries, err := r.tasks.store.ListSession(ctx, ref)
	if err != nil {
		r.participantMu.Unlock()
		return nil, err
	}
	active, err := r.sessions.Session(ctx, ref)
	if err != nil {
		r.participantMu.Unlock()
		return nil, err
	}
	ids := map[string]struct{}{}
	for _, entry := range entries {
		if entry != nil && entry.Kind == taskapi.KindSubagent {
			ids[entry.TaskID] = struct{}{}
		}
	}
	initialIDs := make(map[string]struct{}, len(ids))
	for id := range ids {
		initialIDs[id] = struct{}{}
	}
	var reservations []*participantInputReservation
	for reservation := range r.participantInputs {
		if reservation.ref.SessionID == ref.SessionID {
			reservation.cancel()
			r.releaseParticipantInputPinsLocked(reservation)
			reservations = append(reservations, reservation)
			if reservation.taskID != "" {
				ids[reservation.taskID] = struct{}{}
				// A durable post_spawn write can precede installation of its
				// process-local producer. Only the joined pass may classify it.
				delete(initialIDs, reservation.taskID)
			}
		}
	}
	knownEndpoints := map[string]agent.ChildEndpointRef{}
	var cancelErr error
	for _, binding := range active.Participants {
		if binding.Kind != session.ParticipantKindSubagent {
			continue
		}
		ids[binding.DelegationID] = struct{}{}
		knownEndpoints[binding.DelegationID] = agent.NormalizeChildEndpointRef(agent.ChildEndpointRef{
			ParticipantID: binding.ID, SessionID: binding.SessionID, EndpointKey: binding.DelegationID,
			Role: binding.Role, Placement: binding.Placement,
		})
		cancelErr = errors.Join(cancelErr, r.removeCancelledSubagentBindingLocked(ctx, ref, binding))
	}
	r.participantMu.Unlock()

	// Start cancellation before joining input receipts: an accepted receipt may
	// itself be waiting for the child producer to acknowledge cancellation.
	var cancellations []chan error
	for id := range initialIDs {
		id := id
		done := make(chan error, 1)
		cancellations = append(cancellations, done)
		go func() { done <- r.tasks.cancelSessionSubagent(ctx, ref, id) }()
	}
	for _, reservation := range reservations {
		select {
		case <-reservation.done:
		case <-ctx.Done():
			endpoints, loadErr := r.sessionSubagentEndpoints(context.WithoutCancel(ctx), ref, ids, knownEndpoints)
			return endpoints, errors.Join(cancelErr, ctx.Err(), loadErr)
		}
	}
	for _, cancellation := range cancellations {
		select {
		case err := <-cancellation:
			cancelErr = errors.Join(cancelErr, err)
		case <-ctx.Done():
			endpoints, loadErr := r.sessionSubagentEndpoints(context.WithoutCancel(ctx), ref, ids, knownEndpoints)
			return endpoints, errors.Join(cancelErr, ctx.Err(), loadErr)
		}
	}
	// A Spawn may have had no anchor when the first cancellation pass ran.
	// Reconcile only Spawns joined above, never unrelated newly admitted work.
	joinedEntries, listErr := r.tasks.store.ListSession(ctx, ref)
	cancelErr = errors.Join(cancelErr, listErr)
	for _, entry := range joinedEntries {
		for _, reservation := range reservations {
			if entry != nil && entry.Kind == taskapi.KindSubagent && entry.TaskID == reservation.taskID {
				cancelErr = errors.Join(cancelErr, r.tasks.cancelSessionSubagent(ctx, ref, entry.TaskID))
				break
			}
		}
	}
	endpoints, loadErr := r.sessionSubagentEndpoints(context.WithoutCancel(ctx), ref, ids, knownEndpoints)
	return endpoints, errors.Join(cancelErr, loadErr)
}

// removeCancelledSubagentBindingLocked closes durable source/recipient admission
// without deleting the Task or claiming that its producer has exited.
func (r *Runtime) removeCancelledSubagentBindingLocked(ctx context.Context, ref session.SessionRef, binding session.ParticipantBinding) error {
	lifecycle, ok := r.sessions.(session.ParticipantLifecycleService)
	if !ok {
		return fmt.Errorf("participant lifecycle store does not support atomic cancellation")
	}
	for range subagentParticipantAttachAttempts {
		active, err := r.sessions.Session(ctx, ref)
		if err != nil {
			return err
		}
		current, exists := participantBinding(active, binding.ID)
		if !exists || current.DelegationID != binding.DelegationID || current.AttachmentGeneration != binding.AttachmentGeneration {
			return nil
		}
		_, _, err = lifecycle.RemoveParticipantWithEvent(ctx, session.RemoveParticipantWithEventRequest{
			SessionRef: ref, ExpectedRevision: &active.Revision,
			MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeParticipant),
			ParticipantID: binding.ID, ExpectedDelegationID: stringPointer(binding.DelegationID),
			Event: participantLifecycleEvent(active, binding, "detached", r.now()),
		})
		if errors.Is(err, session.ErrRevisionConflict) {
			continue
		}
		return err
	}
	return session.ErrRevisionConflict
}

func (r *Runtime) sessionSubagentEndpoints(ctx context.Context, ref session.SessionRef, ids map[string]struct{}, known map[string]agent.ChildEndpointRef) ([]agent.ChildEndpointRef, error) {
	entries, loadErr := r.tasks.store.ListSession(ctx, ref)
	for _, entry := range entries {
		if entry == nil || entry.Kind != taskapi.KindSubagent || entry.Session.SessionID != ref.SessionID {
			continue
		}
		if _, selected := ids[entry.TaskID]; !selected {
			continue
		}
		task := r.tasks.rehydrateSubagentTask(entry)
		if task.anchor.SessionID == "" || task.anchor.AgentID == "" {
			continue
		}
		known[entry.TaskID] = agent.NormalizeChildEndpointRef(agent.ChildEndpointRef{
			ParticipantID: task.anchor.AgentID, SessionID: task.anchor.SessionID, EndpointKey: entry.TaskID,
			Role: subagentParticipantRole(task), Placement: task.target.Placement,
		})
	}
	endpoints := make([]agent.ChildEndpointRef, 0, len(known))
	for _, endpoint := range known {
		endpoints = append(endpoints, endpoint)
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].EndpointKey < endpoints[j].EndpointKey })
	return endpoints, loadErr
}

func (tm *taskRuntime) cancelSessionSubagent(ctx context.Context, ref session.SessionRef, taskID string) error {
	release, err := tm.waitForTaskOperationClaim(ctx, ref, taskID)
	if err != nil {
		return err
	}
	entry, err := tm.store.Get(ctx, taskID)
	if err != nil || entry == nil {
		release()
		return err
	}
	orphan := entry.Running && !tm.hasActiveSubagentTask(entry)
	task, err := tm.lookupSubagentCanonical(ctx, ref, taskID)
	if err != nil {
		release()
		return err
	}
	task.mu.Lock()
	if orphan {
		task.applyInterruptedLocked(subagentInterruptedSummary(entry))
	}
	turnSeq := max(task.turnSeq, 1)
	pending := task.pendingInput != nil && !task.pendingInput.settled.Load()
	if pending {
		turnSeq = max(turnSeq, task.pendingInput.turnSeq)
	}
	running := task.running || pending
	phase := subagentCancelPhase(taskStringValue(task.metadata[subagentCancelPhaseKey]))
	runner, anchor := task.runner, delegation.CloneAnchor(task.anchor)
	task.mu.Unlock()
	if anchor.SessionID == "" {
		release()
		return nil
	}
	if !running {
		err = tm.markSubagentSessionCancellation(ctx, task, turnSeq)
		release()
		return err
	}
	if runner == nil {
		release()
		return fmt.Errorf("live subagent cancellation requires a runner for Task %q", taskID)
	}
	claim := phase == subagentCancelPhaseNone
	if claim {
		_, err = tm.persistSubagentCancelPhase(ctx, task, turnSeq, subagentCancelPhaseClaimed, "Session cancellation requested", nil, false)
	}
	release()
	if err != nil {
		return err
	}
	// Neither Cancel nor Wait may hold the Task operation claim: a producer can
	// synchronously publish its durable completion before either call returns.
	var effectErr error
	if claim {
		effectErr = runner.Cancel(ctx, anchor)
	}
	result, waitErr := runner.Wait(ctx, anchor, int(taskCancelWait.Milliseconds()))
	release, err = tm.waitForTaskOperationClaim(ctx, ref, taskID)
	if err != nil {
		return errors.Join(effectErr, waitErr, err)
	}
	defer release()
	task, err = tm.lookupSubagentCanonical(ctx, ref, taskID)
	if err != nil {
		return errors.Join(effectErr, waitErr, err)
	}
	task.mu.Lock()
	settled := !task.running && (task.pendingInput == nil || task.pendingInput.settled.Load())
	task.mu.Unlock()
	if settled {
		return errors.Join(effectErr, waitErr)
	}
	if waitErr != nil {
		_, err = tm.persistSubagentCancelPhase(ctx, task, turnSeq, subagentCancelPhaseUnknown, "remote subagent cancellation outcome could not be confirmed", nil, false)
	} else if subagentCancelResultTerminal(result) && !result.Running {
		_, err = tm.persistSubagentCancelPhase(ctx, task, turnSeq, subagentCancelPhaseCompleted, "", &result, true)
	} else if subagentCancelResultPending(result) {
		next := phase
		if claim {
			next = subagentCancelPhaseApplied
		}
		if effectErr != nil || next == subagentCancelPhaseClaimed {
			next = subagentCancelPhaseUnknown
		}
		_, err = tm.persistSubagentCancelPhase(ctx, task, turnSeq, next, "remote subagent cancellation is not yet terminal", &result, false)
	} else {
		err = fmt.Errorf("subagent cancellation returned invalid state %q", result.State)
	}
	return errors.Join(effectErr, waitErr, err)
}

// A terminal Task still needs the cancellation journal marker: a post_spawn
// record must never roll forward and reattach its revoked participant on retry.
// Rehydration without a runner uses the existing interrupted recovery outcome.
func (tm *taskRuntime) markSubagentSessionCancellation(ctx context.Context, task *subagentTask, turnSeq int64) error {
	task.mu.Lock()
	entry := task.entrySnapshot(tm.runtime.now())
	entry.Metadata[subagentCancelPhaseKey] = string(subagentCancelPhaseCompleted)
	entry.Metadata[subagentCancelTurnSeqKey] = turnSeq
	entry.Spec[subagentCancelPhaseKey] = string(subagentCancelPhaseCompleted)
	entry.Spec[subagentCancelTurnSeqKey] = turnSeq
	task.mu.Unlock()
	if err := tm.persistSubagentCancellationEntry(ctx, entry); err != nil {
		return err
	}
	task.mu.Lock()
	task.metadata = session.CloneState(entry.Metadata)
	task.mu.Unlock()
	return nil
}
