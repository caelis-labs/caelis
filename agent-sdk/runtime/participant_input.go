package runtime

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type participantInputReservation struct {
	ref    session.SessionRef
	keys   map[participantPromptKey]struct{}
	cancel context.CancelFunc
	done   chan struct{}
	// taskID identifies the child endpoint an admission may create or activate.
	taskID string
	// A successful asynchronous Spawn transfers context cleanup to its producer.
	retainProducerContext bool
}

// reserveParticipantInputLocked gives each admission a revocable context under
// the same lock that validates its bindings. participantMu must be held.
func (r *Runtime) reserveParticipantInputLocked(ctx context.Context, ref session.SessionRef, bindings []session.ParticipantBinding) (context.Context, *participantInputReservation) {
	ctx, cancel := context.WithCancel(ctx)
	reservation := &participantInputReservation{ref: ref, keys: map[participantPromptKey]struct{}{}, cancel: cancel, done: make(chan struct{})}
	for _, binding := range bindings {
		if binding.ID != "" {
			reservation.keys[participantPromptKey{sessionID: ref.SessionID, participantID: binding.ID}] = struct{}{}
		}
	}
	if r.participantInputPins == nil {
		r.participantInputPins = map[participantPromptKey]int{}
	}
	if r.participantInputs == nil {
		r.participantInputs = map[*participantInputReservation]struct{}{}
	}
	for key := range reservation.keys {
		r.participantInputPins[key]++
	}
	r.participantInputs[reservation] = struct{}{}
	return ctx, reservation
}

func (r *Runtime) releaseParticipantInputPinsLocked(reservation *participantInputReservation) {
	for key := range reservation.keys {
		r.participantInputPins[key]--
		if r.participantInputPins[key] == 0 {
			delete(r.participantInputPins, key)
		}
	}
	reservation.keys = nil
}

func (r *Runtime) finishParticipantInputLocked(reservation *participantInputReservation) {
	r.releaseParticipantInputPinsLocked(reservation)
	delete(r.participantInputs, reservation)
	if !reservation.retainProducerContext {
		reservation.cancel()
	}
	close(reservation.done)
}

// awaitParticipantInputLocked pins validated bindings while waiting without the
// topology lock. Session cancellation revokes the callback context and its pins;
// ordinary detach still rejects pinned bindings. participantMu is held on entry
// and again on return.
func (r *Runtime) awaitParticipantInputLocked(ctx context.Context, ref session.SessionRef, bindings []session.ParticipantBinding, taskID string, submit func(context.Context) error) error {
	ctx, reservation := r.reserveParticipantInputLocked(ctx, ref, bindings)
	reservation.taskID = taskID
	r.participantMu.Unlock()
	defer func() {
		r.participantMu.Lock()
		r.finishParticipantInputLocked(reservation)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return submit(ctx)
}

// participantInputMutationErrorLocked fences removal/replacement for the whole
// admission wait, including its uncertain outcome, without blocking topology.
func (r *Runtime) participantInputMutationErrorLocked(ref session.SessionRef, id string) error {
	if r.participantInputPins[participantPromptKey{sessionID: ref.SessionID, participantID: id}] > 0 {
		return errorcode.New(errorcode.Conflict, "Participant has an input admission in progress")
	}
	return nil
}
