package controlplane

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	watchdogCheckpointStatus     = "loop_watchdog_checkpoint"
	defaultWatchdogReviewTimeout = 30 * time.Second
)

var (
	_ agent.StreamProvider          = (*WatchdogRuntime)(nil)
	_ agent.LiveRunAttacher         = (*WatchdogRuntime)(nil)
	_ agent.ApprovalResolver        = (*WatchdogRuntime)(nil)
	_ agent.ParticipantControlPlane = (*WatchdogRuntime)(nil)
	_ PlacementExecutor             = (*WatchdogRuntime)(nil)
)

// ExecutePlaced delegates to the inner PlacementExecutor. Loop detection needs
// a live Runner event stream and is not reimplemented for sync Control ops.
func (r *WatchdogRuntime) ExecutePlaced(ctx context.Context, ref session.SessionRef, execute func(context.Context) error) error {
	placement, ok := r.inner.(PlacementExecutor)
	if !ok {
		return fmt.Errorf("controlplane: decorated runtime does not support placed operations")
	}
	return placement.ExecutePlaced(ctx, ref, execute)
}

// WatchdogReason identifies a loop-detector trigger.
type WatchdogReason string

const (
	// WatchdogReasonTextLoop is a pure reasoning/assistant stream-tail cycle.
	WatchdogReasonTextLoop WatchdogReason = "text_loop"
	// WatchdogReasonToolLoop is identical content-segment + tool args steps.
	WatchdogReasonToolLoop WatchdogReason = "tool_loop"
)

// WatchdogAction identifies the Control decision after detecting a loop.
type WatchdogAction string

const (
	WatchdogActionContinue   WatchdogAction = "continue"
	WatchdogActionCheckpoint WatchdogAction = "checkpoint"
	// WatchdogActionInterrupt checkpoints then cancels the live turn without a
	// separate user-confirmation bit. Used for high-confidence loops.
	WatchdogActionInterrupt WatchdogAction = "interrupt"
	// WatchdogActionCancel checkpoints, then cancels only when Confirmed.
	WatchdogActionCancel WatchdogAction = "cancel"
)

// WatchdogThresholds configures the generation-tail loop detector.
// Zero Text/Tool streaks mean "use production defaults", not "disabled".
type WatchdogThresholds struct {
	TextLoopStreak  int // default 20
	ToolLoopStreak  int // default 6
	MinContentRunes int // default 32
}

// WatchdogObservation is one immutable loop-detector snapshot for review.
type WatchdogObservation struct {
	SessionRef     session.SessionRef
	RunID          string
	ReviewSequence uint64
	ObservedAt     time.Time
	LoopStreak     int
	LoopHasTool    bool
	ContentDigest  string
	ToolDigest     string
	LoopDetail     string
	Reasons        []WatchdogReason
}

// HasReason reports whether this snapshot includes one reason.
func (o WatchdogObservation) HasReason(reason WatchdogReason) bool {
	for _, candidate := range o.Reasons {
		if candidate == reason {
			return true
		}
	}
	return false
}

// WatchdogDecision is the reviewed Control action.
type WatchdogDecision struct {
	Action    WatchdogAction
	Confirmed bool // only meaningful for WatchdogActionCancel
	Reason    string
}

// WatchdogReviewer maps observations to Control-owned actions.
type WatchdogReviewer interface {
	ReviewWatchdog(context.Context, WatchdogObservation) (WatchdogDecision, error)
}

// WatchdogReviewFunc adapts a function to WatchdogReviewer.
type WatchdogReviewFunc func(context.Context, WatchdogObservation) (WatchdogDecision, error)

func (f WatchdogReviewFunc) ReviewWatchdog(ctx context.Context, observation WatchdogObservation) (WatchdogDecision, error) {
	return f(ctx, observation)
}

// WatchdogRuntimeConfig configures the Control-owned generation-tail loop watchdog.
type WatchdogRuntimeConfig struct {
	Runtime       agent.Runtime
	Sessions      session.Service
	Lifecycle     *WatchdogLifecycleObserver
	Thresholds    WatchdogThresholds
	ReviewTimeout time.Duration
	Reviewer      WatchdogReviewer
	Clock         func() time.Time
}

// WatchdogRuntime observes one live Runner for generation-tail loops.
type WatchdogRuntime struct {
	runtimeFacade
	sessions      session.Service
	thresholds    WatchdogThresholds
	reviewTimeout time.Duration
	reviewer      WatchdogReviewer
	clock         func() time.Time
	lifecycle     *WatchdogLifecycleObserver
}

// NewWatchdogRuntime wraps an execution Runtime with loop detection, durable
// diagnostic checkpoint, and high-confidence turn interrupt.
func NewWatchdogRuntime(config WatchdogRuntimeConfig) (*WatchdogRuntime, error) {
	if config.Runtime == nil {
		return nil, fmt.Errorf("controlplane: watchdog runtime requires an execution runtime")
	}
	if config.Sessions == nil {
		return nil, fmt.Errorf("controlplane: watchdog runtime requires a session service")
	}
	if config.Thresholds.TextLoopStreak < 0 || config.Thresholds.ToolLoopStreak < 0 || config.Thresholds.MinContentRunes < 0 {
		return nil, fmt.Errorf("controlplane: watchdog thresholds cannot be negative")
	}
	if config.Thresholds.TextLoopStreak == 0 {
		config.Thresholds.TextLoopStreak = defaultTextLoopStreak
	}
	if config.Thresholds.ToolLoopStreak == 0 {
		config.Thresholds.ToolLoopStreak = defaultToolLoopStreak
	}
	if config.Thresholds.MinContentRunes == 0 {
		config.Thresholds.MinContentRunes = defaultMinContentRunes
	}
	if config.ReviewTimeout <= 0 {
		config.ReviewTimeout = defaultWatchdogReviewTimeout
	}
	if config.Reviewer == nil {
		config.Reviewer = loopWatchdogReviewer{}
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.Lifecycle == nil {
		config.Lifecycle = NewWatchdogLifecycleObserver()
	}
	return &WatchdogRuntime{
		runtimeFacade: newRuntimeFacade(config.Runtime),
		sessions:      config.Sessions,
		thresholds:    config.Thresholds,
		reviewTimeout: config.ReviewTimeout,
		reviewer:      config.Reviewer,
		clock:         config.Clock,
		lifecycle:     config.Lifecycle,
	}, nil
}

func (r *WatchdogRuntime) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := r.inner.Run(ctx, req)
	if err != nil || result.Handle == nil {
		return result, err
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	if result.Session.SessionID != "" {
		ref = session.NormalizeSessionRef(result.Session.SessionRef)
	}
	return r.wrapLiveHandle(result, func(inner agent.Runner, runID string) agent.Runner {
		return newWatchdogRunner(inner, ref, r, func() { r.forgetRun(runID) })
	}), nil
}

func (r *WatchdogRuntime) PromptParticipant(ctx context.Context, req agent.PromptParticipantRequest) (agent.RunResult, error) {
	participants, err := r.participants()
	if err != nil {
		return agent.RunResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := participants.PromptParticipant(ctx, req)
	if err != nil || result.Handle == nil {
		return result, err
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	if result.Session.SessionID != "" {
		ref = session.NormalizeSessionRef(result.Session.SessionRef)
	}
	return r.wrapLiveHandle(result, func(inner agent.Runner, runID string) agent.Runner {
		return newWatchdogRunner(inner, ref, r, func() { r.forgetRun(runID) })
	}), nil
}

// WatchdogLifecycleObserver routes typed Runtime lifecycle traces without
// blocking execution. Kept for stack TraceSink wiring; loop detection is
// driven by canonical Runner events, not traces.
type WatchdogLifecycleObserver struct {
	mu   sync.RWMutex
	runs map[watchdogRunKey]*watchdogRunner
}

type watchdogRunKey struct {
	sessionID string
	runID     string
}

// NewWatchdogLifecycleObserver creates a lifecycle sink shared by Runtime and
// its Control-owned WatchdogRuntime wrapper.
func NewWatchdogLifecycleObserver() *WatchdogLifecycleObserver {
	return &WatchdogLifecycleObserver{runs: map[watchdogRunKey]*watchdogRunner{}}
}

// RecordTrace implements agent.TraceSink.
func (o *WatchdogLifecycleObserver) RecordTrace(record agent.TraceRecord) {
	// Intentionally no-op for loop detection. Traces stay off the hot path.
	_ = record
}

func (o *WatchdogLifecycleObserver) register(runner *watchdogRunner) {
	if o == nil || runner == nil {
		return
	}
	key := runner.watchdogRunKey()
	if key.sessionID == "" || key.runID == "" {
		return
	}
	o.mu.Lock()
	o.runs[key] = runner
	o.mu.Unlock()
}

func (o *WatchdogLifecycleObserver) unregister(runner *watchdogRunner) {
	if o == nil || runner == nil {
		return
	}
	key := runner.watchdogRunKey()
	o.mu.Lock()
	if o.runs[key] == runner {
		delete(o.runs, key)
	}
	o.mu.Unlock()
}

type watchdogRunner struct {
	inner      agent.Runner
	source     agent.SourceHandle
	sessionRef session.SessionRef
	owner      *WatchdogRuntime
	stop       chan struct{}
	stopOnce   sync.Once
	loop       *generationLoopDetector

	mu             sync.Mutex
	reviewInFlight bool
	reviewSequence uint64
	terminalErr    error
	onFinish       func()
}

func newWatchdogRunner(inner agent.Runner, ref session.SessionRef, owner *WatchdogRuntime, onFinish func()) agent.Runner {
	runner := &watchdogRunner{
		inner: inner, sessionRef: ref, owner: owner, stop: make(chan struct{}), onFinish: onFinish,
		loop: newGenerationLoopDetector(owner.thresholds.TextLoopStreak, owner.thresholds.ToolLoopStreak, owner.thresholds.MinContentRunes),
	}
	owner.lifecycle.register(runner)
	if source, ok := inner.(agent.SourceHandle); ok {
		runner.source = source
		return &watchdogSourceRunner{watchdogRunner: runner}
	}
	return runner
}

func (r *watchdogRunner) RunID() string { return r.inner.RunID() }

func (r *watchdogRunner) watchdogRunKey() watchdogRunKey {
	return watchdogRunKey{sessionID: strings.TrimSpace(r.sessionRef.SessionID), runID: strings.TrimSpace(r.RunID())}
}

func (r *watchdogRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		completed := true
		for event, err := range r.inner.Events() {
			if event != nil {
				r.observe(event)
			}
			if !yield(event, err) {
				completed = false
				break
			}
		}
		if !completed {
			_ = r.inner.Close()
		}
		r.finish()
		if err := r.currentTerminalError(); err != nil {
			yield(nil, err)
		}
	}
}

type watchdogSourceRunner struct{ *watchdogRunner }

func (r *watchdogSourceRunner) SourceEvents() iter.Seq2[agent.SourceEvent, error] {
	return func(yield func(agent.SourceEvent, error) bool) {
		completed := true
		for event, err := range r.source.SourceEvents() {
			if event.Canonical != nil {
				r.observe(event.Canonical)
			}
			if !yield(event, err) {
				completed = false
				break
			}
		}
		if !completed {
			_ = r.inner.Close()
		}
		r.finish()
		if err := r.currentTerminalError(); err != nil {
			yield(agent.SourceEvent{}, err)
		}
	}
}

func (r *watchdogRunner) Submit(submission agent.Submission) error { return r.inner.Submit(submission) }

func (r *watchdogRunner) Cancel() agent.CancelResult {
	r.finish()
	return r.inner.Cancel()
}

func (r *watchdogRunner) Close() error {
	r.finish()
	return r.inner.Close()
}

func (r *watchdogRunner) observe(event *session.Event) {
	if event == nil {
		return
	}
	r.mu.Lock()
	hit, ok := r.loop.observe(event)
	if !ok || r.reviewInFlight {
		r.mu.Unlock()
		return
	}
	r.reviewInFlight = true
	r.reviewSequence++
	observation := WatchdogObservation{
		SessionRef: r.sessionRef, RunID: r.inner.RunID(), ReviewSequence: r.reviewSequence,
		ObservedAt: r.owner.clock(), LoopStreak: hit.Streak, LoopHasTool: hit.HasTool,
		ContentDigest: hit.Content, ToolDigest: hit.Tools, LoopDetail: hit.Detail,
		Reasons: []WatchdogReason{hit.Reason},
	}
	r.mu.Unlock()
	go r.review(observation)
}

func (r *watchdogRunner) review(observation WatchdogObservation) {
	ctx, cancel := context.WithTimeout(context.Background(), r.owner.reviewTimeout)
	decision, err := r.owner.reviewer.ReviewWatchdog(ctx, observation)
	cancel()
	if err == nil {
		err = r.applyDecision(observation, decision)
	}
	r.mu.Lock()
	r.reviewInFlight = false
	if err != nil {
		r.terminalErr = errors.Join(r.terminalErr, fmt.Errorf("controlplane: watchdog review failed: %w", err))
	}
	r.mu.Unlock()
}

func (r *watchdogRunner) applyDecision(observation WatchdogObservation, decision WatchdogDecision) error {
	select {
	case <-r.stop:
		return nil
	default:
	}
	action := decision.Action
	if action == "" {
		action = WatchdogActionContinue
	}
	switch action {
	case WatchdogActionContinue:
		return nil
	case WatchdogActionCheckpoint:
		return r.appendCheckpoint(observation, decision, action)
	case WatchdogActionInterrupt:
		if err := r.appendCheckpoint(observation, decision, action); err != nil {
			return err
		}
		if result := r.inner.Cancel(); result.Err != nil {
			return result.Err
		}
		return nil
	case WatchdogActionCancel:
		if err := r.appendCheckpoint(observation, decision, action); err != nil {
			return err
		}
		if !decision.Confirmed {
			return nil
		}
		if result := r.inner.Cancel(); result.Err != nil {
			return result.Err
		}
		return nil
	default:
		return fmt.Errorf("unsupported watchdog action %q", action)
	}
}

func (r *watchdogRunner) appendCheckpoint(observation WatchdogObservation, decision WatchdogDecision, action WatchdogAction) error {
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		reason = fmt.Sprintf("watchdog %s after %s", action, strings.Join(watchdogReasonStrings(observation.Reasons), ","))
	}
	event := &session.Event{
		IdempotencyKey: fmt.Sprintf("watchdog:%s:%d", strings.TrimSpace(observation.RunID), observation.ReviewSequence),
		Type:           session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Time: observation.ObservedAt,
		Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: "control-watchdog"},
		Lifecycle: &session.EventLifecycle{Status: watchdogCheckpointStatus, Reason: reason, Meta: map[string]any{
			"action":      string(action),
			"loop_streak": observation.LoopStreak, "loop_has_tool": observation.LoopHasTool,
			"content_digest": observation.ContentDigest, "tool_digest": observation.ToolDigest,
			"loop_detail": observation.LoopDetail,
			"reasons":     watchdogReasonStrings(observation.Reasons),
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.owner.reviewTimeout)
	defer cancel()
	_, err := r.owner.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: r.sessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeWatchdog), Event: event,
	})
	if session.IsCommitted(err) {
		return nil
	}
	return err
}

func (r *watchdogRunner) finish() {
	r.stopOnce.Do(func() {
		close(r.stop)
		r.owner.lifecycle.unregister(r)
		if r.onFinish != nil {
			r.onFinish()
		}
	})
}

func (r *watchdogRunner) currentTerminalError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.terminalErr
}

// loopWatchdogReviewer interrupts high-confidence generation loops.
type loopWatchdogReviewer struct{}

func (loopWatchdogReviewer) ReviewWatchdog(_ context.Context, observation WatchdogObservation) (WatchdogDecision, error) {
	if observation.HasReason(WatchdogReasonTextLoop) || observation.HasReason(WatchdogReasonToolLoop) {
		return WatchdogDecision{
			Action: WatchdogActionInterrupt,
			Reason: fmt.Sprintf("high-confidence generation loop: %s", strings.Join(watchdogReasonStrings(observation.Reasons), ",")),
		}, nil
	}
	return WatchdogDecision{Action: WatchdogActionContinue}, nil
}

func watchdogReasonStrings(reasons []WatchdogReason) []string {
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		out = append(out, string(reason))
	}
	return out
}
