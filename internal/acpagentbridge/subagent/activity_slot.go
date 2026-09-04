package subagent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
)

// childSlot is the stable process-local execution owner for one child
// endpoint. It serializes endpoint effects, but deliberately retains no output
// journal: normalized output is synchronously handed to the Control-bound
// observer and durable completion is handed to the Task completion sink.
type childSlot struct {
	opMu sync.Mutex

	mu                 sync.Mutex
	target             agent.ChildEndpointRef
	targetReady        bool
	run                *childRun
	activityID         string
	activityInitial    bool
	terminalActivity   string
	inputActive        bool
	inputProjectionSeq uint64
	outputQuarantined  bool
	setupActive        bool
	terminalPending    chan struct{}
	activeInputCancel  context.CancelFunc
	promptDispatchDone chan struct{}
	promptCancel       context.CancelFunc

	// ingressMu is the one ordering point shared by ACP updates and terminal
	// completion. No file or network bytes are retained behind it.
	ingressMu sync.Mutex
}

func (s *childSlot) pendingPromptDispatch() <-chan struct{} {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.promptDispatchDone
}

func (s *childSlot) pendingTerminalSettlement() <-chan struct{} {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminalPending
}

func (s *childSlot) beginTerminalSettlement(done chan struct{}) {
	if s == nil || done == nil {
		return
	}
	s.mu.Lock()
	s.terminalPending = done
	s.mu.Unlock()
}

func (s *childSlot) finishTerminalSettlement(done chan struct{}) {
	if s == nil || done == nil {
		return
	}
	s.mu.Lock()
	if s.terminalPending == done {
		s.terminalPending = nil
	}
	s.mu.Unlock()
}

func (s *childSlot) beginPromptDispatch(cancel context.CancelFunc) chan struct{} {
	done, _ := s.reservePromptDispatch(cancel)
	return done
}

func (s *childSlot) reservePromptDispatch(cancel context.CancelFunc) (chan struct{}, bool) {
	return s.transitionPromptDispatch(nil, cancel)
}

func (s *childSlot) transitionPromptDispatch(expected chan struct{}, cancel context.CancelFunc) (chan struct{}, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.promptDispatchDone
	if current != nil && current != expected {
		return nil, false
	}
	done := make(chan struct{})
	s.promptDispatchDone = done
	s.promptCancel = cancel
	if current != nil {
		close(current)
	}
	return done, true
}

func (s *childSlot) finishPromptDispatch(done chan struct{}) {
	if s == nil || done == nil {
		return
	}
	s.mu.Lock()
	if s.promptDispatchDone == done {
		s.promptDispatchDone = nil
		s.promptCancel = nil
		close(done)
	}
	s.mu.Unlock()
}

type childActivityCheckpoint struct {
	activityID        string
	activityInitial   bool
	terminalActivity  string
	outputQuarantined bool
	setupActive       bool
}

func newChildSlot(target agent.ChildEndpointRef, run *childRun) *childSlot {
	target = agent.NormalizeChildEndpointRef(target)
	slot := &childSlot{target: target, targetReady: true, run: run}
	if run != nil {
		run.installChildSlot(slot)
	}
	return slot
}

func newPendingChildSlot(target agent.ChildEndpointRef, run *childRun) *childSlot {
	slot := newChildSlot(target, run)
	slot.targetReady = false
	return slot
}

func (s *childSlot) beginSetup(run *childRun) {
	if s == nil || run == nil {
		return
	}
	s.ingressMu.Lock()
	defer s.ingressMu.Unlock()
	run.mu.RLock()
	activityID := strings.TrimSpace(run.spawn.ActivityID)
	run.mu.RUnlock()
	s.mu.Lock()
	s.run = run
	s.activityID = activityID
	s.activityInitial = true
	s.setupActive = true
	s.outputQuarantined = false
	s.mu.Unlock()
}

func (s *childSlot) finalizeTarget(target agent.ChildEndpointRef) error {
	if s == nil {
		return errorcode.New(errorcode.FailedPrecondition, "Target Agent state is unavailable")
	}
	target = agent.NormalizeChildEndpointRef(target)
	if err := validateChildEndpointRef(target); err != nil {
		return err
	}
	s.ingressMu.Lock()
	defer s.ingressMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	current := agent.NormalizeChildEndpointRef(s.target)
	if (current.ParticipantID != "" && current.ParticipantID != target.ParticipantID) ||
		(current.EndpointKey != "" && current.EndpointKey != target.EndpointKey) ||
		(current.Role != "" && current.Role != target.Role) ||
		(current.Placement.Kind != "" && !reflect.DeepEqual(current.Placement, target.Placement)) ||
		(current.SessionID != "" && current.SessionID != target.SessionID) {
		return childSlotTargetError(target)
	}
	s.target = target
	s.targetReady = true
	return nil
}

func (s *childSlot) currentRun() *childRun {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run
}

func (s *childSlot) acceptsSetupOutput(run *childRun) bool {
	if s == nil || run == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run == run && s.setupActive && !s.outputQuarantined
}

func (s *childSlot) matchesTarget(target agent.ChildEndpointRef) bool {
	if s == nil {
		return false
	}
	want := agent.NormalizeChildEndpointRef(target)
	s.mu.Lock()
	have := agent.NormalizeChildEndpointRef(s.target)
	s.mu.Unlock()
	return have.ParticipantID == want.ParticipantID && have.SessionID == want.SessionID &&
		have.EndpointKey == want.EndpointKey && have.Role == want.Role &&
		reflect.DeepEqual(have.Placement, want.Placement)
}

func (s *childSlot) beginActivity(activityID string, run *childRun) {
	s.beginActivityKind(activityID, run, false)
}

func (s *childSlot) beginInitialActivity(activityID string, run *childRun) {
	s.beginActivityKind(activityID, run, true)
}

func (s *childSlot) beginActivityKind(activityID string, run *childRun, initial bool) {
	if s == nil || run == nil {
		return
	}
	s.ingressMu.Lock()
	defer s.ingressMu.Unlock()
	s.mu.Lock()
	s.run = run
	s.activityID = strings.TrimSpace(activityID)
	s.activityInitial = initial
	s.terminalActivity = ""
	s.inputActive = false
	s.outputQuarantined = false
	s.setupActive = false
	s.activeInputCancel = nil
	s.mu.Unlock()
}

func (s *childSlot) activityCheckpoint() childActivityCheckpoint {
	if s == nil {
		return childActivityCheckpoint{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return childActivityCheckpoint{
		activityID: s.activityID, activityInitial: s.activityInitial,
		terminalActivity: s.terminalActivity, outputQuarantined: s.outputQuarantined,
		setupActive: s.setupActive,
	}
}

func (s *childSlot) restoreActivity(checkpoint childActivityCheckpoint, run *childRun) {
	if s == nil {
		return
	}
	s.ingressMu.Lock()
	defer s.ingressMu.Unlock()
	s.mu.Lock()
	s.run = run
	s.activityID = checkpoint.activityID
	s.activityInitial = checkpoint.activityInitial
	s.terminalActivity = checkpoint.terminalActivity
	s.inputActive = false
	s.outputQuarantined = checkpoint.outputQuarantined
	s.setupActive = checkpoint.setupActive
	s.activeInputCancel = nil
	s.mu.Unlock()
}

// publishRunOutputLocked publishes one normalized event while ingressMu is held.
// ACP update normalization and observer delivery share this lock so terminal
// completion cannot overtake the last update.
func (s *childSlot) publishRunOutputLocked(run *childRun, event output.Event) {
	if s == nil {
		return
	}
	s.mu.Lock()
	current := s.run
	activityID := s.activityID
	rejected := (run != nil && current != run) || s.outputQuarantined || activityID == "" ||
		(s.terminalActivity != "" && s.terminalActivity == activityID)
	s.mu.Unlock()
	if rejected || current == nil {
		return
	}
	current.mu.RLock()
	observer := current.output
	current.mu.RUnlock()
	if observer == nil {
		return
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now()
	}
	_ = observer.ObserveTaskOutput(context.Background(), event)
}

func (s *childSlot) settleInput(run *childRun, release bool, acceptedInput *output.Event) {
	if s == nil {
		return
	}
	s.ingressMu.Lock()
	defer s.ingressMu.Unlock()
	s.mu.Lock()
	s.inputActive = false
	s.outputQuarantined = !release
	s.activeInputCancel = nil
	s.mu.Unlock()
	if release && acceptedInput != nil {
		s.publishRunOutputLocked(run, *acceptedInput)
	}
}

func (s *childSlot) quarantineOutput(run *childRun) {
	if s == nil {
		return
	}
	s.ingressMu.Lock()
	defer s.ingressMu.Unlock()
	s.mu.Lock()
	if run == nil || s.run == run {
		s.outputQuarantined = true
		s.inputActive = false
		s.setupActive = false
		s.activeInputCancel = nil
	}
	s.mu.Unlock()
}

func (s *childSlot) beginInput(cancel context.CancelFunc) bool {
	if s == nil {
		return false
	}
	s.ingressMu.Lock()
	defer s.ingressMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inputActive || s.activityID == "" || s.outputQuarantined {
		return false
	}
	s.inputActive = true
	s.activeInputCancel = cancel
	return true
}

func (s *childSlot) nextInputProjectionID(activityID string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	s.inputProjectionSeq++
	sequence := s.inputProjectionSeq
	s.mu.Unlock()
	return fmt.Sprintf("agent-input:%s:%d", strings.TrimSpace(activityID), sequence)
}

func (s *childSlot) revokeActiveInput() (*childRun, context.CancelFunc) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	run := s.run
	steeringCancel := s.activeInputCancel
	promptCancel := s.promptCancel
	s.activeInputCancel = nil
	s.promptCancel = nil
	s.mu.Unlock()
	if steeringCancel == nil {
		return run, promptCancel
	}
	if promptCancel == nil {
		return run, steeringCancel
	}
	return run, func() {
		steeringCancel()
		promptCancel()
	}
}

func (s *childSlot) publishRunResult(run *childRun) <-chan struct{} {
	done := make(chan struct{})
	if s == nil || run == nil {
		close(done)
		return done
	}
	s.ingressMu.Lock()
	s.mu.Lock()
	activityID := s.activityID
	if s.run != run || activityID == "" || s.terminalActivity == activityID {
		s.mu.Unlock()
		s.ingressMu.Unlock()
		close(done)
		return done
	}
	s.terminalActivity = activityID
	s.outputQuarantined = true
	s.setupActive = false
	s.mu.Unlock()
	run.mu.RLock()
	result := childResultLocked(run)
	observer := run.output
	completion := run.completion
	run.mu.RUnlock()
	if observer != nil {
		at := result.UpdatedAt
		if at.IsZero() {
			at = time.Now()
		}
		_ = observer.ObserveTaskOutput(context.Background(), output.Event{
			OccurredAt: at, State: string(result.State), Closed: true,
		})
	}
	s.ingressMu.Unlock()
	go func() {
		if completion != nil {
			completion.PublishSubagentCompletion(delegation.CloneResult(result))
		}
		close(done)
	}()
	return done
}

func validateChildEndpointRef(target agent.ChildEndpointRef) error {
	target = agent.NormalizeChildEndpointRef(target)
	if target.ParticipantID == "" || target.SessionID == "" || target.EndpointKey == "" || target.Role == "" {
		return errorcode.New(errorcode.InvalidArgument, "Target Agent identity is incomplete")
	}
	if err := placementValidationError(target); err != nil {
		return err
	}
	return nil
}

func placementValidationError(target agent.ChildEndpointRef) error {
	if target.Placement.Kind == "" {
		return errorcode.New(errorcode.InvalidArgument, "Target Agent placement is required")
	}
	return nil
}

func childSlotTargetError(agent.ChildEndpointRef) error {
	return errorcode.New(errorcode.Conflict, "Target Agent binding changed before the message was sent.")
}

func joinChildInputUnknown(message string, err error) error {
	if err == nil {
		return errorcode.New(errorcode.UnknownOutcome, message)
	}
	return errorcode.Wrap(errorcode.UnknownOutcome, message, err)
}

func childInputProvenFailure(message string, err error) error {
	if err == nil {
		return errorcode.New(errorcode.FailedPrecondition, message)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errorcode.Wrap(errorcode.FailedPrecondition, message, err)
}
