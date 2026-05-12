package host

import (
	"context"
	"iter"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type mockRuntime struct{}

func (mockRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{}, nil
}

func (mockRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type recordingRuntime struct {
	session session.Session
	result  agent.RunResult
	lastReq agent.RunRequest
}

func (r *recordingRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.lastReq = req
	return r.result, nil
}

func (r *recordingRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type cancellableRuntime struct {
	session   session.Session
	cancelled chan struct{}
}

func (r *cancellableRuntime) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	_ = req
	<-ctx.Done()
	close(r.cancelled)
	return agent.RunResult{}, ctx.Err()
}

func (r *cancellableRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type recordingSessionService struct {
	startReq           session.StartSessionRequest
	loadReq            session.LoadSessionRequest
	listReq            session.ListSessionsRequest
	sessionReq         session.SessionRef
	startSessionResult session.Session
	loadSessionResult  session.LoadedSession
	listSessionsResult session.SessionList
	sessionResult      session.Session
	startErr           error
	loadErr            error
	listErr            error
	sessionErr         error
}

func (s *recordingSessionService) StartSession(_ context.Context, req session.StartSessionRequest) (session.Session, error) {
	s.startReq = req
	if s.startErr != nil {
		return session.Session{}, s.startErr
	}
	return s.startSessionResult, nil
}

func (s *recordingSessionService) LoadSession(_ context.Context, req session.LoadSessionRequest) (session.LoadedSession, error) {
	s.loadReq = req
	if s.loadErr != nil {
		return session.LoadedSession{}, s.loadErr
	}
	return s.loadSessionResult, nil
}

func (s *recordingSessionService) Session(_ context.Context, ref session.SessionRef) (session.Session, error) {
	s.sessionReq = ref
	if s.sessionErr != nil {
		return session.Session{}, s.sessionErr
	}
	return s.sessionResult, nil
}

func (s *recordingSessionService) AppendEvent(_ context.Context, req session.AppendEventRequest) (*session.Event, error) {
	return req.Event, nil
}

func (s *recordingSessionService) Events(context.Context, session.EventsRequest) ([]*session.Event, error) {
	return nil, nil
}

func (s *recordingSessionService) ListSessions(_ context.Context, req session.ListSessionsRequest) (session.SessionList, error) {
	s.listReq = req
	if s.listErr != nil {
		return session.SessionList{}, s.listErr
	}
	return s.listSessionsResult, nil
}

func (s *recordingSessionService) BindController(context.Context, session.BindControllerRequest) (session.Session, error) {
	return s.sessionResult, nil
}

func (s *recordingSessionService) PutParticipant(context.Context, session.PutParticipantRequest) (session.Session, error) {
	return s.sessionResult, nil
}

func (s *recordingSessionService) RemoveParticipant(context.Context, session.RemoveParticipantRequest) (session.Session, error) {
	return s.sessionResult, nil
}

func (s *recordingSessionService) SnapshotState(context.Context, session.SessionRef) (map[string]any, error) {
	return map[string]any{}, nil
}

func (s *recordingSessionService) ReplaceState(context.Context, session.SessionRef, map[string]any) error {
	return nil
}

func (s *recordingSessionService) UpdateState(context.Context, session.SessionRef, func(map[string]any) (map[string]any, error)) error {
	return nil
}

type staticResolver struct {
	resolved kernel.ResolvedTurn
}

func (r staticResolver) ResolveTurn(context.Context, kernel.TurnIntent) (kernel.ResolvedTurn, error) {
	return r.resolved, nil
}

type recordingResolver struct {
	resolved   kernel.ResolvedTurn
	lastIntent kernel.TurnIntent
}

func (r *recordingResolver) ResolveTurn(_ context.Context, intent kernel.TurnIntent) (kernel.ResolvedTurn, error) {
	r.lastIntent = intent
	return r.resolved, nil
}

type recordingRunner struct {
	submissions []agent.Submission
	events      []*session.Event
	cancelled   bool
}

func (r *recordingRunner) RunID() string { return "run-1" }

func (r *recordingRunner) Events() iter.Seq2[*session.Event, error] {
	events := append([]*session.Event(nil), r.events...)
	return func(yield func(*session.Event, error) bool) {
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func (r *recordingRunner) Submit(sub agent.Submission) error {
	r.submissions = append(r.submissions, sub)
	return nil
}

func (r *recordingRunner) Cancel() agent.CancelResult {
	if r.cancelled {
		return agent.CancelResult{Status: agent.CancelStatusAlreadyCancelled}
	}
	r.cancelled = true
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}

func (r *recordingRunner) Close() error { return nil }
