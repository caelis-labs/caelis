package runtime

import (
	"context"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/requestsource"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// AsyncCompactionConfig enables an experimental, same-Turn candidate. At most
// one generation is launched per Turn. It never crosses a Runner completion or
// lends the Turn's fence to a later activation. Hosts must await Runner completion
// before releasing the fence, provider leases, or stores, including on Stop.
type AsyncCompactionConfig struct {
	Enabled bool
	// StartRatio is measured against the raw, explicitly known model context
	// window, not the effective input budget. Zero selects 0.65. Invalid values
	// or a threshold at/above the synchronous watermark disable speculation.
	StartRatio float64
	// GenerationTimeout bounds a speculative candidate, defaulting to 600 seconds.
	// A provider that ignores cancellation still holds completion until drained.
	GenerationTimeout time.Duration
	// FallbackTimeout bounds each actual foreground recovery generation. Zero
	// selects 600 seconds. Retry waiting uses the parent context, with no
	// Turn-wide quota. Cancellation still requires producer drain.
	FallbackTimeout time.Duration
}

type asyncCompactScopeKey struct{}

// Only the foreground accesses scope fields. The producer publishes its result
// and accounting error by closing done; it never installs a checkpoint.
type asyncCompactScope struct {
	mu      sync.Mutex
	ctx     context.Context
	job     *asyncCompactJob
	release func()
}

type asyncCompactJob struct {
	id          string
	source      session.LoadedSession
	coverage    compact.Coverage
	frozen      *model.Request
	requestHash string
	epoch       string
	llm         model.LLM
	guard       session.MutationGuard
	cancel      context.CancelFunc
	done        chan struct{}
	result      compact.Result
	err         error
	receiptErr  error
	discarded   bool
}

func asyncScope(ctx context.Context) *asyncCompactScope {
	s, _ := ctx.Value(asyncCompactScopeKey{}).(*asyncCompactScope)
	return s
}

func (s *asyncCompactScope) drain() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job != nil {
		s.job.cancel()
		// Cancellation is a request, not evidence of producer quiescence.
		<-s.job.done
	}
	if s.release != nil {
		s.release()
		s.release = nil
	}
	if s.job != nil {
		return s.job.receiptErr
	}
	return nil
}

type asyncCompactAdoptedError struct{ checkpoint *session.Event }

func (*asyncCompactAdoptedError) Error() string {
	return "runtime: rebuild after durable async compaction"
}

// A pre-dispatch boundary can adopt a checkpoint and ask the Run loop to
// rebuild from durable history. Launch only after actual attempt admission.
func (l *autoCompactGatedLLM) asyncCompactionBoundary(ctx context.Context, req *model.Request, hard, dispatch bool) (*session.Event, error) {
	r := l.runtime
	s := asyncScope(ctx)
	if !r.compaction.Async.Enabled || s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	epoch := "static"
	if l.requestAuthority != nil {
		epoch = l.requestAuthority.epoch
	}
	requestHash, err := asyncRequestFingerprint(req)
	if err != nil {
		return nil, err
	}
	if job := s.job; job != nil && !dispatch {
		if job.epoch != epoch || job.requestHash != requestHash || !sameCompactionModel(job.llm, l.inner) {
			job.discarded = true
			job.cancel()
		}
		select {
		case <-job.done:
			if job.receiptErr != nil {
				return nil, job.receiptErr
			}
			if !job.discarded && job.err == nil {
				job.discarded = true // exactly one adoption attempt
				adopted, err := r.adoptAsyncCompaction(ctx, l, job)
				if err != nil || adopted != nil {
					return adopted, err
				}
			}
		default:
		}
	}
	if hard {
		// Never wait speculatively at the hard gate. Cancel/drain the producer
		// before the existing bounded synchronous generation path takes over.
		if s.job != nil {
			s.job.discarded = true
			s.job.cancel()
			<-s.job.done
			if s.job.receiptErr != nil {
				return nil, s.job.receiptErr
			}
		}
		return nil, nil
	}
	if !dispatch || s.job != nil || req.Source == nil || !compactionPrefixSafe(req.Messages) {
		return nil, nil
	}
	messagesHash, err := requestsource.Fingerprint(req.Messages)
	if err != nil || messagesHash != req.Source.MessagesHash {
		return nil, nil
	}
	if l.requestAuthority != nil && l.requestAuthority.validate == nil {
		return nil, nil
	}
	provider, modelName := compactionModelIdentity(l.inner)
	if provider == "" || modelName == "" {
		return nil, nil
	}
	guard := session.RuntimeMutationGuard(ctx)
	if guard.Authority != session.MutationAuthorityRuntime || guard.FenceID == "" || epoch == "" {
		return nil, nil
	}
	validator, ok := r.sessions.(session.MutationGuardValidator)
	if !ok {
		return nil, nil
	}
	if err := validator.ValidateMutationGuard(ctx, l.sessionRef, guard); err != nil {
		return nil, err
	}
	windowProvider, ok := l.inner.(compact.ContextWindowProvider)
	if !ok || windowProvider.ContextWindowTokens() <= 0 {
		return nil, nil
	}
	loaded, err := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: l.sessionRef})
	if err != nil {
		return nil, err
	}
	events := mainInvocationEvents(loaded.Events)
	cut := 0
	for cut < len(events) && events[cut].Seq <= req.Source.SourceThroughSeq {
		cut++
	}
	events = events[:cut]
	prefix, err := requestsource.Fingerprint(events)
	if err != nil || prefix != req.Source.SourcePrefixHash || session.LastEventSeq(events) != req.Source.SourceThroughSeq {
		return nil, nil
	}
	delta := compact.EventsAfterLatestCompact(events)
	if len(delta) == 0 || !autoCompactGateHasModelProgress(delta) || !asyncToolPairsComplete(compact.PromptEventsFromLatestCompact(events)) {
		return nil, nil
	}
	usage, _ := usageForModelRequest(events, l.inner, req, r.compaction)
	ratio := r.compaction.Async.StartRatio
	if ratio == 0 {
		ratio = .65
	}
	threshold := ratio * float64(windowProvider.ContextWindowTokens())
	probe := usage
	probe.TotalTokens = int(threshold) + 1
	if !(ratio > 0 && ratio < 1) || evaluateWatermark(probe, r.compaction).ShouldCompact || float64(usage.TotalTokens) < threshold {
		return nil, nil
	}
	frozen := model.CloneRequest(req)
	frozenHash, err := requestsource.Fingerprint(frozen)
	if err != nil {
		return nil, nil
	}
	coverage := compact.Coverage{
		SourceThroughSeq:     req.Source.SourceThroughSeq,
		SummarizedThroughSeq: session.LastEventSeq(delta), SummarizedThroughID: lastEventID(delta),
		SourceEventCount: len(delta), SourcePrefixHash: prefix, FrozenRequestHash: frozenHash,
		RequireInContext: true,
	}
	loaded.Events = session.CloneEvents(events)
	timeout := r.compaction.Async.GenerationTimeout
	if timeout <= 0 {
		timeout = model.DefaultRequestTimeout
	}
	// Root at the Turn, not the request's ephemeral accounting/admission context.
	jobCtx, cancel := context.WithTimeout(s.ctx, timeout)
	job := &asyncCompactJob{
		id: r.nextID("async-compact", nil), source: loaded, coverage: coverage, frozen: frozen,
		requestHash: requestHash, epoch: epoch, llm: l.inner, guard: guard,
		cancel: cancel, done: make(chan struct{}),
	}
	s.job = job
	s.release = l.requestAuthority.retain()
	go func() {
		defer close(job.done)
		defer cancel()
		records := &compactionInvocations{}
		generationCtx := records.context(jobCtx)
		generationCtx = model.WithInvocationAdmission(generationCtx, func(ctx context.Context, _ *model.Request) error {
			return validator.ValidateMutationGuard(ctx, l.sessionRef, guard)
		})
		if a := l.requestAuthority; a != nil && a.admit != nil {
			generationCtx = model.WithInvocationAdmission(generationCtx, func(ctx context.Context, _ *model.Request) error {
				return a.admit(ctx, agent.ModelRequestAdmission{RequestID: r.nextID("compact-request", nil)})
			})
		}
		job.result, job.err = r.compactor.(compact.ForceEngine).Force(generationCtx, compact.Request{
			Session: loaded.Session, Events: session.CloneEvents(job.source.Events),
			Model: l.inner, ServiceTier: job.frozen.ServiceTier,
			InContextRequest: job.frozen, Coverage: &job.coverage, RequireInContext: true,
		}, "async_candidate")
		active := loaded.Session
		job.receiptErr = r.persistCompactionInvocations(jobCtx, &active, lifecycleScopeFromContext(s.ctx).turnID, records)
	}()
	return nil, nil
}

func (s *asyncCompactScope) beforeRecovery() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job != nil {
		s.job.discarded = true
		s.job.cancel()
		<-s.job.done
		if s.job.receiptErr != nil {
			return s.job.receiptErr
		}
	}
	return nil
}

func (r *Runtime) compactionRequestContext(ctx context.Context) context.Context {
	return model.WithRequestAttemptTimeout(ctx, r.compaction.Async.FallbackTimeout)
}

func asyncRequestFingerprint(req *model.Request) (string, error) {
	prefix := model.CloneRequest(req)
	prefix.Messages = nil
	// Unlike the usage estimate's media-stripped hash this binds exact bytes,
	// schema, reasoning, service tier, and output shape.
	return requestsource.Fingerprint(prefix)
}

// No pair may straddle the coverage cut. A standalone/unknown result or an
// incomplete batch is conservatively ineligible; no synthetic results are made.
func asyncToolPairsComplete(events []*session.Event) bool {
	pending := map[string]bool{}
	for _, event := range events {
		switch session.EventTypeOf(event) {
		case session.EventTypeToolCall:
			if event.Tool == nil || event.Tool.ID == "" || pending[event.Tool.ID] {
				return false
			}
			pending[event.Tool.ID] = true
		case session.EventTypeToolResult:
			if event.Tool == nil || !pending[event.Tool.ID] {
				return false
			}
			delete(pending, event.Tool.ID)
		}
	}
	return len(pending) == 0
}
