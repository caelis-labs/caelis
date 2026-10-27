package runtime

import (
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// awaitParticipantInputLocked pins the already validated source/recipient
// bindings while waiting without the topology lock. participantMu must be held
// on entry and is held again on return. Detach rejects only the pinned bindings;
// other topology changes and other recipients remain independent.
func (r *Runtime) awaitParticipantInputLocked(ref session.SessionRef, bindings []session.ParticipantBinding, submit func() error) error {
	keys := make(map[participantPromptKey]struct{}, len(bindings))
	for _, binding := range bindings {
		if binding.ID != "" {
			keys[participantPromptKey{sessionID: ref.SessionID, participantID: binding.ID}] = struct{}{}
		}
	}
	if r.participantInputPins == nil {
		r.participantInputPins = map[participantPromptKey]int{}
	}
	for key := range keys {
		r.participantInputPins[key]++
	}
	r.participantMu.Unlock()
	defer func() {
		r.participantMu.Lock()
		for key := range keys {
			r.participantInputPins[key]--
			if r.participantInputPins[key] == 0 {
				delete(r.participantInputPins, key)
			}
		}
	}()
	return submit()
}

// participantInputMutationErrorLocked fences removal/replacement for the whole
// admission wait, including its uncertain outcome, without blocking topology.
func (r *Runtime) participantInputMutationErrorLocked(ref session.SessionRef, id string) error {
	if r.participantInputPins[participantPromptKey{sessionID: ref.SessionID, participantID: id}] > 0 {
		return errorcode.New(errorcode.Conflict, "Participant has an input admission in progress")
	}
	return nil
}
