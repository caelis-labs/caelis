// Package gateway implements the new ACP-native runtime.Engine skeleton over
// core contracts.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	engineapproval "github.com/OnslaughtSnail/caelis/internal/engine/approval"
	"github.com/OnslaughtSnail/caelis/internal/engine/loop"
)

type Runner interface {
	Run(context.Context, loop.Request) ([]session.Event, error)
}

type Config struct {
	Store       session.Store
	Runner      Runner
	Clock       func() time.Time
	IDGenerator func(string) string
}

type Gateway struct {
	store       session.Store
	runner      Runner
	clock       func() time.Time
	idGenerator func(string) string
	nextID      atomic.Uint64
	mu          sync.Mutex
	active      map[string]*turn
}

func New(cfg Config) (*Gateway, error) {
	if cfg.Store == nil {
		return nil, errors.New("engine/gateway: session store is required")
	}
	if cfg.Runner == nil {
		return nil, errors.New("engine/gateway: runner is required")
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	g := &Gateway{
		store:       cfg.Store,
		runner:      cfg.Runner,
		clock:       cfg.Clock,
		idGenerator: cfg.IDGenerator,
		active:      map[string]*turn{},
	}
	return g, nil
}

func (g *Gateway) StartSession(ctx context.Context, req session.StartRequest) (session.Session, error) {
	return g.store.Create(ctx, req)
}

func (g *Gateway) ListSessions(ctx context.Context, query session.ListQuery) (session.SessionPage, error) {
	return g.store.List(ctx, query)
}

func (g *Gateway) LoadSession(ctx context.Context, ref session.Ref) (session.Snapshot, error) {
	return g.store.Load(ctx, ref)
}

func (g *Gateway) RecordEvents(ctx context.Context, ref session.Ref, events []session.Event) (session.Cursor, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ref = session.NormalizeRef(ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return "", fmt.Errorf("%w: session id is required", session.ErrInvalid)
	}
	snapshot, err := g.store.Load(ctx, ref)
	if err != nil {
		return "", err
	}
	events = normalizeEventsForSession(snapshot.Session.Ref.SessionID, events)
	return g.store.Append(ctx, snapshot.Session.Ref, events)
}

func (g *Gateway) UpdateSessionState(ctx context.Context, ref session.Ref, patch session.StatePatch) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ref = session.NormalizeRef(ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return fmt.Errorf("%w: session id is required", session.ErrInvalid)
	}
	snapshot, err := g.store.Load(ctx, ref)
	if err != nil {
		return err
	}
	return g.store.UpdateState(ctx, snapshot.Session.Ref, patch)
}

func (g *Gateway) BeginTurn(ctx context.Context, req coreruntime.TurnRequest) (coreruntime.Turn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ref := session.NormalizeRef(req.SessionRef)
	if strings.TrimSpace(ref.SessionID) == "" {
		return nil, fmt.Errorf("%w: session id is required", session.ErrInvalid)
	}
	snapshot, err := g.store.Load(ctx, ref)
	if err != nil {
		return nil, err
	}
	runID := g.next("run")
	turnID := g.next("turn")
	runCtx, cancel := context.WithCancel(ctx)
	handle := &turn{
		id:          turnID,
		runID:       runID,
		sessionRef:  snapshot.Session.Ref,
		startedAt:   g.clock(),
		cancel:      cancel,
		events:      make(chan coreruntime.EventEnvelope, 32),
		submissions: make(chan coreruntime.Submission, 8),
		done:        make(chan struct{}),
	}
	g.register(handle)
	go g.execute(runCtx, snapshot, req, handle)
	return handle, nil
}

func (g *Gateway) Interrupt(ctx context.Context, ref session.Ref) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ref = session.NormalizeRef(ref)
	g.mu.Lock()
	handle := g.active[ref.SessionID]
	g.mu.Unlock()
	if handle == nil {
		return fmt.Errorf("%w for session %q", coreruntime.ErrNoActiveTurn, ref.SessionID)
	}
	handle.Cancel()
	return nil
}

func (g *Gateway) Replay(ctx context.Context, req coreruntime.ReplayRequest) (<-chan coreruntime.EventEnvelope, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	page, err := g.store.Events(ctx, session.EventQuery{
		Ref:              req.SessionRef,
		After:            req.After,
		Limit:            req.Limit,
		IncludeTransient: req.IncludeTransient,
	})
	if err != nil {
		return nil, err
	}
	out := make(chan coreruntime.EventEnvelope, len(page.Events))
	go func() {
		defer close(out)
		for _, event := range page.Events {
			select {
			case out <- coreruntime.EventEnvelope{Cursor: page.NextCursor, Event: event}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (g *Gateway) execute(ctx context.Context, snapshot session.Snapshot, req coreruntime.TurnRequest, handle *turn) {
	defer close(handle.events)
	defer close(handle.done)
	defer g.unregister(handle)
	runState := maps.Clone(snapshot.State)
	if runState == nil {
		runState = session.State{}
	}
	events, err := g.runner.Run(ctx, loop.Request{
		Session:      snapshot.Session,
		Events:       snapshot.Events,
		State:        runState,
		Input:        req.Input,
		ContentParts: req.ContentParts,
		Instructions: req.Instructions,
		Model:        req.Model,
		Reasoning:    req.Reasoning,
		Mode:         req.Mode,
		TurnID:       handle.id,
		Surface:      req.Surface,
		StartedAt:    handle.startedAt,
		Emit: func(_ context.Context, events []session.Event) error {
			return g.emitEvents(ctx, snapshot.Session.Ref, handle, events)
		},
		AwaitApproval: func(waitCtx context.Context, event session.Event) (session.ApprovalEvent, error) {
			result, err := g.awaitApproval(waitCtx, snapshot.Session.Ref, handle, event)
			if err != nil {
				return session.ApprovalEvent{}, err
			}
			if err := g.rememberApprovalDecision(ctx, snapshot.Session.Ref, runState, result); err != nil {
				return session.ApprovalEvent{}, err
			}
			return result, nil
		},
	})
	if err != nil {
		handle.publish(coreruntime.EventEnvelope{Err: err.Error()})
		return
	}
	events = normalizeEventsForSession(snapshot.Session.Ref.SessionID, events)
	cursor, err := g.store.Append(context.WithoutCancel(ctx), snapshot.Session.Ref, events)
	if err != nil {
		handle.publish(coreruntime.EventEnvelope{Err: err.Error()})
		return
	}
	for _, event := range events {
		handle.publish(coreruntime.EventEnvelope{Cursor: cursor, Event: event})
	}
}

func (g *Gateway) emitEvents(ctx context.Context, ref session.Ref, handle *turn, events []session.Event) error {
	if len(events) == 0 {
		return nil
	}
	events = normalizeEventsForSession(ref.SessionID, events)
	cursor, err := g.store.Append(context.WithoutCancel(ctx), ref, events)
	if err != nil {
		return err
	}
	for _, event := range events {
		handle.publish(coreruntime.EventEnvelope{Cursor: cursor, Event: event})
	}
	return nil
}

func (g *Gateway) awaitApproval(ctx context.Context, ref session.Ref, handle *turn, event session.Event) (session.ApprovalEvent, error) {
	if event.Approval == nil {
		return session.ApprovalEvent{}, errors.New("engine/gateway: approval event is required")
	}
	if err := g.emitEvents(ctx, ref, handle, []session.Event{event}); err != nil {
		return session.ApprovalEvent{}, err
	}
	for {
		select {
		case <-ctx.Done():
			return session.ApprovalEvent{}, ctx.Err()
		case <-handle.done:
			return session.ApprovalEvent{}, errors.New("engine/gateway: turn closed before approval decision")
		case submission := <-handle.submissions:
			if submission.Kind != coreruntime.SubmissionApproval || submission.Approval == nil {
				continue
			}
			return approvalFromSubmission(*event.Approval, *submission.Approval), nil
		}
	}
}

func (g *Gateway) rememberApprovalDecision(ctx context.Context, ref session.Ref, state session.State, result session.ApprovalEvent) error {
	if result.Tool == nil {
		return nil
	}
	toolName := strings.TrimSpace(result.Tool.Name)
	optionID := strings.TrimSpace(result.Decision)
	reason := strings.TrimSpace(result.Reason)
	if !engineapproval.RememberToolDecision(state, toolName, optionID, reason) {
		return nil
	}
	return g.store.UpdateState(context.WithoutCancel(ctx), ref, engineapproval.RememberToolDecisionPatch(toolName, optionID, reason))
}

func approvalFromSubmission(pending session.ApprovalEvent, decision coreruntime.ApprovalDecision) session.ApprovalEvent {
	out := pending
	optionID := strings.TrimSpace(decision.OptionID)
	out.Decision = optionID
	out.Reason = strings.TrimSpace(decision.Reason)
	if out.Reason == "" {
		out.Reason = strings.TrimSpace(decision.Outcome)
	}
	normalizedOption := strings.ToLower(optionID)
	normalizedOutcome := strings.ToLower(strings.TrimSpace(decision.Outcome))
	if decision.Approved || strings.HasPrefix(normalizedOutcome, "allow") || strings.HasPrefix(normalizedOption, "allow") {
		out.Status = session.ApprovalApproved
		return out
	}
	out.Status = session.ApprovalRejected
	return out
}

func normalizeEventsForSession(sessionID string, events []session.Event) []session.Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]session.Event, 0, len(events))
	for _, event := range events {
		next := session.CloneEvent(event)
		if strings.TrimSpace(next.SessionID) == "" {
			next.SessionID = strings.TrimSpace(sessionID)
		}
		if next.Visibility == "" {
			next.Visibility = session.VisibilityCanonical
		}
		out = append(out, next)
	}
	return out
}

func (g *Gateway) register(handle *turn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active[handle.sessionRef.SessionID] = handle
}

func (g *Gateway) unregister(handle *turn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active[handle.sessionRef.SessionID] == handle {
		delete(g.active, handle.sessionRef.SessionID)
	}
}

func (g *Gateway) next(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if g.idGenerator != nil {
		if id := strings.TrimSpace(g.idGenerator(prefix)); id != "" {
			return id
		}
	}
	return fmt.Sprintf("%s-%d", prefix, g.nextID.Add(1))
}

type turn struct {
	id          string
	runID       string
	sessionRef  session.Ref
	startedAt   time.Time
	cancel      context.CancelFunc
	events      chan coreruntime.EventEnvelope
	submissions chan coreruntime.Submission
	done        chan struct{}
	cancelOnce  sync.Once
}

func (t *turn) ID() string {
	return t.id
}

func (t *turn) RunID() string {
	return t.runID
}

func (t *turn) SessionRef() session.Ref {
	return session.NormalizeRef(t.sessionRef)
}

func (t *turn) StartedAt() time.Time {
	return t.startedAt
}

func (t *turn) Events() <-chan coreruntime.EventEnvelope {
	return t.events
}

func (t *turn) Submit(ctx context.Context, submission coreruntime.Submission) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if submission.Kind == "" {
		submission.Kind = coreruntime.SubmissionConversation
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.done:
		return errors.New("engine/gateway: turn is closed")
	case t.submissions <- submission:
		return nil
	}
}

func (t *turn) Cancel() coreruntime.CancelResult {
	t.cancelOnce.Do(func() {
		if t.cancel != nil {
			t.cancel()
		}
	})
	return coreruntime.CancelResult{Status: coreruntime.CancelCancelled}
}

func (t *turn) Close() error {
	t.Cancel()
	<-t.done
	return nil
}

func (t *turn) publish(env coreruntime.EventEnvelope) {
	select {
	case t.events <- env:
	default:
		t.events <- env
	}
}
