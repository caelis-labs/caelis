package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/requestsource"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// IdleCompactionConfig enables Host-coordinated compaction between Turns. It is
// independent of Async. The zero value retains no idle provider lease or work.
type IdleCompactionConfig struct{ Enabled bool }

// IdleCompactionJob owns one completed source and its provider/configuration
// lease. The Host must cancel and drain Run before Close, and retain the job
// while FlushAccounting reports an error. It never owns a foreground fence.
type IdleCompactionJob struct {
	runtime               *Runtime
	candidate             *asyncCompactJob
	authority             *modelRequestAuthority
	release               func()
	closeOnce             sync.Once
	startedAt, preparedAt time.Time
	turnID, sourceStamp   string
	attempts              []idleCompactionAttempt
	accountingErr         error
	uncertainBegin        *session.BeginCompactionAttemptRequest
}

type idleCompactionAttempt struct {
	ticket            session.CompactionReceiptTicket
	invocation        model.Invocation
	staged, finalized bool
}
type idleCompactionCandidate struct {
	job     *asyncCompactJob
	readyAt time.Time
}

func (r *Runtime) bindIdleCompactionAttempt(ref session.SessionRef, in model.Invocation, authority *modelRequestAuthority, turnID string) {
	if !r.compaction.Idle.Enabled || authority == nil || !authority.canSpeculate || turnID == "" {
		return
	}
	release := authority.retain()
	r.compactionRequestMu.Lock()
	key := compactionSessionKey(ref)
	snapshot, ok := r.compactionRequests[key]
	if ok && snapshot.dispatch != nil && snapshot.dispatch.Source != nil {
		snapshot.startedAt = in.StartedAt
		snapshot.turnID = turnID
		snapshot.authority = authority
		snapshot.release = release
		r.compactionRequests[key] = snapshot
		release = nil
	}
	r.compactionRequestMu.Unlock()
	if release != nil {
		release()
	}
}

// PrepareIdleCompaction consumes only the last successful actual request's
// completed source. Store reload verifies S -> final-event proof; it never
// upgrades the immutable dispatch boundary to the latest observed sequence.
func (r *Runtime) PrepareIdleCompaction(ctx context.Context, ref session.SessionRef) (*IdleCompactionJob, error) {
	if !r.compaction.Enabled || !r.compaction.Idle.Enabled {
		return nil, nil
	}
	if _, ok := r.compactor.(compact.ForceEngine); !ok {
		return nil, nil
	}
	if _, ok := r.sessions.(session.IdleCompactionStore); !ok {
		return nil, nil
	}
	if _, ok := r.sessions.(session.CompactionReceiptStore); !ok {
		return nil, nil
	}
	r.compactionRequestMu.Lock()
	key := compactionSessionKey(ref)
	snapshot, ok := r.compactionRequests[key]
	if ok {
		next := snapshot
		next.release = nil
		next.authority = nil
		r.compactionRequests[key] = next
	}
	r.compactionRequestMu.Unlock()
	release := snapshot.release
	keep := false
	defer func() {
		if !keep && release != nil {
			release()
		}
	}()
	if !ok || !snapshot.completed || snapshot.dispatch == nil || snapshot.dispatch.Source == nil || snapshot.authority == nil || release == nil || snapshot.startedAt.IsZero() {
		return nil, nil
	}
	provider, name := compactionModelIdentity(snapshot.model)
	// NeverExpires is selected by the stable provider/model channel name,
	// including Codex behind a Magpie endpoint. No provider whitelist is used.
	if strings.Contains(strings.ToLower(provider+"/"+name), "codex") || model.UsesProviderExecutedTools(snapshot.model, snapshot.dispatch) {
		return nil, nil
	}
	if !compactionPrefixSafe(snapshot.request.Messages) {
		return nil, nil
	}
	loaded, err := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		return nil, err
	}
	events := mainInvocationEvents(loaded.Events)
	source := snapshot.dispatch.Source
	messagesHash, err := requestsource.Fingerprint(snapshot.dispatch.Messages)
	if err != nil || messagesHash != source.MessagesHash {
		return nil, err
	}
	cut := 0
	for cut < len(events) && events[cut].Seq <= source.SourceThroughSeq {
		cut++
	}
	hash, err := requestsource.Fingerprint(events[:cut])
	if err != nil {
		return nil, err
	}
	if hash != source.SourcePrefixHash || session.LastEventSeq(events[:cut]) != source.SourceThroughSeq || len(events[cut:]) != 1 {
		return nil, nil
	}
	final := events[cut]
	if final.Type != session.EventTypeAssistant || final.Message == nil || final.Scope == nil || final.Scope.TurnID != snapshot.turnID || !reflect.DeepEqual(*final.Message, snapshot.final) {
		return nil, nil
	}
	if !asyncToolPairsComplete(mainInvocationEvents(loaded.Events)) {
		return nil, nil
	}
	tasks, err := r.tasks.activeSubagentSessionEntries(ctx, ref)
	if err != nil {
		return nil, err
	}
	if len(tasks) > 0 {
		return nil, nil
	}
	if reader, ok := r.sessions.(session.ApprovalRecoveryReader); ok {
		pending, err := reader.PendingApprovals(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range pending {
			if item.SessionRef.SessionID == ref.SessionID {
				return nil, nil
			}
		}
	}
	delta := compactableEvents(events)
	if estimatePromptEventsTokens(delta) < 16000 {
		return nil, nil
	}
	turns := map[string]bool{}
	for _, event := range delta {
		if event.Type == session.EventTypeAssistant && event.Message != nil && len(event.Message.ToolCalls()) == 0 && event.Scope != nil && event.Scope.TurnID != "" {
			turns[event.Scope.TurnID] = true
		}
	}
	if len(turns) < 2 {
		return nil, nil
	}
	window, ok := snapshot.model.(compact.ContextWindowProvider)
	if !ok || window.ContextWindowTokens() <= 0 {
		return nil, nil
	}
	usage, _, _ := usageForModelRequestDetails(events, snapshot.model, snapshot.request, r.compaction)
	if float64(usage.TotalTokens) < .5*float64(window.ContextWindowTokens()) || evaluateWatermark(usage, r.compaction).ShouldCompact {
		return nil, nil
	}
	loaded.Events = session.CloneEvents(events)
	stamp, err := session.CheckpointSourceStamp(loaded)
	if err != nil {
		return nil, err
	}
	frozen := model.CloneRequest(snapshot.request)
	frozenHash, err := requestsource.Fingerprint(frozen)
	if err != nil {
		return nil, err
	}
	sourceHash, err := requestsource.Fingerprint(events)
	if err != nil {
		return nil, err
	}
	requestHash, err := asyncRequestFingerprint(frozen)
	if err != nil {
		return nil, err
	}
	// Acquire a new resolver-owned lease before relinquishing the completed Turn's
	// bridge reference. In particular, do not carry its Turn-bound Admit closure.
	fresh := agent.ModelRequestSnapshot{Revision: "static", Model: snapshot.model, DeferredTools: snapshot.authority.catalog, Validate: func(context.Context) (func(), error) { return func() {}, nil }}
	if resolve := snapshot.authority.resolveIdle; resolve != nil {
		fresh, err = resolve(ctx)
		if err != nil {
			return nil, err
		}
	}
	validParent := fresh.Validate != nil
	fresh = r.withMCPCheckpointAuthority(fresh)
	if !validParent || fresh.Revision != snapshot.authority.epoch || !sameCompactionModel(fresh.Model, snapshot.model) {
		if fresh.Release != nil {
			fresh.Release()
		}
		return nil, nil
	}
	release()
	authority := &modelRequestAuthority{epoch: fresh.Revision, admit: fresh.Admit, validate: fresh.Validate, catalog: fresh.DeferredTools, canSpeculate: true, refs: 1, release: fresh.Release}
	release = authority.drop
	snapshot.model = fresh.Model
	snapshot.authority = authority
	job := &asyncCompactJob{id: r.nextID("idle-compact", nil), source: loaded, frozen: frozen, llm: snapshot.model, epoch: snapshot.authority.epoch, requestHash: requestHash, coverage: compact.Coverage{SourceThroughSeq: final.Seq, SummarizedThroughSeq: session.LastEventSeq(delta), SummarizedThroughID: lastEventID(delta), SourceEventCount: len(delta), SourcePrefixHash: sourceHash, FrozenRequestHash: frozenHash, RequireInContext: true}}
	out := &IdleCompactionJob{runtime: r, candidate: job, authority: snapshot.authority, release: release, startedAt: snapshot.startedAt, preparedAt: r.compactionSnapshotTime(), turnID: snapshot.turnID, sourceStamp: stamp}
	if !out.Fresh() {
		return nil, nil
	}
	keep = true
	return out, nil
}

// Source supplies the fixed handoff proof for an atomic fence release.
func (j *IdleCompactionJob) Source(hostID, activationID string) session.IdleCompactionSource {
	_, base, _ := compact.LatestCompactEvent(j.candidate.source.Events)
	coverage := j.candidate.coverage
	return session.IdleCompactionSource{JobID: j.candidate.id, HostID: hostID, ActivationID: activationID, TurnID: j.turnID, Stamp: j.sourceStamp, ThroughSeq: coverage.SourceThroughSeq, Coverage: session.IdleCompactionCoverage{Revision: base.Revision + 1, EventCount: coverage.SourceEventCount, EndSeq: coverage.SummarizedThroughSeq, EndID: coverage.SummarizedThroughID}}
}

// Fresh checks dispatch wall time, not response completion or snapshot updates.
// A five-minute cache lifetime with a thirty-second margin is an experimental
// assumption for other channels, not a provider capability probe.
func (j *IdleCompactionJob) Fresh() bool {
	now := j.runtime.compactionSnapshotTime()
	age := time.Duration(now.UnixNano() - j.startedAt.UnixNano())
	wall := time.Duration(now.UnixNano() - j.preparedAt.UnixNano())
	drift := wall - now.Sub(j.preparedAt)
	return age >= 0 && age <= 270*time.Second && drift >= -5*time.Second && drift <= 5*time.Second
}

// Run performs one generation (at most two actual attempts), then one adoption.
// It returns only after the provider is drained. Accounting remains independent
// of candidate success, cancellation and foreground revocation.
func (j *IdleCompactionJob) Run(ctx context.Context, window session.IdleCompactionWindow) (bool, error) {
	if !j.Fresh() {
		return false, nil
	}
	if j.authority.validate == nil {
		return false, nil
	}
	release, err := j.authority.validate(ctx)
	if err != nil {
		return false, err
	}
	if release == nil {
		return false, agent.ErrModelRequestSnapshotStale
	}
	release()
	store := j.runtime.sessions.(session.CompactionReceiptStore)
	generation, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	generation = model.WithInvocationAdmission(generation, func(ctx context.Context, _ *model.Request) error {
		if len(j.attempts) >= 2 {
			return &idleCompactionAttemptLimit{}
		}
		if !j.Fresh() {
			return session.ErrIdleCompactionRevoked
		}
		if j.authority.admit != nil {
			return j.authority.admit(ctx, agent.ModelRequestAdmission{RequestID: j.runtime.nextID("idle-request", nil), JobID: j.candidate.id})
		}
		release, err := j.authority.validate(ctx)
		if err != nil {
			return err
		}
		if release == nil {
			return agent.ErrModelRequestSnapshotStale
		}
		release()
		return nil
	})
	generation = model.WithInvocationStart(generation, func(ctx context.Context, in model.Invocation, _ *model.Request) error {
		request := session.BeginCompactionAttemptRequest{Window: window, Invocation: in}
		ticket, err := store.BeginCompactionAttempt(ctx, request)
		if err != nil && !session.IsCommitted(err) {
			j.accountingErr = err
			j.uncertainBegin = &request
			return &idleCompactionAccountingError{err}
		}
		j.attempts = append(j.attempts, idleCompactionAttempt{ticket: ticket, invocation: in})
		return nil
	})
	generation = model.WithInvocationObserver(generation, func(in model.Invocation) {
		for i := range j.attempts {
			if j.attempts[i].invocation.ID == in.ID {
				j.attempts[i].invocation = in
				return
			}
		}
	})
	c := j.candidate
	c.result, c.err = j.runtime.compactor.(compact.ForceEngine).Force(generation, compact.Request{Session: c.source.Session, SessionRef: c.source.Session.SessionRef, Events: session.CloneEvents(c.source.Events), Model: c.llm, ServiceTier: c.frozen.ServiceTier, InContextRequest: c.frozen, Coverage: &c.coverage, RequireInContext: true}, "idle_candidate")
	cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	accountingErr := j.FlushAccounting(cleanup)
	cancelCleanup()
	if accountingErr != nil {
		return false, accountingErr
	}
	if c.err != nil {
		return false, c.err
	}
	if ctx.Err() != nil {
		j.keepReadyCandidate()
		return false, ctx.Err()
	}
	idleStore := j.runtime.sessions.(session.IdleCompactionStore)
	var grant *session.IdleCompactionGrant
	appendCheckpoint := func(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
		if grant == nil {
			value, err := idleStore.IssueIdleCompactionGrant(ctx, session.IssueIdleCompactionGrantRequest{Window: window, Event: req.Event})
			if err != nil && !session.IsCommitted(err) {
				return nil, err
			}
			grant = &value
		}
		return idleStore.AppendIdleCompactionCheckpoint(ctx, session.AppendIdleCompactionCheckpointRequest{Grant: *grant, Event: req.Event, ExpectedRevision: *req.ExpectedRevision})
	}
	l := &autoCompactGatedLLM{runtime: j.runtime, sessionRef: c.source.Session.SessionRef, inner: c.llm, requestAuthority: j.authority}
	adopted, err := j.runtime.adoptCompactionCandidate(ctx, l, c, "", appendCheckpoint)
	if !adopted && (errors.Is(err, session.ErrIdleCompactionRevoked) || errors.Is(err, context.Canceled) || errors.Is(err, errSessionWriteBusy)) {
		j.keepReadyCandidate()
	}
	return adopted, err
}

type idleCompactionAttemptLimit struct{}

func (*idleCompactionAttemptLimit) Error() string {
	return "runtime: idle compaction attempt limit reached"
}
func (*idleCompactionAttemptLimit) Retryable() bool { return false }

type idleCompactionAccountingError struct{ cause error }

func (e *idleCompactionAccountingError) Error() string {
	return "runtime: idle compaction accounting: " + e.cause.Error()
}
func (e *idleCompactionAccountingError) Unwrap() error { return e.cause }
func (*idleCompactionAccountingError) Retryable() bool { return false }

// FlushAccounting retries only original sealed invocation identities. Successful
// staging is durable, but an unconfirmed terminal payload retains this job.
func (j *IdleCompactionJob) FlushAccounting(ctx context.Context) error {
	store := j.runtime.sessions.(session.CompactionReceiptStore)
	if j.uncertainBegin != nil {
		if err := store.CancelCompactionReservation(ctx, *j.uncertainBegin); err != nil && !session.IsCommitted(err) {
			return err
		}
		j.uncertainBegin = nil
		j.accountingErr = nil
	}
	if j.accountingErr != nil {
		return j.accountingErr
	}
	for i := range j.attempts {
		attempt := &j.attempts[i]
		if attempt.finalized {
			continue
		}
		in := attempt.invocation
		if !attempt.staged {
			err := store.StageCompactionReceipt(ctx, attempt.ticket, session.CompactionAttemptResult{Usage: in.Usage, Outcome: in.Outcome, FinishedAt: in.FinishedAt})
			if err != nil && !session.IsCommitted(err) {
				return err
			}
			attempt.staged = true
		}
		if _, err := store.FinalizeCompactionReceipt(ctx, attempt.ticket); err != nil && !session.IsCommitted(err) {
			return err
		}
		attempt.finalized = true
	}
	return nil
}

func (j *IdleCompactionJob) keepReadyCandidate() {
	r := j.runtime
	r.idleCandidateMu.Lock()
	defer r.idleCandidateMu.Unlock()
	if j.candidate.err == nil && j.candidate.result.Compacted {
		now := r.compactionSnapshotTime()
		for key, value := range r.idleCandidates {
			age := now.Sub(value.readyAt)
			if age < 0 || age > 120*time.Second {
				delete(r.idleCandidates, key)
			}
		}
		if len(r.idleCandidates) >= 4 {
			oldestKey := ""
			var oldest time.Time
			for key, value := range r.idleCandidates {
				if oldestKey == "" || value.readyAt.Before(oldest) {
					oldestKey, oldest = key, value.readyAt
				}
			}
			delete(r.idleCandidates, oldestKey)
		}
		r.idleCandidates[compactionSessionKey(j.candidate.source.Session.SessionRef)] = &idleCompactionCandidate{job: j.candidate, readyAt: r.compactionSnapshotTime()}
	}
}

// Close releases the retained provider/configuration lease after provider drain.
func (j *IdleCompactionJob) Close() {
	if j != nil {
		j.closeOnce.Do(func() {
			if j.release != nil {
				j.release()
			}
		})
	}
}

func (l *autoCompactGatedLLM) adoptReadyIdleCandidate(ctx context.Context, req *model.Request) (bool, error) {
	r := l.runtime
	if !r.compaction.Idle.Enabled {
		return false, nil
	}
	key := compactionSessionKey(l.sessionRef)
	r.idleCandidateMu.Lock()
	candidate := r.idleCandidates[key]
	if candidate != nil {
		delete(r.idleCandidates, key)
	}
	r.idleCandidateMu.Unlock()
	if candidate == nil {
		return false, nil
	}
	job := candidate.job
	age := r.compactionSnapshotTime().Sub(candidate.readyAt)
	if age < 0 || age > 120*time.Second || l.requestAuthority == nil || job.epoch != l.requestAuthority.epoch || !sameCompactionModel(job.llm, l.inner) {
		return false, nil
	}
	hash, err := asyncRequestFingerprint(req)
	if err != nil || hash != job.requestHash {
		return false, err
	}
	job.guard = session.RuntimeMutationGuard(ctx)
	return r.adoptAsyncCompaction(ctx, l, job)
}
