package kernel

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
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
	ran     chan struct{}
}

type recordingExecutionValidator struct {
	request agent.RunRequest
	calls   int
	err     error
}

func (v *recordingExecutionValidator) ValidateExecutionRequest(req agent.RunRequest) error {
	v.calls++
	v.request = req
	return v.err
}

func (r *recordingRuntime) Run(_ context.Context, req agent.RunRequest) (agent.RunResult, error) {
	r.lastReq = req
	if r.ran != nil {
		select {
		case <-r.ran:
		default:
			close(r.ran)
		}
	}
	return r.result, nil
}

func (r *recordingRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type approvalRuntime struct {
	session  session.Session
	requests int
	mode     string
}

func (r *approvalRuntime) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	if req.ApprovalRequester == nil {
		return agent.RunResult{}, nil
	}
	requests := r.requests
	if requests <= 0 {
		requests = 1
	}
	for range requests {
		_, err := req.ApprovalRequester.RequestApproval(ctx, agent.ApprovalRequest{
			SessionRef: r.session.SessionRef,
			Session:    r.session,
			RunID:      "run-1",
			TurnID:     "turn-1",
			Tool:       tool.Definition{Name: "RUN_COMMAND"},
			Call:       tool.Call{ID: "approval-call", Name: "RUN_COMMAND"},
			Approval: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{
					ID:     "approval-call",
					Name:   "RUN_COMMAND",
					Kind:   "execute",
					Title:  "RUN_COMMAND test",
					Status: "pending",
				},
				Options: []session.ProtocolApprovalOption{
					{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
					{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
				},
			},
		})
		if err != nil {
			return agent.RunResult{}, err
		}
	}
	return agent.RunResult{
		Session: r.session,
		Handle: &recordingRunner{
			events: []*session.Event{{ID: "approved", Type: session.EventTypeNotice}},
		},
	}, nil
}

func (r *approvalRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type childApprovalRuntime struct {
	session   session.Session
	responses chan agent.ApprovalResponse
}

func (r *childApprovalRuntime) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	if req.ApprovalRequester == nil {
		return agent.RunResult{}, errors.New("missing approval requester")
	}
	response, err := req.ApprovalRequester.RequestApproval(ctx, agent.ApprovalRequest{
		SessionRef: r.session.SessionRef,
		Session:    r.session,
		RunID:      "run-1",
		TurnID:     "turn-1",
		Tool:       tool.Definition{Name: "WRITE"},
		Call:       tool.Call{ID: "shared-child-call", Name: "WRITE"},
		Approval: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{
				ID:        "shared-child-call",
				Name:      "WRITE",
				Kind:      "edit",
				Title:     "Write file",
				Status:    "pending",
				RawInput:  map[string]any{"path": "child.txt"},
				RawOutput: map[string]any{"preview": "new content"},
				Content: []session.ProtocolToolCallContent{{
					Type:    "content",
					Content: session.ProtocolTextContent("child permission detail"),
				}},
			},
			Options: []session.ProtocolApprovalOption{{ID: "allow_once", Name: "Allow once", Kind: "allow_once"}},
		},
		Metadata: map[string]any{
			"subagent":       true,
			"scope":          "subagent",
			"scope_id":       "task-1",
			"task_id":        "task-1",
			"parent_call_id": "spawn-call-1",
			"parent_tool":    "SPAWN",
		},
	})
	if err != nil {
		return agent.RunResult{}, err
	}
	select {
	case r.responses <- response:
	case <-ctx.Done():
		return agent.RunResult{}, ctx.Err()
	}
	return agent.RunResult{Session: r.session, Handle: &recordingRunner{}}, nil
}

func (r *childApprovalRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type serialApprovalApprover struct {
	started chan string
	release <-chan struct{}
}

func (a *serialApprovalApprover) Decide(ctx context.Context, req ApprovalReviewRequest) (ApprovalReviewResult, error) {
	if a != nil && a.started != nil {
		select {
		case a.started <- req.RuntimeRequest.Call.ID:
		case <-ctx.Done():
			return ApprovalReviewResult{}, ctx.Err()
		}
	}
	if a != nil && a.release != nil {
		select {
		case <-a.release:
		case <-ctx.Done():
			return ApprovalReviewResult{}, ctx.Err()
		}
	}
	return ApprovalReviewResult{
		Approved:       true,
		Outcome:        string(ApprovalStatusApproved),
		DecisionSource: string(ApprovalModeAutoReview),
	}, nil
}

func waitForApprovalQueueLength(t *testing.T, handle *turnHandle, want int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		got := len(handle.approvals.queueSnapshot())
		if got == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("approval queue length = %d, want %d", got, want)
		case <-tick.C:
		}
	}
}

type staticApprovalReviewer struct {
	result            ApprovalReviewResult
	sessionAccounting *UsageSnapshot
}

type recordingApprovalReviewer struct {
	req    ApprovalReviewRequest
	result ApprovalReviewResult
}

func (r *recordingApprovalReviewer) ReviewApproval(_ context.Context, req ApprovalReviewRequest) (ApprovalReviewResult, error) {
	r.req = req
	return r.result, nil
}

func (r staticApprovalReviewer) ReviewApproval(context.Context, ApprovalReviewRequest) (ApprovalReviewResult, error) {
	return r.result, nil
}

func (r staticApprovalReviewer) ApprovalReviewAccounting(context.Context, ApprovalReviewRequest, ApprovalReviewResult) (*UsageSnapshot, *session.EventInvocation, error) {
	return r.sessionAccounting, nil, nil
}

type contextBlockingApprovalReviewer struct {
	started chan struct{}
}

func (r *contextBlockingApprovalReviewer) ReviewApproval(ctx context.Context, _ ApprovalReviewRequest) (ApprovalReviewResult, error) {
	if r != nil && r.started != nil {
		close(r.started)
	}
	<-ctx.Done()
	return ApprovalReviewResult{}, ctx.Err()
}

type blockingRuntime struct {
	session session.Session
	wait    chan struct{}
}

func (r *blockingRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	if r.wait == nil {
		r.wait = make(chan struct{})
	}
	<-r.wait
	return agent.RunResult{
		Session: r.session,
		Handle:  &recordingRunner{},
	}, nil
}

func (r *blockingRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
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

type controlPlaneRuntime struct {
	session     session.Session
	runState    agent.RunState
	handoffReq  agent.HandoffControllerRequest
	handoffResp session.Session
	attachReq   agent.AttachParticipantRequest
	attachResp  session.Session
	promptReq   agent.PromptParticipantRequest
	promptResp  agent.RunResult
	promptErr   error
	detachReq   agent.DetachParticipantRequest
	detachResp  session.Session
	detachErr   error
	// detachBlock, when non-nil, blocks DetachParticipant until the request
	// context is cancelled or the channel is closed.
	detachBlock <-chan struct{}
}

func (r *controlPlaneRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{Session: r.session}, nil
}

func (r *controlPlaneRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return r.runState, nil
}

func (r *controlPlaneRuntime) HandoffController(_ context.Context, req agent.HandoffControllerRequest) (session.Session, error) {
	r.handoffReq = req
	return r.handoffResp, nil
}

func (r *controlPlaneRuntime) AttachParticipant(_ context.Context, req agent.AttachParticipantRequest) (session.Session, error) {
	r.attachReq = req
	return r.attachResp, nil
}

func (r *controlPlaneRuntime) PromptParticipant(_ context.Context, req agent.PromptParticipantRequest) (agent.RunResult, error) {
	r.promptReq = req
	if r.promptErr != nil {
		return agent.RunResult{}, r.promptErr
	}
	if r.promptResp.Handle != nil || r.promptResp.Session.SessionID != "" {
		return r.promptResp, nil
	}
	return agent.RunResult{Session: r.attachResp}, nil
}

func (r *controlPlaneRuntime) DetachParticipant(ctx context.Context, req agent.DetachParticipantRequest) (session.Session, error) {
	r.detachReq = req
	if r.detachBlock != nil {
		select {
		case <-ctx.Done():
			return session.Session{}, ctx.Err()
		case <-r.detachBlock:
		}
	}
	if r.detachErr != nil {
		return session.Session{}, r.detachErr
	}
	return r.detachResp, nil
}

type staticSessionService struct {
	session session.Session
	state   map[string]any
}

func (s staticSessionService) StartSession(context.Context, session.StartSessionRequest) (session.Session, error) {
	return s.session, nil
}

func (s staticSessionService) LoadSession(context.Context, session.LoadSessionRequest) (session.LoadedSession, error) {
	return session.LoadedSession{Session: s.session}, nil
}

func (s staticSessionService) Session(context.Context, session.SessionRef) (session.Session, error) {
	return s.session, nil
}

func (s staticSessionService) AppendEvent(_ context.Context, req session.AppendEventRequest) (*session.Event, error) {
	return storedTestEvent(req), nil
}
func (s staticSessionService) Events(context.Context, session.EventsRequest) ([]*session.Event, error) {
	return nil, nil
}
func (s staticSessionService) ListSessions(context.Context, session.ListSessionsRequest) (session.SessionList, error) {
	return session.SessionList{}, nil
}
func (s staticSessionService) BindController(context.Context, session.BindControllerRequest) (session.Session, error) {
	return s.session, nil
}
func (s staticSessionService) PutParticipant(context.Context, session.PutParticipantRequest) (session.Session, error) {
	return s.session, nil
}
func (s staticSessionService) RemoveParticipant(context.Context, session.RemoveParticipantRequest) (session.Session, error) {
	return s.session, nil
}
func (s staticSessionService) SnapshotState(context.Context, session.SessionRef) (map[string]any, error) {
	return cloneMap(s.state), nil
}
func (s staticSessionService) ReplaceState(context.Context, session.ReplaceStateRequest) (session.Session, error) {
	return s.session, nil
}
func (s staticSessionService) UpdateState(context.Context, session.UpdateStateRequest) (session.Session, error) {
	return s.session, nil
}

type mockSessionService struct{ staticSessionService }

type recordingSessionService struct {
	startReq           session.StartSessionRequest
	loadReq            session.LoadSessionRequest
	eventsReq          session.EventsRequest
	listReq            session.ListSessionsRequest
	sessionReq         session.SessionRef
	startSessionResult session.Session
	loadSessionResult  session.LoadedSession
	listSessionsResult session.SessionList
	listSessionsFn     func(session.ListSessionsRequest) session.SessionList
	sessionResult      session.Session
	eventsResult       []*session.Event
	snapshotErr        error
	startErr           error
	loadErr            error
	listErr            error
	sessionErr         error
	eventsErr          error
	loadCalls          int
	listCalls          int
	listRequests       []session.ListSessionsRequest
	sessionCalls       int
}

func (s *recordingSessionService) StartSession(_ context.Context, req session.StartSessionRequest) (session.Session, error) {
	s.startReq = req
	if s.startErr != nil {
		return session.Session{}, s.startErr
	}
	return s.startSessionResult, nil
}

func (s *recordingSessionService) LoadSession(_ context.Context, req session.LoadSessionRequest) (session.LoadedSession, error) {
	s.loadCalls++
	s.loadReq = req
	if s.loadErr != nil {
		return session.LoadedSession{}, s.loadErr
	}
	return s.loadSessionResult, nil
}

func (s *recordingSessionService) Session(_ context.Context, ref session.SessionRef) (session.Session, error) {
	s.sessionCalls++
	s.sessionReq = ref
	if s.sessionErr != nil {
		return session.Session{}, s.sessionErr
	}
	return s.sessionResult, nil
}

func (s *recordingSessionService) AppendEvent(_ context.Context, req session.AppendEventRequest) (*session.Event, error) {
	return storedTestEvent(req), nil
}

func storedTestEvent(req session.AppendEventRequest) *session.Event {
	event := session.CloneEvent(req.Event)
	if event == nil {
		return nil
	}
	event.SessionID = firstNonEmpty(strings.TrimSpace(event.SessionID), strings.TrimSpace(req.SessionRef.SessionID))
	event.ID = firstNonEmpty(strings.TrimSpace(event.ID), strings.TrimSpace(event.IdempotencyKey), "test-event")
	if event.Seq == 0 {
		event.Seq = 1
	}
	return event
}

func (s *recordingSessionService) Events(_ context.Context, req session.EventsRequest) ([]*session.Event, error) {
	s.eventsReq = req
	if s.eventsErr != nil {
		return nil, s.eventsErr
	}
	return append([]*session.Event(nil), s.eventsResult...), nil
}

func (s *recordingSessionService) ListSessions(_ context.Context, req session.ListSessionsRequest) (session.SessionList, error) {
	s.listCalls++
	s.listReq = req
	s.listRequests = append(s.listRequests, req)
	if s.listErr != nil {
		return session.SessionList{}, s.listErr
	}
	if s.listSessionsFn != nil {
		return s.listSessionsFn(req), nil
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
	if s.snapshotErr != nil {
		return nil, s.snapshotErr
	}
	return map[string]any{}, nil
}

func (s *recordingSessionService) ReplaceState(context.Context, session.ReplaceStateRequest) (session.Session, error) {
	return s.sessionResult, nil
}

func (s *recordingSessionService) UpdateState(context.Context, session.UpdateStateRequest) (session.Session, error) {
	return s.sessionResult, nil
}

type staticResolver struct {
	resolved ResolvedTurn
	err      error
}

type approvalModelResolverStub struct {
	staticResolver
	model model.LLM
	err   error
}

func (r approvalModelResolverStub) ResolveApprovalModel(context.Context, session.SessionRef) (model.LLM, error) {
	return r.model, r.err
}

func (r staticResolver) ResolveTurn(context.Context, TurnIntent) (ResolvedTurn, error) {
	if r.err != nil {
		return ResolvedTurn{}, r.err
	}
	return r.resolved, nil
}

type recordingResolver struct {
	resolved   ResolvedTurn
	lastIntent TurnIntent
	calls      int
	err        error
}

func (r *recordingResolver) ResolveTurn(_ context.Context, intent TurnIntent) (ResolvedTurn, error) {
	r.calls++
	r.lastIntent = intent
	if r.err != nil {
		return ResolvedTurn{}, r.err
	}
	return r.resolved, nil
}

type recordingControllerResolver struct {
	recordingResolver
	controllerResolved   ResolvedTurn
	lastControllerIntent TurnIntent
	controllerCalls      int
	controllerErr        error
}

func (r *recordingControllerResolver) ResolveControllerTurn(_ context.Context, intent TurnIntent) (ResolvedTurn, error) {
	r.controllerCalls++
	r.lastControllerIntent = intent
	if r.controllerErr != nil {
		return ResolvedTurn{}, r.controllerErr
	}
	resolved := r.controllerResolved
	resolved.RunRequest.SessionRef = intent.SessionRef
	resolved.RunRequest.Input = intent.Input
	resolved.RunRequest.ContentParts = append([]model.ContentPart(nil), intent.ContentParts...)
	return resolved, nil
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

type blockingRunner struct {
	release chan struct{}
}

func (blockingRunner) RunID() string { return "run-blocking" }

func (r blockingRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		<-r.release
	}
}

func (blockingRunner) Submit(agent.Submission) error { return nil }
func (blockingRunner) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (blockingRunner) Close() error { return nil }

type submitRecordingBlockingRunner struct {
	release     chan struct{}
	mu          sync.Mutex
	submissions []agent.Submission
}

func (r *submitRecordingBlockingRunner) RunID() string { return "run-submit-blocking" }

func (r *submitRecordingBlockingRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		<-r.release
	}
}

func (r *submitRecordingBlockingRunner) Submit(sub agent.Submission) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.submissions = append(r.submissions, agent.CloneSubmission(sub))
	return nil
}

func (r *submitRecordingBlockingRunner) snapshot() []agent.Submission {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]agent.Submission, 0, len(r.submissions))
	for _, submission := range r.submissions {
		out = append(out, agent.CloneSubmission(submission))
	}
	return out
}

func (r *submitRecordingBlockingRunner) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (r *submitRecordingBlockingRunner) Close() error { return nil }

type blockingCancelRunner struct {
	eventsStarted chan struct{}
	cancelled     chan struct{}
	release       chan struct{}
}

func (r *blockingCancelRunner) RunID() string { return "run-blocking-cancel" }

func (r *blockingCancelRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		close(r.eventsStarted)
		<-r.release
	}
}

func (r *blockingCancelRunner) Submit(agent.Submission) error { return nil }

func (r *blockingCancelRunner) Cancel() agent.CancelResult {
	select {
	case <-r.cancelled:
		return agent.CancelResult{Status: agent.CancelStatusAlreadyCancelled}
	default:
		close(r.cancelled)
		return agent.CancelResult{Status: agent.CancelStatusCancelled}
	}
}

func (r *blockingCancelRunner) Close() error { return nil }

func TestSanityTestClock(t *testing.T) {
	t.Parallel()
	if time.Unix(100, 0).IsZero() {
		t.Fatal("unexpected zero time")
	}
}

func collectHandleEvents(t *testing.T, handle TurnHandle) []eventstream.Envelope {
	t.Helper()

	var out []eventstream.Envelope
	timeout := time.After(2 * time.Second)
	for {
		select {
		case env, ok := <-handle.ACPEvents():
			if !ok {
				return out
			}
			out = append(out, env)
		case <-timeout:
			t.Fatalf("timed out waiting for handle events: %#v", out)
		}
	}
}

func firstUsageSnapshot(events []eventstream.Envelope) *eventstream.UsageSnapshot {
	for _, env := range events {
		if usage := eventstream.UsageSnapshotFromEnvelope(env); usage != nil {
			return usage
		}
	}
	return nil
}
