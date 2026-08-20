package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

const (
	childActivityJournalMaxEvents = 256
	childActivityJournalMaxBytes  = 1 << 20
)

// childSlot is the stable process-local owner for one durable child endpoint.
// Its operation mutex survives ACP transport replacement. The state mutex is
// deliberately short-lived: notification callbacks never wait on an RPC or a
// Task observer while holding it.
type childSlot struct {
	opMu sync.Mutex

	mu                 sync.Mutex
	target             agent.ChildEndpointRef
	targetReady        bool
	run                *childRun
	activityID         string
	activityInitial    bool
	cursor             uint64
	observer           agent.ChildActivityObserver
	journal            []*childActivityJournalItem
	journalBytes       int
	delivering         bool
	overflowed         bool
	terminalActivity   string
	steeringActive     bool
	steeringFrames     []stream.Frame
	outputQuarantined  bool
	setupActive        bool
	setupFrames        []stream.Frame
	terminalPending    chan struct{}
	activeInputCancel  context.CancelFunc
	promptDispatchDone chan struct{}
	promptCancel       context.CancelFunc

	deliveryMu sync.Mutex
	ingressMu  sync.Mutex
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

// reservePromptDispatch closes child-input admission around one prompt frame.
// The initial/idle caller establishes the first reservation under opMu; an
// auth-required response may establish a later reservation from the response
// owner before it waits to reacquire opMu for the authenticated retry.
func (s *childSlot) reservePromptDispatch(cancel context.CancelFunc) (chan struct{}, bool) {
	return s.transitionPromptDispatch(nil, cancel)
}

// transitionPromptDispatch atomically converts the dispatch reservation that
// guarded a just-written prompt into the reservation for its authenticated
// retry. When the original reservation already completed, nil is still a
// valid predecessor; any different live reservation is a conflicting owner.
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

type childActivityJournalItem struct {
	event agent.ChildActivityEvent
	size  int
	done  chan struct{}
	once  sync.Once
}

type childActivityCheckpoint struct {
	activityID        string
	activityInitial   bool
	terminalActivity  string
	overflowed        bool
	outputQuarantined bool
	setupActive       bool
	setupFrames       []stream.Frame
}

func (item *childActivityJournalItem) acknowledge() {
	if item == nil {
		return
	}
	item.once.Do(func() { close(item.done) })
}

func newChildSlot(target agent.ChildEndpointRef, run *childRun) *childSlot {
	target = agent.NormalizeChildEndpointRef(target)
	slot := &childSlot{
		target: target, targetReady: true, run: run,
	}
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
	s.mu.Lock()
	s.run = run
	s.setupActive = true
	s.setupFrames = nil
	s.outputQuarantined = false
	s.mu.Unlock()
}

func (s *childSlot) finalizeTarget(target agent.ChildEndpointRef) error {
	if s == nil {
		return errorcode.New(errorcode.FailedPrecondition, "internal/acpagentbridge/subagent: child slot is unavailable")
	}
	target = agent.NormalizeChildEndpointRef(target)
	if err := validateChildEndpointRef(target); err != nil {
		return err
	}
	s.ingressMu.Lock()
	defer s.ingressMu.Unlock()
	s.mu.Lock()
	current := agent.NormalizeChildEndpointRef(s.target)
	if (current.ParticipantID != "" && current.ParticipantID != target.ParticipantID) ||
		(current.EndpointKey != "" && current.EndpointKey != target.EndpointKey) ||
		(current.Role != "" && current.Role != target.Role) ||
		(current.Placement.Kind != "" && !reflect.DeepEqual(current.Placement, target.Placement)) ||
		(current.SessionID != "" && current.SessionID != target.SessionID) {
		s.mu.Unlock()
		return childSlotTargetError(target)
	}
	s.target = target
	s.targetReady = true
	for _, item := range s.journal {
		item.event.Target = agent.NormalizeChildEndpointRef(target)
	}
	s.mu.Unlock()
	s.triggerDelivery()
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
	return s.run == run && s.setupActive
}

func (s *childSlot) matchesTarget(target agent.ChildEndpointRef) bool {
	if s == nil {
		return false
	}
	want := agent.NormalizeChildEndpointRef(target)
	s.mu.Lock()
	have := agent.NormalizeChildEndpointRef(s.target)
	s.mu.Unlock()
	return have.ParticipantID == want.ParticipantID &&
		have.SessionID == want.SessionID &&
		have.EndpointKey == want.EndpointKey &&
		have.Role == want.Role &&
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
	s.overflowed = false
	s.steeringActive = false
	s.steeringFrames = nil
	s.outputQuarantined = false
	frames := append([]stream.Frame(nil), s.setupFrames...)
	s.setupActive = false
	s.setupFrames = nil
	s.activeInputCancel = nil
	activityID = s.activityID
	s.mu.Unlock()
	if activityID == "" {
		return
	}
	for _, frame := range frames {
		cloned := stream.CloneFrame(frame)
		s.appendEvent(agent.ChildActivityEvent{ActivityID: activityID, Initial: initial, Frame: &cloned})
	}
}

func (s *childSlot) activityCheckpoint() childActivityCheckpoint {
	if s == nil {
		return childActivityCheckpoint{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return childActivityCheckpoint{
		activityID: s.activityID, activityInitial: s.activityInitial,
		terminalActivity: s.terminalActivity, overflowed: s.overflowed,
		outputQuarantined: s.outputQuarantined, setupActive: s.setupActive,
		setupFrames: append([]stream.Frame(nil), s.setupFrames...),
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
	s.overflowed = checkpoint.overflowed
	s.steeringActive = false
	s.steeringFrames = nil
	s.outputQuarantined = checkpoint.outputQuarantined
	s.setupActive = checkpoint.setupActive
	s.setupFrames = append([]stream.Frame(nil), checkpoint.setupFrames...)
	s.activeInputCancel = nil
	s.mu.Unlock()
}

func (s *childSlot) bindObserver(afterCursor uint64, observer agent.ChildActivityObserver) {
	if s == nil {
		return
	}
	// Wait for an already selected observer callback to finish before swapping.
	// Output ingestion remains free to append to the journal while this waits.
	s.deliveryMu.Lock()
	s.mu.Lock()
	s.observer = observer
	s.cursor = max(s.cursor, afterCursor)
	for len(s.journal) > 0 && s.journal[0].event.Cursor <= afterCursor {
		item := s.journal[0]
		s.journal = s.journal[1:]
		s.journalBytes -= item.size
		item.acknowledge()
	}
	s.mu.Unlock()
	s.deliveryMu.Unlock()
	s.triggerDelivery()
}

func (s *childSlot) publishFrame(frame stream.Frame) {
	s.publishRunFrame(nil, frame)
}

func (s *childSlot) publishRunFrame(run *childRun, frame stream.Frame) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if run != nil && s.run != run {
		s.mu.Unlock()
		return
	}
	if s.outputQuarantined {
		s.mu.Unlock()
		return
	}
	if s.setupActive {
		s.setupFrames = append(s.setupFrames, stream.CloneFrame(frame))
		s.mu.Unlock()
		return
	}
	if s.steeringActive {
		s.steeringFrames = append(s.steeringFrames, stream.CloneFrame(frame))
		s.mu.Unlock()
		return
	}
	activityID := s.activityID
	initial := s.activityInitial
	s.mu.Unlock()
	if activityID == "" {
		return
	}
	s.appendEvent(agent.ChildActivityEvent{ActivityID: activityID, Initial: initial, Frame: &frame})
}

func (s *childSlot) settleSteeringFrames(release bool) {
	if s == nil {
		return
	}
	s.ingressMu.Lock()
	defer s.ingressMu.Unlock()
	s.mu.Lock()
	frames := append([]stream.Frame(nil), s.steeringFrames...)
	s.steeringFrames = nil
	s.steeringActive = false
	s.outputQuarantined = !release
	s.activeInputCancel = nil
	activityID := s.activityID
	initial := s.activityInitial
	s.mu.Unlock()
	if !release || activityID == "" {
		return
	}
	for _, frame := range frames {
		cloned := stream.CloneFrame(frame)
		s.appendEvent(agent.ChildActivityEvent{ActivityID: activityID, Initial: initial, Frame: &cloned})
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
		s.steeringActive = false
		s.steeringFrames = nil
		s.setupActive = false
		s.setupFrames = nil
		s.activeInputCancel = nil
	}
	s.mu.Unlock()
}

func (s *childSlot) beginSteering(cancel context.CancelFunc) bool {
	if s == nil {
		return false
	}
	s.ingressMu.Lock()
	defer s.ingressMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.steeringActive || s.activityID == "" {
		return false
	}
	s.steeringActive = true
	s.steeringFrames = nil
	s.activeInputCancel = cancel
	return true
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

func (s *childSlot) publishResult(result delegation.Result) <-chan struct{} {
	done := make(chan struct{})
	if s == nil {
		close(done)
		return done
	}
	s.mu.Lock()
	activityID := s.activityID
	initial := s.activityInitial
	if activityID == "" || s.terminalActivity == activityID {
		s.mu.Unlock()
		close(done)
		return done
	}
	s.terminalActivity = activityID
	s.mu.Unlock()
	item := s.appendEvent(agent.ChildActivityEvent{ActivityID: activityID, Initial: initial, Result: &result})
	if item == nil {
		close(done)
		return done
	}
	return item.done
}

func (s *childSlot) appendEvent(raw agent.ChildActivityEvent) *childActivityJournalItem {
	if s == nil {
		return nil
	}
	event := agent.CloneChildActivityEvent(raw)
	s.mu.Lock()
	if s.overflowed {
		if len(s.journal) == 0 {
			s.mu.Unlock()
			return nil
		}
		item := s.journal[len(s.journal)-1]
		s.mu.Unlock()
		return item
	}
	s.cursor++
	event.Cursor = s.cursor
	event.Target = agent.NormalizeChildEndpointRef(s.target)
	size := childActivityEventSize(event)
	item := &childActivityJournalItem{event: event, size: size, done: make(chan struct{})}
	s.journal = append(s.journal, item)
	s.journalBytes += size
	overflow := len(s.journal) > childActivityJournalMaxEvents || s.journalBytes > childActivityJournalMaxBytes
	if overflow && !s.overflowed {
		s.overflowed = true
		for _, dropped := range s.journal {
			dropped.acknowledge()
		}
		s.cursor++
		unknown := delegation.Result{
			TaskID: strings.TrimSpace(s.target.EndpointKey), State: delegation.StateUnknownOutcome,
			Error: "child activity observation gap", OutputPreview: "child activity observation gap",
			UpdatedAt: time.Now(),
		}
		gap := agent.ChildActivityEvent{
			Target: agent.NormalizeChildEndpointRef(s.target), ActivityID: event.ActivityID,
			Cursor: s.cursor, Initial: event.Initial, Gap: true, Result: &unknown,
		}
		gapItem := &childActivityJournalItem{event: gap, size: childActivityEventSize(gap), done: make(chan struct{})}
		s.journal = []*childActivityJournalItem{gapItem}
		s.journalBytes = gapItem.size
		s.terminalActivity = event.ActivityID
		item = gapItem
	}
	s.mu.Unlock()
	s.triggerDelivery()
	if overflow {
		go s.isolateOverflow()
	}
	return item
}

func childActivityEventSize(event agent.ChildActivityEvent) int {
	encoded, err := json.Marshal(event)
	if err != nil {
		return 1
	}
	return max(len(encoded), 1)
}

func (s *childSlot) isolateOverflow() {
	run, cancel := s.revokeActiveInput()
	if cancel != nil {
		cancel()
	}
	if run == nil {
		return
	}
	run.mu.RLock()
	acpClient := run.client
	runCancel := run.cancel
	run.mu.RUnlock()
	if runCancel != nil {
		runCancel()
	}
	if acpClient != nil {
		_ = acpClient.Close(context.Background())
	}
}

func (s *childSlot) triggerDelivery() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.delivering || !s.targetReady || s.observer == nil || len(s.journal) == 0 {
		s.mu.Unlock()
		return
	}
	s.delivering = true
	s.mu.Unlock()
	go s.deliverJournal()
}

func (s *childSlot) deliverJournal() {
	defer func() {
		s.mu.Lock()
		s.delivering = false
		retry := s.observer != nil && len(s.journal) > 0
		s.mu.Unlock()
		if retry {
			time.AfterFunc(50*time.Millisecond, s.triggerDelivery)
		}
	}()
	for {
		s.deliveryMu.Lock()
		s.mu.Lock()
		if !s.targetReady || s.observer == nil || len(s.journal) == 0 {
			s.mu.Unlock()
			s.deliveryMu.Unlock()
			return
		}
		observer := s.observer
		item := s.journal[0]
		event := agent.CloneChildActivityEvent(item.event)
		s.mu.Unlock()
		err := observer.ObserveChildActivity(context.Background(), event)
		s.deliveryMu.Unlock()
		if err != nil {
			return
		}
		s.mu.Lock()
		if len(s.journal) > 0 && s.journal[0] == item {
			s.journal = s.journal[1:]
			s.journalBytes -= item.size
			item.acknowledge()
		}
		s.mu.Unlock()
	}
}

type compatibilityActivityObserver struct {
	run *childRun
}

func (o compatibilityActivityObserver) ObserveChildActivity(_ context.Context, event agent.ChildActivityEvent) error {
	if o.run == nil {
		return nil
	}
	if event.Frame != nil {
		o.run.mu.RLock()
		sink := o.run.sink
		o.run.mu.RUnlock()
		if sink != nil {
			sink.PublishStream(stream.CloneFrame(*event.Frame))
		}
	}
	if event.Result != nil {
		o.run.mu.RLock()
		completion := o.run.completion
		o.run.mu.RUnlock()
		if completion != nil {
			completion.PublishSubagentCompletion(delegation.CloneResult(*event.Result))
		}
	}
	return nil
}

func validateChildEndpointRef(target agent.ChildEndpointRef) error {
	target = agent.NormalizeChildEndpointRef(target)
	if target.ParticipantID == "" || target.SessionID == "" || target.EndpointKey == "" || target.Role == "" {
		return errorcode.New(errorcode.InvalidArgument, "internal/acpagentbridge/subagent: child endpoint identity is incomplete")
	}
	if err := placementValidationError(target); err != nil {
		return err
	}
	return nil
}

func placementValidationError(target agent.ChildEndpointRef) error {
	if target.Placement.Kind == "" {
		return errorcode.New(errorcode.InvalidArgument, "internal/acpagentbridge/subagent: child endpoint placement is required")
	}
	return nil
}

func childSlotTargetError(target agent.ChildEndpointRef) error {
	return errorcode.New(errorcode.Conflict, fmt.Sprintf(
		"internal/acpagentbridge/subagent: child endpoint %q no longer matches its durable binding",
		strings.TrimSpace(target.EndpointKey),
	))
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
