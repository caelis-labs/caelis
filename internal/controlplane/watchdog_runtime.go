package controlplane

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	watchdogCheckpointStatus        = "loop_watchdog_checkpoint"
	defaultWatchdogReviewTimeout    = 30 * time.Second
	maxOutstandingWatchdogPipelines = 8
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

// WatchdogReason identifies a high-confidence loop-detector trigger.
type WatchdogReason string

const (
	// WatchdogReasonTextLoop is a pure reasoning/assistant stream-tail cycle.
	WatchdogReasonTextLoop WatchdogReason = "text_loop"
	// WatchdogReasonToolLoop is identical content-segment + tool args steps.
	WatchdogReasonToolLoop WatchdogReason = "tool_loop"
)

// WatchdogAction identifies the only Control decisions supported by the loop
// watchdog. It deliberately has no generic Cancel action: only a validated
// high-confidence output loop may interrupt a live Turn.
type WatchdogAction string

const (
	WatchdogActionContinue  WatchdogAction = "continue"
	WatchdogActionInterrupt WatchdogAction = "interrupt"
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

// WatchdogDecision is the reviewed Control action. Interrupt is accepted only
// for an observation produced by the high-confidence loop detector.
type WatchdogDecision struct {
	Action WatchdogAction
	Reason string
}

// WatchdogReviewer maps high-confidence observations to Control-owned actions.
// It runs asynchronously and cannot delay or fail the Agent Turn.
type WatchdogReviewer interface {
	ReviewWatchdog(context.Context, WatchdogObservation) (WatchdogDecision, error)
}

// WatchdogReviewFunc adapts a function to WatchdogReviewer.
type WatchdogReviewFunc func(context.Context, WatchdogObservation) (WatchdogDecision, error)

func (f WatchdogReviewFunc) ReviewWatchdog(ctx context.Context, observation WatchdogObservation) (WatchdogDecision, error) {
	return f(ctx, observation)
}

// WatchdogRuntimeConfig configures the Control-owned generation-tail loop
// watchdog. ReviewTimeout bounds cooperative review/audit work only; watchdog
// failures never become Turn errors.
type WatchdogRuntimeConfig struct {
	Runtime       agent.Runtime
	Sessions      session.Service
	Thresholds    WatchdogThresholds
	ReviewTimeout time.Duration
	Reviewer      WatchdogReviewer
	Clock         func() time.Time
}

// WatchdogRuntime observes live Runner output. It may interrupt only after a
// high-confidence loop decision. Detection saturation and all internal
// review/audit failures are best-effort and never affect the Turn.
type WatchdogRuntime struct {
	runtimeFacade
	sessions      session.Service
	thresholds    WatchdogThresholds
	reviewTimeout time.Duration
	reviewer      WatchdogReviewer
	clock         func() time.Time
	pipelineSlots chan struct{}
}

// NewWatchdogRuntime wraps an execution Runtime with bounded loop detection
// and high-confidence turn interruption.
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
	return &WatchdogRuntime{
		runtimeFacade: newRuntimeFacade(config.Runtime),
		sessions:      config.Sessions,
		thresholds:    config.Thresholds,
		reviewTimeout: config.ReviewTimeout,
		reviewer:      config.Reviewer,
		clock:         config.Clock,
		pipelineSlots: make(chan struct{}, maxOutstandingWatchdogPipelines),
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
	return r.wrapLiveHandle(result, ref, func(inner agent.Runner, onFinish func()) agent.Runner {
		return newWatchdogRunner(inner, ref, r, onFinish)
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
	return r.wrapLiveHandle(result, ref, func(inner agent.Runner, onFinish func()) agent.Runner {
		return newWatchdogRunner(inner, ref, r, onFinish)
	}), nil
}

// WatchdogLifecycleObserver remains a non-blocking TraceSink for stack wiring.
// Loop detection is driven only by canonical Runner events.
type WatchdogLifecycleObserver struct{}

func NewWatchdogLifecycleObserver() *WatchdogLifecycleObserver { return &WatchdogLifecycleObserver{} }

func (*WatchdogLifecycleObserver) RecordTrace(agent.TraceRecord) {}

type watchdogRunner struct {
	inner      agent.Runner
	source     agent.SourceHandle
	sessionRef session.SessionRef
	owner      *WatchdogRuntime
	stop       chan struct{}
	stopOnce   sync.Once
	loop       *generationLoopDetector

	mu               sync.Mutex
	reviewInFlight   bool
	reviewSequence   uint64
	pendingHit       *loopHit
	pipelineCancel   context.CancelFunc
	pipelineSequence uint64
	terminal         bool
	actionClaimed    bool
	actionSequence   uint64
	cancelOnce       sync.Once
	cancelResult     agent.CancelResult
	onFinish         func()
}

func newWatchdogRunner(inner agent.Runner, ref session.SessionRef, owner *WatchdogRuntime, onFinish func()) agent.Runner {
	runner := &watchdogRunner{
		inner: inner, sessionRef: ref, owner: owner, stop: make(chan struct{}), onFinish: onFinish,
		loop: newGenerationLoopDetector(owner.thresholds.TextLoopStreak, owner.thresholds.ToolLoopStreak, owner.thresholds.MinContentRunes),
	}
	if source, ok := inner.(agent.SourceHandle); ok {
		runner.source = source
		return &watchdogSourceRunner{watchdogRunner: runner}
	}
	return runner
}

func (r *watchdogRunner) RunID() string { return r.inner.RunID() }

func (r *watchdogRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for event, err := range r.inner.Events() {
			if event != nil {
				r.observe(event)
			}
			if !yield(event, err) {
				r.finish()
				_ = r.inner.Close()
				return
			}
		}
		r.finish()
	}
}

type watchdogSourceRunner struct{ *watchdogRunner }

func (r *watchdogSourceRunner) SourceEvents() iter.Seq2[agent.SourceEvent, error] {
	return func(yield func(agent.SourceEvent, error) bool) {
		for event, err := range r.source.SourceEvents() {
			if event.Canonical != nil {
				r.observe(event.Canonical)
			}
			if !yield(event, err) {
				r.finish()
				_ = r.inner.Close()
				return
			}
		}
		r.finish()
	}
}

func (r *watchdogRunner) Submit(submission agent.Submission) error { return r.inner.Submit(submission) }

func (r *watchdogRunner) Cancel() agent.CancelResult {
	r.finish()
	return r.cancelInner()
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
	if r.terminal {
		r.mu.Unlock()
		return
	}
	hit, ok := r.loop.observe(event)
	if !ok {
		r.mu.Unlock()
		return
	}
	if r.reviewInFlight {
		copyHit := hit
		r.pendingHit = &copyHit
		r.mu.Unlock()
		return
	}
	if !r.owner.tryAcquireWatchdogPipeline() {
		// Capacity is diagnostic only. Drop this evidence window and let the
		// Agent Turn continue untouched.
		r.loop.resetAll()
		r.mu.Unlock()
		return
	}
	r.startReviewLocked(hit)
	r.mu.Unlock()
}

func (r *watchdogRunner) startReviewLocked(hit loopHit) {
	r.reviewInFlight = true
	r.reviewSequence++
	pipelineCtx, pipelineCancel := context.WithCancel(context.Background())
	r.pipelineCancel = pipelineCancel
	r.pipelineSequence = r.reviewSequence
	observation := WatchdogObservation{
		SessionRef: r.sessionRef, RunID: r.inner.RunID(), ReviewSequence: r.reviewSequence,
		ObservedAt: r.owner.clock(), LoopStreak: hit.Streak, LoopHasTool: hit.HasTool,
		ContentDigest: hit.Content, ToolDigest: hit.Tools, LoopDetail: hit.Detail,
		Reasons: []WatchdogReason{hit.Reason},
	}
	go r.review(pipelineCtx, observation)
}

func (r *watchdogRunner) review(parent context.Context, observation WatchdogObservation) {
	var (
		decision WatchdogDecision
		claimed  bool
	)
	defer func() {
		_ = recover()
		r.owner.releaseWatchdogPipeline()
		r.completeReview(observation.ReviewSequence, claimed)
	}()
	ctx, cancel := context.WithTimeout(parent, r.owner.reviewTimeout)
	defer cancel()
	var err error
	decision, err = r.owner.reviewer.ReviewWatchdog(ctx, observation)
	if err != nil {
		return
	}
	claimed, _ = r.applyDecision(ctx, observation, decision)
}

func (r *watchdogRunner) completeReview(sequence uint64, claimed bool) {
	r.mu.Lock()
	if r.pipelineSequence != sequence {
		r.mu.Unlock()
		return
	}
	r.pipelineCancel = nil
	r.pipelineSequence = 0
	r.reviewInFlight = false
	select {
	case <-r.stop:
		r.pendingHit = nil
		r.mu.Unlock()
		return
	default:
	}
	if claimed || (r.actionClaimed && r.actionSequence == sequence) {
		r.terminal = true
		r.pendingHit = nil
		r.mu.Unlock()
		return
	}
	r.loop.resetAll()
	pending := r.pendingHit
	r.pendingHit = nil
	if pending == nil || !r.owner.tryAcquireWatchdogPipeline() {
		r.mu.Unlock()
		return
	}
	r.startReviewLocked(*pending)
	r.mu.Unlock()
}

func (r *watchdogRunner) applyDecision(ctx context.Context, observation WatchdogObservation, decision WatchdogDecision) (bool, error) {
	action := decision.Action
	if action == "" {
		action = WatchdogActionContinue
	}
	switch action {
	case WatchdogActionContinue:
		return false, nil
	case WatchdogActionInterrupt:
		if !observation.HasReason(WatchdogReasonTextLoop) && !observation.HasReason(WatchdogReasonToolLoop) {
			return false, fmt.Errorf("controlplane: watchdog interrupt requires high-confidence loop evidence")
		}
		if err := r.claimWatchdogInterrupt(ctx, observation.ReviewSequence); err != nil {
			return false, err
		}
		// Interrupt first; durable audit is best-effort and must never gate the
		// core safety action or become a Turn error.
		_ = r.cancelInner()
		_ = r.appendCheckpoint(ctx, observation, decision)
		return true, nil
	default:
		return false, fmt.Errorf("controlplane: unsupported watchdog action %q", action)
	}
}

func (r *watchdogRunner) claimWatchdogInterrupt(ctx context.Context, sequence uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-r.stop:
		return context.Canceled
	default:
	}
	if r.reviewSequence != sequence || r.pipelineSequence != sequence || !r.reviewInFlight || r.terminal {
		return fmt.Errorf("controlplane: watchdog review no longer owns the interrupt")
	}
	if r.actionClaimed {
		return fmt.Errorf("controlplane: watchdog interrupt was already claimed")
	}
	r.actionClaimed = true
	r.actionSequence = sequence
	r.terminal = true
	return nil
}

func (r *watchdogRunner) cancelInner() agent.CancelResult {
	if r == nil || r.inner == nil {
		return agent.CancelResult{Err: fmt.Errorf("controlplane: watchdog runner is unavailable")}
	}
	r.cancelOnce.Do(func() {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					r.cancelResult.Err = fmt.Errorf("controlplane: watchdog interrupt panic: %v", recovered)
				}
			}()
			r.cancelResult = r.inner.Cancel()
		}()
	})
	return r.cancelResult
}

func (r *watchdogRunner) appendCheckpoint(ctx context.Context, observation WatchdogObservation, decision WatchdogDecision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		reason = fmt.Sprintf("high-confidence generation loop: %s", strings.Join(watchdogReasonStrings(observation.Reasons), ","))
	}
	event := &session.Event{
		IdempotencyKey: fmt.Sprintf("watchdog:%s:%d", strings.TrimSpace(observation.RunID), observation.ReviewSequence),
		Type:           session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Time: observation.ObservedAt,
		Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: "control-watchdog"},
		Lifecycle: &session.EventLifecycle{Status: watchdogCheckpointStatus, Reason: reason, Meta: map[string]any{
			"action": string(WatchdogActionInterrupt), "loop_streak": observation.LoopStreak,
			"loop_has_tool": observation.LoopHasTool, "content_digest": observation.ContentDigest,
			"tool_digest": observation.ToolDigest, "loop_detail": observation.LoopDetail,
			"reasons": watchdogReasonStrings(observation.Reasons),
		}},
	}
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
		r.mu.Lock()
		close(r.stop)
		cancel := r.pipelineCancel
		r.pipelineCancel = nil
		r.pipelineSequence = 0
		r.pendingHit = nil
		r.reviewInFlight = false
		r.terminal = true
		r.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if r.onFinish != nil {
			r.onFinish()
		}
	})
}

func (r *WatchdogRuntime) tryAcquireWatchdogPipeline() bool {
	if r == nil || r.pipelineSlots == nil {
		return true
	}
	select {
	case r.pipelineSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (r *WatchdogRuntime) releaseWatchdogPipeline() {
	if r == nil || r.pipelineSlots == nil {
		return
	}
	select {
	case <-r.pipelineSlots:
	default:
	}
}

// loopWatchdogReviewer interrupts only high-confidence generation loops.
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
