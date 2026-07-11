package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	watchdogCheckpointStatus      = "watchdog_checkpoint"
	defaultWatchdogElapsed        = 30 * time.Minute
	defaultWatchdogNoProgress     = 10 * time.Minute
	defaultWatchdogRepeatedTools  = 6
	defaultWatchdogTickInterval   = time.Second
	defaultWatchdogReviewInterval = 5 * time.Minute
	defaultWatchdogReviewTimeout  = 30 * time.Second
)

var (
	_ agent.StreamProvider          = (*WatchdogRuntime)(nil)
	_ agent.LiveRunAttacher         = (*WatchdogRuntime)(nil)
	_ agent.ApprovalResolver        = (*WatchdogRuntime)(nil)
	_ agent.ParticipantControlPlane = (*WatchdogRuntime)(nil)
	_ PlacementExecutor             = (*WatchdogRuntime)(nil)
)

// ExecutePlaced delegates to the inner PlacementExecutor (typically
// LeasedRuntime). Soft watchdog review requires a live Runner event stream and
// is intentionally not reimplemented here for synchronous Control ops; lease
// fencing/heartbeat/cancel-on-loss is the placement safety envelope.
func (r *WatchdogRuntime) ExecutePlaced(ctx context.Context, ref session.SessionRef, execute func(context.Context) error) error {
	placement, ok := r.inner.(PlacementExecutor)
	if !ok {
		return fmt.Errorf("controlplane: decorated runtime does not support placed operations")
	}
	return placement.ExecutePlaced(ctx, ref, execute)
}

// WatchdogReason identifies a soft Control review trigger.
type WatchdogReason string

const (
	WatchdogReasonElapsed      WatchdogReason = "elapsed"
	WatchdogReasonNoProgress   WatchdogReason = "no_progress"
	WatchdogReasonRepeatedTool WatchdogReason = "repeated_tool"
	WatchdogReasonUsage        WatchdogReason = "usage"
)

// WatchdogAction identifies the Control decision after reviewing dynamic run
// signals.
type WatchdogAction string

const (
	WatchdogActionContinue   WatchdogAction = "continue"
	WatchdogActionCheckpoint WatchdogAction = "checkpoint"
	WatchdogActionCancel     WatchdogAction = "cancel"
)

// WatchdogThresholds configures soft review triggers. These are not SDK step
// budgets and never cancel a run without a reviewer decision.
type WatchdogThresholds struct {
	Elapsed           time.Duration
	NoProgress        time.Duration
	RepeatedToolCalls int
	TotalTokens       int
}

// WatchdogObservation is one immutable run-safety snapshot presented to
// Control policy or a user-confirmation adapter.
type WatchdogObservation struct {
	SessionRef            session.SessionRef
	RunID                 string
	ReviewSequence        uint64
	StartedAt             time.Time
	ObservedAt            time.Time
	Elapsed               time.Duration
	SinceProgress         time.Duration
	RepeatedToolSignature string
	RepeatedToolCalls     int
	Usage                 session.UsageSnapshot
	LifecycleStatus       string
	Reasons               []WatchdogReason
}

// HasReason reports whether this snapshot crossed one soft threshold.
func (o WatchdogObservation) HasReason(reason WatchdogReason) bool {
	for _, candidate := range o.Reasons {
		if candidate == reason {
			return true
		}
	}
	return false
}

// WatchdogDecision is the reviewed Control action. Cancellation requires
// Confirmed so an adapter can make user confirmation explicit.
type WatchdogDecision struct {
	Action    WatchdogAction
	Confirmed bool
	Reason    string
}

// WatchdogReviewer maps dynamic observations to Control-owned actions.
type WatchdogReviewer interface {
	ReviewWatchdog(context.Context, WatchdogObservation) (WatchdogDecision, error)
}

// WatchdogReviewFunc adapts a function to WatchdogReviewer.
type WatchdogReviewFunc func(context.Context, WatchdogObservation) (WatchdogDecision, error)

func (f WatchdogReviewFunc) ReviewWatchdog(ctx context.Context, observation WatchdogObservation) (WatchdogDecision, error) {
	return f(ctx, observation)
}

// WatchdogRuntimeConfig configures the Control-owned dynamic run watchdog.
type WatchdogRuntimeConfig struct {
	Runtime        agent.Runtime
	Sessions       session.Service
	Lifecycle      *WatchdogLifecycleObserver
	Thresholds     WatchdogThresholds
	TickInterval   time.Duration
	ReviewInterval time.Duration
	ReviewTimeout  time.Duration
	Reviewer       WatchdogReviewer
	Clock          func() time.Time
}

// WatchdogRuntime observes one live Runner without owning Agent semantics.
type WatchdogRuntime struct {
	runtimeFacade
	sessions       session.Service
	thresholds     WatchdogThresholds
	tickInterval   time.Duration
	reviewInterval time.Duration
	reviewTimeout  time.Duration
	reviewer       WatchdogReviewer
	clock          func() time.Time
	lifecycle      *WatchdogLifecycleObserver
}

// NewWatchdogRuntime wraps an execution Runtime with soft-threshold review,
// durable diagnostic checkpoint, and confirmed-cancellation support.
func NewWatchdogRuntime(config WatchdogRuntimeConfig) (*WatchdogRuntime, error) {
	if config.Runtime == nil {
		return nil, fmt.Errorf("controlplane: watchdog runtime requires an execution runtime")
	}
	if config.Sessions == nil {
		return nil, fmt.Errorf("controlplane: watchdog runtime requires a session service")
	}
	if config.Thresholds.Elapsed < 0 || config.Thresholds.NoProgress < 0 || config.Thresholds.RepeatedToolCalls < 0 || config.Thresholds.TotalTokens < 0 {
		return nil, fmt.Errorf("controlplane: watchdog thresholds cannot be negative")
	}
	if config.Thresholds == (WatchdogThresholds{}) {
		config.Thresholds = WatchdogThresholds{
			Elapsed:           defaultWatchdogElapsed,
			NoProgress:        defaultWatchdogNoProgress,
			RepeatedToolCalls: defaultWatchdogRepeatedTools,
		}
	}
	if config.TickInterval <= 0 {
		config.TickInterval = defaultWatchdogTickInterval
	}
	if config.ReviewInterval <= 0 {
		config.ReviewInterval = defaultWatchdogReviewInterval
	}
	if config.ReviewTimeout <= 0 {
		config.ReviewTimeout = defaultWatchdogReviewTimeout
	}
	if config.Reviewer == nil {
		config.Reviewer = checkpointWatchdogReviewer{}
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.Lifecycle == nil {
		config.Lifecycle = NewWatchdogLifecycleObserver()
	}
	return &WatchdogRuntime{
		runtimeFacade:  newRuntimeFacade(config.Runtime),
		sessions:       config.Sessions,
		thresholds:     config.Thresholds,
		tickInterval:   config.TickInterval,
		reviewInterval: config.ReviewInterval,
		reviewTimeout:  config.ReviewTimeout,
		reviewer:       config.Reviewer,
		clock:          config.Clock,
		lifecycle:      config.Lifecycle,
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

// WatchdogLifecycleObserver routes typed Runtime lifecycle traces into the
// matching dynamic run observation without blocking execution.
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
	if o == nil {
		return
	}
	key := watchdogRunKey{sessionID: strings.TrimSpace(record.Event.SessionRef.SessionID), runID: strings.TrimSpace(record.Event.RunID)}
	if key.sessionID == "" || key.runID == "" {
		return
	}
	o.mu.RLock()
	runner := o.runs[key]
	o.mu.RUnlock()
	if runner != nil {
		runner.observeLifecycle(record)
	}
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
	startedAt  time.Time
	stop       chan struct{}
	stopOnce   sync.Once

	mu                    sync.Mutex
	lastProgressAt        time.Time
	lastReviewAt          time.Time
	reviewInFlight        bool
	reviewSequence        uint64
	repeatedToolSignature string
	repeatedToolCalls     int
	usage                 session.UsageSnapshot
	lifecycleStatus       string
	terminalErr           error
	onFinish              func()
}

func newWatchdogRunner(inner agent.Runner, ref session.SessionRef, owner *WatchdogRuntime, onFinish func()) agent.Runner {
	startedAt := owner.clock()
	runner := &watchdogRunner{
		inner: inner, sessionRef: ref, owner: owner, startedAt: startedAt,
		lastProgressAt: startedAt, stop: make(chan struct{}), onFinish: onFinish,
	}
	owner.lifecycle.register(runner)
	go runner.monitor()
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

func (r *watchdogRunner) monitor() {
	ticker := time.NewTicker(r.owner.tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.maybeReview(r.owner.clock())
		}
	}
}

func (r *watchdogRunner) observe(event *session.Event) {
	if event == nil {
		return
	}
	now := r.owner.clock()
	r.mu.Lock()
	if usage := session.UsageSnapshotFromSessionEvent(event); usage != nil {
		r.usage = addWatchdogUsage(r.usage, *usage)
	}
	if event.Lifecycle != nil {
		r.lifecycleStatus = strings.TrimSpace(event.Lifecycle.Status)
	}
	switch session.EventTypeOf(event) {
	case session.EventTypeToolCall:
		signature := watchdogToolSignature(event)
		if signature != "" && signature == r.repeatedToolSignature {
			r.repeatedToolCalls++
		} else {
			r.repeatedToolSignature = signature
			r.repeatedToolCalls = 1
		}
	case session.EventTypeAssistant, session.EventTypePlan:
		r.lastProgressAt = now
	case session.EventTypeToolResult:
		if event.Tool != nil && watchdogSuccessfulToolStatus(event.Tool.Status) {
			r.lastProgressAt = now
		}
	}
	r.mu.Unlock()
	r.maybeReview(now)
}

func (r *watchdogRunner) maybeReview(now time.Time) {
	select {
	case <-r.stop:
		return
	default:
	}
	r.mu.Lock()
	if r.reviewInFlight || (!r.lastReviewAt.IsZero() && now.Sub(r.lastReviewAt) < r.owner.reviewInterval) {
		r.mu.Unlock()
		return
	}
	reasons := r.reasonsLocked(now)
	if len(reasons) == 0 {
		r.mu.Unlock()
		return
	}
	r.reviewInFlight = true
	r.lastReviewAt = now
	r.reviewSequence++
	observation := WatchdogObservation{
		SessionRef: r.sessionRef, RunID: r.inner.RunID(), ReviewSequence: r.reviewSequence,
		StartedAt: r.startedAt, ObservedAt: now, Elapsed: max(now.Sub(r.startedAt), 0),
		SinceProgress: max(now.Sub(r.lastProgressAt), 0), RepeatedToolSignature: r.repeatedToolSignature,
		RepeatedToolCalls: r.repeatedToolCalls, Usage: r.usage, LifecycleStatus: r.lifecycleStatus,
		Reasons: append([]WatchdogReason(nil), reasons...),
	}
	r.mu.Unlock()
	go r.review(observation)
}

func (r *watchdogRunner) observeLifecycle(record agent.TraceRecord) {
	now := r.owner.clock()
	r.mu.Lock()
	r.lifecycleStatus = strings.TrimSpace(string(record.Event.Operation) + ":" + string(record.Status))
	r.mu.Unlock()
	r.maybeReview(now)
}

func (r *watchdogRunner) reasonsLocked(now time.Time) []WatchdogReason {
	thresholds := r.owner.thresholds
	reasons := make([]WatchdogReason, 0, 4)
	if thresholds.Elapsed > 0 && now.Sub(r.startedAt) >= thresholds.Elapsed {
		reasons = append(reasons, WatchdogReasonElapsed)
	}
	if thresholds.NoProgress > 0 && now.Sub(r.lastProgressAt) >= thresholds.NoProgress {
		reasons = append(reasons, WatchdogReasonNoProgress)
	}
	if thresholds.RepeatedToolCalls > 0 && r.repeatedToolCalls >= thresholds.RepeatedToolCalls {
		reasons = append(reasons, WatchdogReasonRepeatedTool)
	}
	if thresholds.TotalTokens > 0 && r.usage.TotalTokens >= thresholds.TotalTokens {
		reasons = append(reasons, WatchdogReasonUsage)
	}
	return reasons
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
			"action": string(action), "confirmed": decision.Confirmed,
			"elapsed_ms": observation.Elapsed.Milliseconds(), "since_progress_ms": observation.SinceProgress.Milliseconds(),
			"repeated_tool_signature": observation.RepeatedToolSignature, "repeated_tool_calls": observation.RepeatedToolCalls,
			"total_tokens": observation.Usage.TotalTokens, "lifecycle_status": observation.LifecycleStatus,
			"reasons": watchdogReasonStrings(observation.Reasons),
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.owner.reviewTimeout)
	defer cancel()
	_, err := r.owner.sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: r.sessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeWatchdog), Event: event})
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

type checkpointWatchdogReviewer struct{}

func (checkpointWatchdogReviewer) ReviewWatchdog(_ context.Context, observation WatchdogObservation) (WatchdogDecision, error) {
	return WatchdogDecision{
		Action: WatchdogActionCheckpoint,
		Reason: fmt.Sprintf("soft watchdog threshold reached: %s", strings.Join(watchdogReasonStrings(observation.Reasons), ",")),
	}, nil
}

func watchdogToolSignature(event *session.Event) string {
	if event == nil || event.Tool == nil {
		return ""
	}
	payload, err := json.Marshal(event.Tool.Input)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(append([]byte(strings.ToUpper(strings.TrimSpace(event.Tool.Name))+"\x00"), payload...))
	return hex.EncodeToString(hash[:])
}

func watchdogSuccessfulToolStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "succeeded", "success":
		return true
	default:
		return false
	}
}

func addWatchdogUsage(left, right session.UsageSnapshot) session.UsageSnapshot {
	left.PromptTokens += right.PromptTokens
	left.CachedInputTokens += right.CachedInputTokens
	left.CompletionTokens += right.CompletionTokens
	left.ReasoningTokens += right.ReasoningTokens
	left.TotalTokens += right.TotalTokens
	left.ContextWindowTokens = max(left.ContextWindowTokens, right.ContextWindowTokens)
	return left
}

func watchdogReasonStrings(reasons []WatchdogReason) []string {
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		out = append(out, string(reason))
	}
	return out
}
