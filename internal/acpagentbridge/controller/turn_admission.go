package controller

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *controllerRun) beginTurn(req controller.TurnRequest, handle *turnHandle) error {
	if r == nil {
		return fmt.Errorf("internal/acpagentbridge/controller: controller run is unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.turnAdmissionClosed {
		return fmt.Errorf("%w for session %q", controller.ErrNotActive, strings.TrimSpace(r.parentSessionID))
	}
	if r.handle != nil || strings.TrimSpace(r.turnID) != "" {
		return fmt.Errorf("internal/acpagentbridge/controller: controller for session %q already has a turn in progress", strings.TrimSpace(r.parentSessionID))
	}
	r.turnID = strings.TrimSpace(req.TurnID)
	r.turnSession = session.CloneSession(req.Session)
	r.turnStream = req.Stream
	r.turnMode = strings.TrimSpace(req.Mode)
	if req.ContextSyncSeq > r.binding.ContextSyncSeq {
		r.binding.ContextSyncSeq = req.ContextSyncSeq
	}
	r.approvalRequester = req.ApprovalRequester
	r.handle = handle
	r.events = nil
	return nil
}

func (r *controllerRun) finishTurn(owner *turnHandle) ([]*session.Event, bool) {
	if r == nil || owner == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handle != owner {
		return nil, false
	}
	buffered := make([]*session.Event, 0, len(r.events))
	for _, event := range r.events {
		buffered = append(buffered, session.CloneEvent(event))
	}
	stream := r.turnStream
	r.turnID = ""
	r.turnSession = session.Session{}
	r.turnStream = false
	r.turnMode = ""
	r.approvalRequester = nil
	r.handle = nil
	r.events = nil
	return buffered, stream
}

// closeTurnAdmission permanently rejects new turns on this run and cancels the
// currently admitted producer. The producer remains responsible for finishing
// its handle and releasing its own buffered state.
func (r *controllerRun) closeTurnAdmission() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.turnAdmissionClosed = true
	handle := r.handle
	r.mu.Unlock()
	if handle != nil {
		handle.Cancel()
	}
}

func (r *participantRun) beginPrompt(req controller.ParticipantPromptRequest, handle *turnHandle) error {
	if r == nil {
		return fmt.Errorf("internal/acpagentbridge/controller: participant run is unavailable")
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.promptAdmissionClosed {
		return fmt.Errorf("%w: participant %q is detached", controller.ErrNotActive, strings.TrimSpace(r.id))
	}
	if r.handle != nil || strings.TrimSpace(r.turnID) != "" {
		return fmt.Errorf("internal/acpagentbridge/controller: participant %q already has a prompt in progress", r.id)
	}
	r.turnID = firstNonEmpty(strings.TrimSpace(req.TurnID), strings.TrimSpace(req.ParticipantID), r.id)
	r.turnSession = session.CloneSession(req.Session)
	r.turnStream = req.Stream
	r.turnMode = strings.TrimSpace(req.Mode)
	r.approvalRequester = req.ApprovalRequester
	r.handle = handle
	r.events = nil
	return nil
}

func (r *participantRun) finishPrompt(owner *turnHandle) ([]*session.Event, bool) {
	if r == nil || owner == nil {
		return nil, false
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handle != owner {
		return nil, false
	}
	buffered := make([]*session.Event, 0, len(r.events))
	for _, event := range r.events {
		buffered = append(buffered, session.CloneEvent(event))
	}
	stream := r.turnStream
	r.turnID = ""
	r.turnSession = session.Session{}
	r.turnStream = false
	r.turnMode = ""
	r.approvalRequester = nil
	r.handle = nil
	r.events = nil
	return buffered, stream
}

// closePromptAdmission permanently rejects new prompts on a detached
// participant and cancels the currently admitted producer. The producer owns
// finishing its handle and releasing buffered state.
func (r *participantRun) closePromptAdmission() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.promptAdmissionClosed = true
	cancelSteering := r.steeringCancel
	r.mu.Unlock()
	if cancelSteering != nil {
		cancelSteering()
	}
	r.operationMu.Lock()
	r.mu.Lock()
	handle := r.handle
	r.mu.Unlock()
	if handle != nil {
		handle.Cancel()
	}
	r.operationMu.Unlock()
}
