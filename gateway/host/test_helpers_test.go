package host

import (
	"context"
	"iter"

	gatewaycore "github.com/OnslaughtSnail/caelis/gateway/core"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

type mockRuntime struct{}

func (mockRuntime) Run(context.Context, sdkruntime.RunRequest) (sdkruntime.RunResult, error) {
	return sdkruntime.RunResult{}, nil
}

func (mockRuntime) RunState(context.Context, sdksession.SessionRef) (sdkruntime.RunState, error) {
	return sdkruntime.RunState{}, nil
}

type recordingRuntime struct {
	session sdksession.Session
	result  sdkruntime.RunResult
	lastReq sdkruntime.RunRequest
}

func (r *recordingRuntime) Run(_ context.Context, req sdkruntime.RunRequest) (sdkruntime.RunResult, error) {
	r.lastReq = req
	return r.result, nil
}

func (r *recordingRuntime) RunState(context.Context, sdksession.SessionRef) (sdkruntime.RunState, error) {
	return sdkruntime.RunState{}, nil
}

type cancellableRuntime struct {
	session   sdksession.Session
	cancelled chan struct{}
}

func (r *cancellableRuntime) Run(ctx context.Context, req sdkruntime.RunRequest) (sdkruntime.RunResult, error) {
	_ = req
	<-ctx.Done()
	close(r.cancelled)
	return sdkruntime.RunResult{}, ctx.Err()
}

func (r *cancellableRuntime) RunState(context.Context, sdksession.SessionRef) (sdkruntime.RunState, error) {
	return sdkruntime.RunState{}, nil
}

type recordingSessionService struct {
	startReq           sdksession.StartSessionRequest
	loadReq            sdksession.LoadSessionRequest
	listReq            sdksession.ListSessionsRequest
	sessionReq         sdksession.SessionRef
	startSessionResult sdksession.Session
	loadSessionResult  sdksession.LoadedSession
	listSessionsResult sdksession.SessionList
	sessionResult      sdksession.Session
	startErr           error
	loadErr            error
	listErr            error
	sessionErr         error
}

func (s *recordingSessionService) StartSession(_ context.Context, req sdksession.StartSessionRequest) (sdksession.Session, error) {
	s.startReq = req
	if s.startErr != nil {
		return sdksession.Session{}, s.startErr
	}
	return s.startSessionResult, nil
}

func (s *recordingSessionService) LoadSession(_ context.Context, req sdksession.LoadSessionRequest) (sdksession.LoadedSession, error) {
	s.loadReq = req
	if s.loadErr != nil {
		return sdksession.LoadedSession{}, s.loadErr
	}
	return s.loadSessionResult, nil
}

func (s *recordingSessionService) Session(_ context.Context, ref sdksession.SessionRef) (sdksession.Session, error) {
	s.sessionReq = ref
	if s.sessionErr != nil {
		return sdksession.Session{}, s.sessionErr
	}
	return s.sessionResult, nil
}

func (s *recordingSessionService) AppendEvent(_ context.Context, req sdksession.AppendEventRequest) (*sdksession.Event, error) {
	return req.Event, nil
}

func (s *recordingSessionService) Events(context.Context, sdksession.EventsRequest) ([]*sdksession.Event, error) {
	return nil, nil
}

func (s *recordingSessionService) ListSessions(_ context.Context, req sdksession.ListSessionsRequest) (sdksession.SessionList, error) {
	s.listReq = req
	if s.listErr != nil {
		return sdksession.SessionList{}, s.listErr
	}
	return s.listSessionsResult, nil
}

func (s *recordingSessionService) BindController(context.Context, sdksession.BindControllerRequest) (sdksession.Session, error) {
	return s.sessionResult, nil
}

func (s *recordingSessionService) PutParticipant(context.Context, sdksession.PutParticipantRequest) (sdksession.Session, error) {
	return s.sessionResult, nil
}

func (s *recordingSessionService) RemoveParticipant(context.Context, sdksession.RemoveParticipantRequest) (sdksession.Session, error) {
	return s.sessionResult, nil
}

func (s *recordingSessionService) SnapshotState(context.Context, sdksession.SessionRef) (map[string]any, error) {
	return map[string]any{}, nil
}

func (s *recordingSessionService) ReplaceState(context.Context, sdksession.SessionRef, map[string]any) error {
	return nil
}

func (s *recordingSessionService) UpdateState(context.Context, sdksession.SessionRef, func(map[string]any) (map[string]any, error)) error {
	return nil
}

type staticResolver struct {
	resolved gatewaycore.ResolvedTurn
}

func (r staticResolver) ResolveTurn(context.Context, gatewaycore.TurnIntent) (gatewaycore.ResolvedTurn, error) {
	return r.resolved, nil
}

type recordingResolver struct {
	resolved   gatewaycore.ResolvedTurn
	lastIntent gatewaycore.TurnIntent
}

func (r *recordingResolver) ResolveTurn(_ context.Context, intent gatewaycore.TurnIntent) (gatewaycore.ResolvedTurn, error) {
	r.lastIntent = intent
	return r.resolved, nil
}

type recordingRunner struct {
	submissions []sdkruntime.Submission
	events      []*sdksession.Event
	cancelled   bool
}

func (r *recordingRunner) RunID() string { return "run-1" }

func (r *recordingRunner) Events() iter.Seq2[*sdksession.Event, error] {
	events := append([]*sdksession.Event(nil), r.events...)
	return func(yield func(*sdksession.Event, error) bool) {
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func (r *recordingRunner) Submit(sub sdkruntime.Submission) error {
	r.submissions = append(r.submissions, sub)
	return nil
}

func (r *recordingRunner) Cancel() bool {
	if r.cancelled {
		return false
	}
	r.cancelled = true
	return true
}

func (r *recordingRunner) Close() error { return nil }
