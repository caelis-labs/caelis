package kernel

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	policyapi "github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func modelMessagePtr(message model.Message) *model.Message {
	return &message
}

func TestNewRequiresSessionsRuntimeAndResolver(t *testing.T) {
	t.Parallel()

	base := Config{
		Sessions: mockSessionService{},
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	}
	cases := []struct {
		name string
		cfg  Config
	}{
		{name: "missing sessions", cfg: Config{Runtime: base.Runtime, Resolver: base.Resolver}},
		{name: "missing runtime", cfg: Config{Sessions: base.Sessions, Resolver: base.Resolver}},
		{name: "missing resolver", cfg: Config{Sessions: base.Sessions, Runtime: base.Runtime}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(tc.cfg); err == nil {
				t.Fatalf("New(%s) error = nil, want non-nil", tc.name)
			}
		})
	}
}

type failingTurnStartGate struct{ err error }

func (g failingTurnStartGate) Wait(context.Context) error { return g.err }

func TestTurnEntryPointsHonorControlStartupGate(t *testing.T) {
	t.Parallel()

	want := errors.New("approval recovery incomplete")
	svc := &recordingSessionService{}
	gw, err := New(Config{
		Sessions: svc, Runtime: mockRuntime{}, Resolver: staticResolver{}, TurnStartGate: failingTurnStartGate{err: want},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gw.BeginTurn(context.Background(), BeginTurnRequest{}); !errors.Is(err, want) {
		t.Fatalf("BeginTurn() error = %v, want recovery gate error", err)
	}
	if _, err := gw.PromptParticipant(context.Background(), PromptParticipantRequest{}); !errors.Is(err, want) {
		t.Fatalf("PromptParticipant() error = %v, want recovery gate error", err)
	}
	if _, err := gw.StartParticipant(context.Background(), StartParticipantRequest{}); !errors.Is(err, want) {
		t.Fatalf("StartParticipant() error = %v, want recovery gate error", err)
	}
	if svc.sessionReq.SessionID != "" || svc.loadCalls != 0 || svc.listCalls != 0 {
		t.Fatalf("session I/O occurred before recovery gate: session=%+v loads=%d lists=%d", svc.sessionReq, svc.loadCalls, svc.listCalls)
	}
}

func TestStartSessionDelegatesToSDKSessions(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		CWD: "/tmp/ws",
	}
	svc := staticSessionService{session: activeSession}
	gw, err := New(Config{
		Sessions: svc,
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	started, err := gw.StartSession(context.Background(), StartSessionRequest{
		AppName: "caelis",
		UserID:  "u",
		Workspace: session.WorkspaceRef{
			Key: "ws",
			CWD: "/tmp/ws",
		},
		PreferredSessionID: "s1",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	if started.SessionID != "s1" || started.CWD != "/tmp/ws" {
		t.Fatalf("StartSession() = %+v", started)
	}
}

func TestLoadSessionDelegatesToSDKSessionsAndBinds(t *testing.T) {
	t.Parallel()

	loaded := session.LoadedSession{
		Session: session.Session{
			SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s2", WorkspaceKey: "ws",
			},
			CWD: "/tmp/ws",
		},
	}
	svc := &recordingSessionService{loadSessionResult: loaded}
	gw, err := New(Config{
		Sessions: svc,
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	got, err := gw.LoadSession(context.Background(), LoadSessionRequest{
		SessionRef: loaded.Session.SessionRef,
		Limit:      32,
		BindingKey: "surface-headless",
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got.Session.SessionID != "s2" || svc.loadReq.Limit != 32 {
		t.Fatalf("LoadSession() = %+v, loadReq = %+v", got, svc.loadReq)
	}
	current, ok := gw.CurrentSession("surface-headless")
	if !ok || current.SessionID != "s2" {
		t.Fatalf("CurrentSession() = %+v, %v", current, ok)
	}
}

func TestListSessionsDelegatesToSDKSessions(t *testing.T) {
	t.Parallel()

	want := session.SessionList{
		Sessions: []session.SessionSummary{{SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s2", WorkspaceKey: "ws",
		}}},
	}
	svc := &recordingSessionService{listSessionsResult: want}
	gw, err := New(Config{
		Sessions: svc,
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	got, err := gw.ListSessions(context.Background(), ListSessionsRequest{
		AppName:      "caelis",
		UserID:       "u",
		WorkspaceKey: "ws",
		Limit:        5,
	})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].SessionID != "s2" {
		t.Fatalf("ListSessions() = %+v", got)
	}
	if svc.listReq.Limit != 5 || svc.listReq.WorkspaceKey != "ws" {
		t.Fatalf("listReq = %+v", svc.listReq)
	}
}

func TestListSessionsHidesSystemManagedSessions(t *testing.T) {
	t.Parallel()

	svc := &recordingSessionService{listSessionsResult: session.SessionList{
		Sessions: []session.SessionSummary{
			{SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "visible", WorkspaceKey: "ws"}, Title: "Visible task"},
			{
				SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "guardian", WorkspaceKey: "ws"},
				Title:      "Guardian approval review",
				Metadata:   map[string]any{"system_managed_agent": "guardian"},
			},
		},
	}}
	gw, err := New(Config{
		Sessions: svc,
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	got, err := gw.ListSessions(context.Background(), ListSessionsRequest{
		AppName:      "caelis",
		UserID:       "u",
		WorkspaceKey: "ws",
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].SessionID != "visible" {
		t.Fatalf("ListSessions() = %+v, want only visible session", got)
	}
}

func TestListSessionsFillsVisibleLimitAcrossHiddenRawPages(t *testing.T) {
	t.Parallel()

	visibleOne := session.SessionSummary{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "visible-1", WorkspaceKey: "ws"},
		Title:      "Visible one",
	}
	visibleTwo := session.SessionSummary{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "visible-2", WorkspaceKey: "ws"},
		Title:      "Visible two",
	}
	visibleThree := session.SessionSummary{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "visible-3", WorkspaceKey: "ws"},
		Title:      "Visible three",
	}
	hidden := session.SessionSummary{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "guardian", WorkspaceKey: "ws"},
		Title:      "Guardian approval review", Metadata: map[string]any{"system_managed_agent": "guardian"},
	}
	svc := &recordingSessionService{
		listSessionsFn: func(req session.ListSessionsRequest) session.SessionList {
			switch req.Cursor {
			case "":
				return session.SessionList{Sessions: []session.SessionSummary{hidden}, NextCursor: "raw-1"}
			case "raw-1":
				return session.SessionList{Sessions: []session.SessionSummary{visibleOne}, NextCursor: "raw-2"}
			case "raw-2":
				return session.SessionList{Sessions: []session.SessionSummary{visibleTwo}, NextCursor: "raw-3"}
			case "raw-3":
				return session.SessionList{Sessions: []session.SessionSummary{visibleThree}}
			default:
				return session.SessionList{}
			}
		},
	}
	gw, err := New(Config{Sessions: svc, Runtime: mockRuntime{}, Resolver: staticResolver{}})
	if err != nil {
		t.Fatal(err)
	}

	first, err := gw.ListSessions(context.Background(), ListSessionsRequest{
		AppName: "caelis", UserID: "u", WorkspaceKey: "ws", Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Sessions) != 2 {
		t.Fatalf("first visible page = %#v, want two Sessions", first)
	}
	if got := []string{first.Sessions[0].SessionID, first.Sessions[1].SessionID}; !reflect.DeepEqual(got, []string{"visible-1", "visible-2"}) {
		t.Fatalf("first visible page = %v", got)
	}
	if first.NextCursor != "raw-3" {
		t.Fatalf("first visible page cursor = %q, want raw-3", first.NextCursor)
	}
	if len(svc.listRequests) != 3 || svc.listRequests[0].Limit != 2 || svc.listRequests[1].Limit != 2 || svc.listRequests[2].Limit != 1 {
		t.Fatalf("raw page requests = %#v", svc.listRequests)
	}

	second, err := gw.ListSessions(context.Background(), ListSessionsRequest{
		AppName: "caelis", UserID: "u", WorkspaceKey: "ws", Cursor: first.NextCursor, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Sessions) != 1 || second.Sessions[0].SessionID != "visible-3" || second.NextCursor != "" {
		t.Fatalf("second visible page = %#v", second)
	}
}

func TestListSessionsDoesNotExposeLegacyGuardianWhenClassificationReadFails(t *testing.T) {
	t.Parallel()

	svc := &recordingSessionService{
		listSessionsResult: session.SessionList{Sessions: []session.SessionSummary{{
			SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "legacy-approval-review", WorkspaceKey: "ws"},
			Title:      "Guardian approval review",
		}}},
		sessionErr: context.DeadlineExceeded,
	}
	gw, err := New(Config{Sessions: svc, Runtime: mockRuntime{}, Resolver: staticResolver{}})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := gw.ListSessions(context.Background(), ListSessionsRequest{
		AppName: "caelis", UserID: "u", WorkspaceKey: "ws", Limit: 1,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ListSessions() = %#v, %v, want classification read deadline", listed, err)
	}
	if len(listed.Sessions) != 0 {
		t.Fatalf("ListSessions() = %#v, want no fail-open Guardian Session", listed)
	}
}

func TestResumeSessionUsesMostRecentExcludingCurrentBinding(t *testing.T) {
	t.Parallel()

	current := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		CWD: "/tmp/ws",
	}
	next := session.LoadedSession{
		Session: session.Session{
			SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s2", WorkspaceKey: "ws",
			},
			CWD: "/tmp/ws",
		},
	}
	svc := &recordingSessionService{
		startSessionResult: current,
		loadSessionResult:  next,
		listSessionsResult: session.SessionList{
			Sessions: []session.SessionSummary{
				{SessionRef: current.SessionRef, UpdatedAt: time.Unix(200, 0)},
				{SessionRef: next.Session.SessionRef, UpdatedAt: time.Unix(100, 0)},
			},
		},
	}
	gw, err := New(Config{
		Sessions: svc,
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := gw.StartSession(context.Background(), StartSessionRequest{
		AppName:    "caelis",
		UserID:     "u",
		Workspace:  session.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
		BindingKey: "surface-1",
	}); err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	loaded, err := gw.ResumeSession(context.Background(), ResumeSessionRequest{
		AppName:    "caelis",
		UserID:     "u",
		Workspace:  session.WorkspaceRef{Key: "ws"},
		BindingKey: "surface-1",
	})
	if err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if loaded.Session.SessionID != "s2" || svc.loadReq.SessionRef.SessionID != "s2" {
		t.Fatalf("ResumeSession() = %+v, loadReq = %+v", loaded, svc.loadReq)
	}
	currentRef, ok := gw.CurrentSession("surface-1")
	if !ok || currentRef.SessionID != "s2" {
		t.Fatalf("CurrentSession() = %+v, %v", currentRef, ok)
	}
}

func TestResumeSessionResolvesUniquePrefix(t *testing.T) {
	t.Parallel()

	target := session.LoadedSession{
		Session: session.Session{
			SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s-12345678", WorkspaceKey: "ws",
			},
		},
	}
	svc := &recordingSessionService{
		loadSessionResult: target,
		listSessionsResult: session.SessionList{
			Sessions: []session.SessionSummary{
				{SessionRef: target.Session.SessionRef},
				{SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "s-87654321", WorkspaceKey: "ws"}},
			},
		},
	}
	gw, err := New(Config{
		Sessions: svc,
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := gw.ResumeSession(context.Background(), ResumeSessionRequest{
		AppName:   "caelis",
		UserID:    "u",
		Workspace: session.WorkspaceRef{Key: "ws"},
		SessionID: "s-1234",
	}); err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if svc.loadReq.SessionRef.SessionID != "s-12345678" {
		t.Fatalf("loadReq = %+v", svc.loadReq)
	}
	if svc.listReq.WorkspaceKey != "" {
		t.Fatalf("listReq.WorkspaceKey = %q, want global SessionID lookup", svc.listReq.WorkspaceKey)
	}
}

func TestResumeSessionResolvesUniquePrefixBeyondFirstTwoHundredSessions(t *testing.T) {
	t.Parallel()

	target := session.LoadedSession{Session: session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "u", SessionID: "s-tail-unique-session", WorkspaceKey: "ws",
	}}}
	firstPage := make([]session.SessionSummary, 0, 200)
	for i := 0; i < 200; i++ {
		firstPage = append(firstPage, session.SessionSummary{SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: fmt.Sprintf("s-recent-%03d", i), WorkspaceKey: "ws",
		}})
	}
	svc := &recordingSessionService{
		loadSessionResult: target,
		listSessionsFn: func(req session.ListSessionsRequest) session.SessionList {
			if req.Cursor == "" {
				return session.SessionList{Sessions: firstPage, NextCursor: "page-2"}
			}
			if req.Cursor == "page-2" {
				return session.SessionList{Sessions: []session.SessionSummary{{SessionRef: target.Session.SessionRef}}}
			}
			return session.SessionList{}
		},
	}
	gw, err := New(Config{Sessions: svc, Runtime: mockRuntime{}, Resolver: staticResolver{}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := gw.ResumeSession(context.Background(), ResumeSessionRequest{
		AppName: "caelis", UserID: "u", SessionID: "s-tail-unique",
	})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Session.SessionID != target.Session.SessionID || svc.loadReq.SessionRef.SessionID != target.Session.SessionID {
		t.Fatalf("ResumeSession() = %#v, load request %#v", loaded, svc.loadReq)
	}
	if len(svc.listRequests) != 2 || svc.listRequests[1].Cursor != "page-2" {
		t.Fatalf("ListSessions() requests = %#v, want second page", svc.listRequests)
	}
}

func TestResumeSessionLoadsExactGlobalIDWithoutListingAndChecksScope(t *testing.T) {
	t.Parallel()

	target := session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "user-1", SessionID: "s-exact", WorkspaceKey: "workspace-1",
	}}
	svc := &recordingSessionService{
		sessionResult: target,
		listSessionsResult: session.SessionList{Sessions: []session.SessionSummary{{
			SessionRef: target.SessionRef,
		}}},
	}
	gw, err := New(Config{Sessions: svc, Runtime: mockRuntime{}, Resolver: staticResolver{}})
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := gw.ResumeSession(context.Background(), ResumeSessionRequest{
		AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "workspace-1"}, SessionID: "s-exact", MetadataOnly: true,
	})
	if err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if loaded.Session.SessionID != "s-exact" {
		t.Fatalf("ResumeSession() = %#v", loaded)
	}
	if svc.listCalls != 0 {
		t.Fatalf("ListSessions() calls = %d, want 0 for an exact global SessionID", svc.listCalls)
	}
	if svc.loadCalls != 0 || svc.sessionCalls != 1 {
		t.Fatalf("history loads = %d metadata reads = %d, want 0 and 1", svc.loadCalls, svc.sessionCalls)
	}

	for _, mismatch := range []ResumeSessionRequest{
		{AppName: "other-app", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "workspace-1"}, SessionID: "s-exact", MetadataOnly: true},
		{AppName: "caelis", UserID: "other-user", Workspace: session.WorkspaceRef{Key: "workspace-1"}, SessionID: "s-exact", MetadataOnly: true},
		{AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "other-workspace"}, SessionID: "s-exact", MetadataOnly: true},
	} {
		if _, err := gw.ResumeSession(context.Background(), mismatch); err == nil {
			t.Fatalf("ResumeSession(%+v) error = nil, want scoped not-found error", mismatch)
		}
	}
}

func TestResumeSessionRejectsAmbiguousPrefix(t *testing.T) {
	t.Parallel()

	svc := &recordingSessionService{
		listSessionsResult: session.SessionList{
			Sessions: []session.SessionSummary{
				{SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "s-12345678", WorkspaceKey: "ws"}},
				{SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "s-12349999", WorkspaceKey: "ws"}},
			},
		},
	}
	gw, err := New(Config{
		Sessions: svc,
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = gw.ResumeSession(context.Background(), ResumeSessionRequest{
		AppName:   "caelis",
		UserID:    "u",
		Workspace: session.WorkspaceRef{Key: "ws"},
		SessionID: "s-1234",
	})
	if err == nil {
		t.Fatal("ResumeSession() error = nil, want ambiguous session error")
	}
	var gwErr *Error
	if !As(err, &gwErr) || gwErr.Code != CodeSessionAmbiguous {
		t.Fatalf("ResumeSession() error = %v, want session_ambiguous", err)
	}
}

func TestInterruptCancelsActiveRunByBinding(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		CWD: "/tmp/ws",
	}
	rt := &cancellableRuntime{session: activeSession, cancelled: make(chan struct{})}
	svc := &recordingSessionService{startSessionResult: activeSession, sessionResult: activeSession}
	gw, err := New(Config{
		Sessions: svc,
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := gw.StartSession(context.Background(), StartSessionRequest{
		AppName:    "caelis",
		UserID:     "u",
		Workspace:  session.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
		BindingKey: "surface-1",
	}); err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	if _, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	}); err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}

	if err := gw.Interrupt(context.Background(), InterruptRequest{BindingKey: "surface-1"}); err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	select {
	case <-rt.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Interrupt() did not cancel runtime context")
	}
}

func TestPromptParticipantCancelCancelsRuntimeRunner(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		CWD: "/tmp/ws",
	}
	runner := &blockingCancelRunner{
		eventsStarted: make(chan struct{}),
		cancelled:     make(chan struct{}),
		release:       make(chan struct{}),
	}
	rt := &controlPlaneRuntime{
		session:    activeSession,
		attachResp: activeSession,
		promptResp: agent.RunResult{Session: activeSession, Handle: runner},
	}
	svc := &recordingSessionService{startSessionResult: activeSession, sessionResult: activeSession}
	gw, err := New(Config{
		Sessions: svc,
		Runtime:  rt,
		Control:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := gw.PromptParticipant(context.Background(), PromptParticipantRequest{
		SessionRef:    activeSession.SessionRef,
		ParticipantID: "side-1",
		Input:         "hello",
	})
	if err != nil {
		t.Fatalf("PromptParticipant() error = %v", err)
	}
	select {
	case <-runner.eventsStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime runner events were not attached")
	}
	if !result.Handle.Cancel().Cancelled() {
		t.Fatal("participant turn Cancel().Cancelled() = false, want true")
	}
	select {
	case <-runner.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("participant turn cancel did not cancel runtime runner")
	}
	close(runner.release)
	for range result.Handle.ACPEvents() {
	}
}

func TestPromptParticipantProjectsSubmissionReferencesBeforeRuntime(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		CWD: "/workspace",
	}
	runner := &recordingRunner{}
	rt := &controlPlaneRuntime{
		session:    activeSession,
		attachResp: activeSession,
		promptResp: agent.RunResult{Session: activeSession, Handle: runner},
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Control:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
		SubmissionReferences: SubmissionReferenceProjectorFunc(func(_ context.Context, req SubmissionReferenceProjectionRequest) (SubmissionReferenceProjection, error) {
			if req.Session.CWD != "/workspace" {
				t.Fatalf("projection session cwd = %q, want /workspace", req.Session.CWD)
			}
			return SubmissionReferenceProjection{Input: "projected participant input", Changed: true}, nil
		}),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.PromptParticipant(context.Background(), PromptParticipantRequest{
		SessionRef:    activeSession.SessionRef,
		ParticipantID: "side-1",
		Input:         "$cmpctl inspect #dict.go",
		ContentParts: []model.ContentPart{
			{Type: model.ContentPartText, Text: "$cmpctl inspect #dict.go"},
			{Type: model.ContentPartImage, MimeType: "image/png", Data: "iVBORw0KGgo=", FileName: "shot.png"},
		},
	})
	if err != nil {
		t.Fatalf("PromptParticipant() error = %v", err)
	}
	_ = collectHandleEvents(t, result.Handle)

	if rt.promptReq.Input != "projected participant input" {
		t.Fatalf("prompt input = %q, want projected participant input", rt.promptReq.Input)
	}
	if rt.promptReq.DisplayInput != "$cmpctl inspect #dict.go" {
		t.Fatalf("prompt DisplayInput = %q, want raw input", rt.promptReq.DisplayInput)
	}
	if got := rt.promptReq.ContentParts; len(got) != 2 ||
		got[0].Type != model.ContentPartText || got[0].Text != "projected participant input" ||
		got[1].Type != model.ContentPartImage {
		t.Fatalf("prompt ContentParts = %#v, want projected text plus original image", got)
	}
}

func TestHandoffControllerDelegatesToInjectedControlAndUpdatesBinding(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		Controller: session.ControllerBinding{
			Kind:         session.ControllerKindACP,
			ControllerID: "acp-controller",
			EpochID:      "epoch-1",
		},
		Participants: []session.ParticipantBinding{{
			ID:            "p-1",
			Kind:          session.ParticipantKindACP,
			Role:          session.ParticipantRoleDelegated,
			ControllerRef: "epoch-1",
		}},
	}
	rt := &controlPlaneRuntime{
		session:     activeSession,
		runState:    agent.RunState{Status: agent.RunLifecycleStatusRunning, ActiveRunID: "run-1"},
		handoffResp: activeSession,
	}
	svc := &recordingSessionService{
		sessionResult:      activeSession,
		startSessionResult: activeSession,
		eventsResult: []*session.Event{
			{
				ID:   "evt-1",
				Type: session.EventTypeHandoff,
				Scope: &session.EventScope{
					Controller: session.ControllerRef{Kind: session.ControllerKindACP, ID: "acp-controller", EpochID: "epoch-1"},
					ACP:        session.ACPRef{SessionID: "acp-main", EventType: "agent_message_chunk"},
				},
			},
			{
				ID:   "evt-2",
				Type: session.EventTypeParticipant,
				Scope: &session.EventScope{
					Controller:  session.ControllerRef{Kind: session.ControllerKindACP, ID: "acp-controller", EpochID: "epoch-1"},
					Participant: session.ParticipantRef{ID: "p-1", Kind: session.ParticipantKindACP},
				},
			},
		},
	}
	gw, err := New(Config{
		Sessions: svc,
		Runtime:  rt,
		Control:  rt,
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := gw.StartSession(context.Background(), StartSessionRequest{
		AppName:    "caelis",
		UserID:     "u",
		Workspace:  session.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
		BindingKey: "surface-acp",
	}); err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	updated, err := gw.HandoffController(context.Background(), HandoffControllerRequest{
		BindingKey: "surface-acp",
		Kind:       session.ControllerKindACP,
		Agent:      "codex",
		Source:     "user",
		Reason:     "delegate main control",
	})
	if err != nil {
		t.Fatalf("HandoffController() error = %v", err)
	}
	if updated.Controller.Kind != session.ControllerKindACP || rt.handoffReq.Agent != "codex" || rt.handoffReq.SessionRef.SessionID != "s1" {
		t.Fatalf("updated=%+v handoffReq=%+v", updated, rt.handoffReq)
	}

	state, err := gw.ControlPlaneState(context.Background(), ControlPlaneStateRequest{BindingKey: "surface-acp"})
	if err != nil {
		t.Fatalf("ControlPlaneState() error = %v", err)
	}
	if state.Controller.Kind != session.ControllerKindACP || state.Controller.EpochID != "epoch-1" || !state.HasActiveTurn {
		t.Fatalf("control state = %+v", state)
	}
	if state.Continuity.LastEventCursor != "evt-2" || state.Continuity.ControllerCursor != "evt-2" {
		t.Fatalf("control continuity = %+v", state.Continuity)
	}
	if got := state.Continuity.ParticipantCursors["p-1"]; got != "evt-2" {
		t.Fatalf("participant cursors = %+v", state.Continuity.ParticipantCursors)
	}
	if state.Continuity.ACPProjection.Cursor != "evt-1" || state.Continuity.ACPProjection.SessionID != "acp-main" {
		t.Fatalf("acp projection = %+v", state.Continuity.ACPProjection)
	}
}

func TestHandoffControllerRejectsMissingControlPlane(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = gw.HandoffController(context.Background(), HandoffControllerRequest{
		SessionRef: activeSession.SessionRef,
		Kind:       session.ControllerKindACP,
		Agent:      "codex",
	})
	if err == nil {
		t.Fatal("HandoffController() error = nil, want unsupported")
	}
	var gwErr *Error
	if !As(err, &gwErr) || gwErr.Code != CodeControlPlaneUnsupported {
		t.Fatalf("HandoffController() error = %v", err)
	}
}

func TestAttachParticipantDelegatesToInjectedControlAndUpdatesBinding(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		Participants: []session.ParticipantBinding{{
			ID:    "participant-1",
			Kind:  session.ParticipantKindACP,
			Role:  session.ParticipantRoleSidecar,
			Label: "Copilot",
		}},
	}
	rt := &controlPlaneRuntime{
		session:    activeSession,
		attachResp: activeSession,
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Control:  rt,
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := gw.StartSession(context.Background(), StartSessionRequest{
		AppName:    "caelis",
		UserID:     "u",
		Workspace:  session.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
		BindingKey: "surface-agent",
	}); err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	updated, err := gw.AttachParticipant(context.Background(), AttachParticipantRequest{
		BindingKey: "surface-agent",
		Agent:      "untrusted-request-agent",
		Role:       session.ParticipantRoleSidecar,
		Source:     "user_attach",
		Label:      "Copilot",
		Placement: sdkplacement.Placement{
			Kind: sdkplacement.KindAgent, Agent: "copilot", ProfileID: "acp:copilot",
		},
	})
	if err != nil {
		t.Fatalf("AttachParticipant() error = %v", err)
	}
	if len(updated.Participants) != 1 || rt.attachReq.Agent != "copilot" || rt.attachReq.SessionRef.SessionID != "s1" || rt.attachReq.Placement.Agent != "copilot" {
		t.Fatalf("updated=%+v attachReq=%+v", updated, rt.attachReq)
	}
	if current, ok := gw.CurrentSession("surface-agent"); !ok || current.SessionID != "s1" {
		t.Fatalf("CurrentSession() = %+v, %v; want s1", current, ok)
	}
}

func TestDetachParticipantDelegatesToInjectedControlAndUpdatesBinding(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &controlPlaneRuntime{
		session:    activeSession,
		detachResp: activeSession,
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Control:  rt,
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := gw.StartSession(context.Background(), StartSessionRequest{
		AppName:    "caelis",
		UserID:     "u",
		Workspace:  session.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
		BindingKey: "surface-agent",
	}); err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	updated, err := gw.DetachParticipant(context.Background(), DetachParticipantRequest{
		BindingKey:    "surface-agent",
		ParticipantID: "participant-1",
		Source:        "user_detach",
	})
	if err != nil {
		t.Fatalf("DetachParticipant() error = %v", err)
	}
	if updated.SessionID != "s1" || rt.detachReq.ParticipantID != "participant-1" || rt.detachReq.SessionRef.SessionID != "s1" {
		t.Fatalf("updated=%+v detachReq=%+v", updated, rt.detachReq)
	}
}

func TestBeginTurnRejectsSecondActiveRunForSameSession(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &blockingRuntime{session: activeSession}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	first, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "first",
	})
	if err != nil {
		t.Fatalf("BeginTurn(first) error = %v", err)
	}
	defer first.Handle.Close()

	_, err = gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "second",
	})
	if err == nil {
		t.Fatal("BeginTurn(second) error = nil, want conflict")
	}
	var gwErr *Error
	if !As(err, &gwErr) || gwErr.Code != CodeActiveRunConflict {
		t.Fatalf("BeginTurn(second) error = %v, want active run conflict", err)
	}
}

func TestBeginTurnChecksActiveConflictBeforeResolver(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &blockingRuntime{session: activeSession}
	resolver := &recordingResolver{resolved: ResolvedTurn{}}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	first, err := gw.BeginTurn(context.Background(), BeginTurnRequest{SessionRef: activeSession.SessionRef, Input: "first"})
	if err != nil {
		t.Fatalf("BeginTurn(first) error = %v", err)
	}
	defer first.Handle.Close()
	resolver.calls = 0

	_, err = gw.BeginTurn(context.Background(), BeginTurnRequest{SessionRef: activeSession.SessionRef, Input: "second"})
	if err == nil {
		t.Fatal("BeginTurn(second) error = nil, want conflict")
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver calls = %d, want 0 for active conflict", resolver.calls)
	}
}

func TestBeginTurnRejectsInvalidSessionBeforeResolver(t *testing.T) {
	t.Parallel()

	resolver := &recordingResolver{resolved: ResolvedTurn{}}
	gw, err := New(Config{
		Sessions: &recordingSessionService{sessionErr: errors.New("missing session")},
		Runtime:  mockRuntime{},
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: session.SessionRef{SessionID: "missing"},
		Input:      "hello",
	})
	if err == nil {
		t.Fatal("BeginTurn() error = nil, want session error")
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver calls = %d, want 0 for invalid session", resolver.calls)
	}
}

func TestBeginTurnPassesIntentToResolver(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	resolver := &recordingResolver{resolved: ResolvedTurn{}}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  mockRuntime{},
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
		ModeName:   "main",
		ModelHint:  "mini",
		Surface:    "headless",
	}); err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}

	if resolver.lastIntent.ModeName != "main" || resolver.lastIntent.ModelHint != "mini" || resolver.lastIntent.Surface != "headless" {
		t.Fatalf("resolver intent = %+v, want propagated fields", resolver.lastIntent)
	}
}

func TestBeginTurnValidatesFinalExecutionRequirementsBeforeRuntime(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "u", SessionID: "requirements", WorkspaceKey: "ws",
	}}
	runtime := &recordingRuntime{session: activeSession, ran: make(chan struct{})}
	validationErr := errors.New("unsupported execution requirements")
	validator := &recordingExecutionValidator{err: validationErr}
	gw, err := New(Config{
		Sessions:           staticSessionService{session: activeSession},
		Runtime:            runtime,
		Resolver:           staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{AgentSpec: agent.AgentSpec{Name: "main"}}}},
		ExecutionValidator: validator,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
		Surface:    "tui",
	})
	if !errors.Is(err, validationErr) {
		t.Fatalf("BeginTurn() error = %v, want %v", err, validationErr)
	}
	if validator.calls != 1 {
		t.Fatalf("validator calls = %d, want 1", validator.calls)
	}
	if !validator.request.Request.StreamEnabled(false) {
		t.Fatalf("validated request = %+v, want merged surface stream requirement", validator.request.Request)
	}
	select {
	case <-runtime.ran:
		t.Fatal("runtime ran after execution requirements failed")
	default:
	}
}

func TestBeginTurnDoesNotApplyLocalRequirementsToACPController(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "external", WorkspaceKey: "ws"},
		Controller: session.ControllerBinding{Kind: session.ControllerKindACP, ControllerID: "external-agent"},
	}
	runtime := &recordingRuntime{session: activeSession, ran: make(chan struct{})}
	validator := &recordingExecutionValidator{err: errors.New("local requirements must not run")}
	gw, err := New(Config{
		Sessions:           staticSessionService{session: activeSession},
		Runtime:            runtime,
		Resolver:           staticResolver{},
		ExecutionValidator: validator,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{SessionRef: activeSession.SessionRef, Input: "hello"})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer result.Handle.Close()
	select {
	case <-runtime.ran:
	case <-time.After(2 * time.Second):
		t.Fatal("external controller runtime did not run")
	}
	if validator.calls != 0 {
		t.Fatalf("local validator calls = %d, want 0 for ACP controller", validator.calls)
	}
}

func TestBeginTurnProjectsSubmissionReferencesBeforeResolver(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		CWD: "/workspace",
	}
	resolver := &recordingResolver{resolved: ResolvedTurn{}}
	rt := &recordingRuntime{session: activeSession, ran: make(chan struct{})}
	projectorCalls := 0
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: resolver,
		SubmissionReferences: SubmissionReferenceProjectorFunc(func(_ context.Context, req SubmissionReferenceProjectionRequest) (SubmissionReferenceProjection, error) {
			projectorCalls++
			if req.Session.CWD != "/workspace" {
				t.Fatalf("projection session cwd = %q, want /workspace", req.Session.CWD)
			}
			if req.Input != "$cmpctl inspect #dict.go" {
				t.Fatalf("projection input = %q, want raw trimmed input", req.Input)
			}
			return SubmissionReferenceProjection{
				Input:   "projected model input",
				Changed: true,
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "  $cmpctl inspect #dict.go  ",
		ContentParts: []model.ContentPart{
			{Type: model.ContentPartText, Text: "$cmpctl inspect #dict.go"},
			{Type: model.ContentPartImage, MimeType: "image/png", Data: "iVBORw0KGgo=", FileName: "shot.png"},
		},
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer result.Handle.Close()
	select {
	case <-rt.ran:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not run")
	}

	if projectorCalls != 1 {
		t.Fatalf("projection calls = %d, want 1", projectorCalls)
	}
	if resolver.lastIntent.Input != "projected model input" {
		t.Fatalf("resolver input = %q, want projected model input", resolver.lastIntent.Input)
	}
	if resolver.lastIntent.DisplayInput != "$cmpctl inspect #dict.go" {
		t.Fatalf("resolver DisplayInput = %q, want raw input", resolver.lastIntent.DisplayInput)
	}
	if got := resolver.lastIntent.ContentParts; len(got) != 2 ||
		got[0].Type != model.ContentPartText || got[0].Text != "projected model input" ||
		got[1].Type != model.ContentPartImage {
		t.Fatalf("resolver ContentParts = %#v, want projected text part plus original image", got)
	}
	if rt.lastReq.Input != "projected model input" || rt.lastReq.DisplayInput != "$cmpctl inspect #dict.go" {
		t.Fatalf("runtime request input/display = %q/%q, want projected/raw", rt.lastReq.Input, rt.lastReq.DisplayInput)
	}
}

func TestBeginTurnSkipsResolverForACPControllerSession(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		Controller: session.ControllerBinding{
			Kind:         session.ControllerKindACP,
			ControllerID: "acp-main",
			EpochID:      "epoch-1",
		},
	}
	rt := &recordingRuntime{
		session: activeSession,
		result:  agent.RunResult{Session: activeSession},
	}
	resolver := &recordingControllerResolver{
		recordingResolver: recordingResolver{err: errors.New("local model should not resolve")},
		controllerResolved: ResolvedTurn{RunRequest: agent.RunRequest{
			AgentSpec: agent.AgentSpec{Metadata: map[string]any{"policy_mode": "manual"}},
		}},
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello controller",
		Surface:    "cli-tui",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	_ = collectHandleEvents(t, result.Handle)

	if resolver.calls != 0 {
		t.Fatalf("resolver calls = %d, want 0 for ACP controller session", resolver.calls)
	}
	if resolver.controllerCalls != 1 {
		t.Fatalf("controller resolver calls = %d, want 1", resolver.controllerCalls)
	}
	if rt.lastReq.Input != "hello controller" || rt.lastReq.SessionRef.SessionID != "s1" {
		t.Fatalf("runtime request = %+v, want controller turn input/session", rt.lastReq)
	}
	if _, ok := rt.lastReq.AgentSpec.Metadata["policy_mode"]; ok {
		t.Fatalf("runtime policy_mode = %#v, want legacy approval mode removed from policy metadata", rt.lastReq.AgentSpec.Metadata["policy_mode"])
	}
	if _, ok := rt.lastReq.AgentSpec.Metadata[policyapi.MetadataPolicyProfile]; ok {
		t.Fatalf("runtime policy_profile = %#v, want legacy approval mode omitted from policy metadata", rt.lastReq.AgentSpec.Metadata[policyapi.MetadataPolicyProfile])
	}
}

func TestBeginTurnNormalizesControllerPolicyProfileMetadata(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		Controller: session.ControllerBinding{
			Kind:         session.ControllerKindACP,
			ControllerID: "acp-main",
			EpochID:      "epoch-1",
		},
	}
	rt := &recordingRuntime{
		session: activeSession,
		result:  agent.RunResult{Session: activeSession},
	}
	resolver := &recordingControllerResolver{
		controllerResolved: ResolvedTurn{RunRequest: agent.RunRequest{
			AgentSpec: agent.AgentSpec{Metadata: map[string]any{policyapi.MetadataPolicyProfile: "workspace_write"}},
		}},
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello controller",
		Surface:    "cli-tui",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	_ = collectHandleEvents(t, result.Handle)

	if got := rt.lastReq.AgentSpec.Metadata[policyapi.MetadataPolicyProfile]; got != policyapi.ProfileWorkspaceWrite {
		t.Fatalf("runtime policy_profile = %#v, want workspace-write", got)
	}
	if _, ok := rt.lastReq.AgentSpec.Metadata[policyapi.MetadataLegacyPolicyMode]; ok {
		t.Fatalf("runtime policy_mode = %#v, want legacy key removed", rt.lastReq.AgentSpec.Metadata[policyapi.MetadataLegacyPolicyMode])
	}
}

func TestBeginTurnLoadsSessionResolvesIntentRunsRuntimeAndPublishesEvents(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	msg := model.NewTextMessage(model.RoleAssistant, "ok")
	runner := &recordingRunner{
		events: []*session.Event{{ID: "e1", Type: session.EventTypeAssistant, Message: &msg, Text: "ok"}},
	}
	rt := &recordingRuntime{
		session: activeSession,
		result:  agent.RunResult{Session: activeSession, Handle: runner},
	}
	resolver := &recordingResolver{resolved: ResolvedTurn{
		RunRequest: agent.RunRequest{Input: "hello"},
	}}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: resolver,
		Clock: func() time.Time {
			return time.Unix(100, 0)
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	got := collectHandleEvents(t, result.Handle)
	if len(got) == 0 || got[len(got)-1].EventID != "e1" || eventstream.UpdateType(got[len(got)-1].Update) != schema.UpdateAgentMessage {
		t.Fatalf("published events = %#v, want assistant event e1", got)
	}
	if rt.lastReq.SessionRef != activeSession.SessionRef || rt.lastReq.Input != "hello" {
		t.Fatalf("runtime req = %+v", rt.lastReq)
	}
}

func TestGatewayActiveTurnsReportsSessionScopedState(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	runner := &blockingRunner{release: make(chan struct{})}
	rt := &recordingRuntime{
		session: activeSession,
		result:  agent.RunResult{Session: activeSession, Handle: runner},
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer result.Handle.Close()

	active := gw.ActiveTurns()
	if len(active) != 1 {
		t.Fatalf("ActiveTurns() len = %d, want 1", len(active))
	}
	if active[0].SessionRef.SessionID != "s1" || active[0].HandleID == "" || active[0].RunID == "" || active[0].TurnID == "" {
		t.Fatalf("ActiveTurns()[0] = %+v", active[0])
	}
	if current, ok := gw.ActiveTurn("s1"); !ok || current.SessionRef.SessionID != "s1" {
		t.Fatalf("ActiveTurn(s1) = %+v, %v", current, ok)
	}

	close(runner.release)
	collectHandleEvents(t, result.Handle)
	if active := gw.ActiveTurns(); len(active) != 0 {
		t.Fatalf("ActiveTurns() after completion = %+v, want empty", active)
	}
}

func TestGatewaySubmitActiveTurnForwardsConversationToRunner(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	runner := &submitRecordingBlockingRunner{release: make(chan struct{})}
	rt := &recordingRuntime{
		session: activeSession,
		result:  agent.RunResult{Session: activeSession, Handle: runner},
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer result.Handle.Close()

	if err := gw.SubmitActiveTurn(context.Background(), SubmitActiveTurnRequest{
		SessionRef: activeSession.SessionRef,
		Kind:       SubmissionKindConversation,
		Text:       "steer next step",
		Metadata:   map[string]any{"source": "test"},
	}); err != nil {
		t.Fatalf("SubmitActiveTurn() error = %v", err)
	}
	deadline := time.After(2 * time.Second)
	for len(runner.snapshot()) == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for active submission to reach runner")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	submissions := runner.snapshot()
	if got, want := len(submissions), 1; got != want {
		t.Fatalf("len(submissions) = %d, want %d", got, want)
	}
	if got := submissions[0].Text; got != "steer next step" {
		t.Fatalf("submission text = %q, want steer text", got)
	}
	if got := submissions[0].Metadata["source"]; got != "test" {
		t.Fatalf("submission metadata[source] = %#v, want test", got)
	}

	close(runner.release)
	collectHandleEvents(t, result.Handle)
}

func TestBeginTurnDefaultsToStreamingRequestsAtGatewayBoundary(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &recordingRuntime{
		session: activeSession,
		result:  agent.RunResult{Session: activeSession, Handle: &recordingRunner{}},
		ran:     make(chan struct{}),
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
		Surface:    "cli-tui",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	result.Handle.Close()
	<-rt.ran

	if !rt.lastReq.Request.StreamEnabled(false) {
		t.Fatalf("runtime request stream = false, want true by default")
	}
}

func TestBeginTurnAllowsSurfaceToOverrideStreamingPolicy(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &recordingRuntime{
		session: activeSession,
		result:  agent.RunResult{Session: activeSession, Handle: &recordingRunner{}},
		ran:     make(chan struct{}),
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
		Surface:    "headless",
		Request:    agent.ModelRequestOptions{Stream: boolPtr(false)},
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	result.Handle.Close()
	<-rt.ran

	if rt.lastReq.Request.StreamEnabled(true) {
		t.Fatalf("runtime request stream = true, want explicit false override")
	}
}

func TestBeginTurnDefaultsHeadlessSurfaceToStreaming(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &recordingRuntime{
		session: activeSession,
		result:  agent.RunResult{Session: activeSession, Handle: &recordingRunner{}},
		ran:     make(chan struct{}),
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
		Surface:    "headless",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	result.Handle.Close()
	<-rt.ran

	if !rt.lastReq.Request.StreamEnabled(false) {
		t.Fatalf("runtime request stream = false, want true for headless default")
	}
}

func TestBeginTurnBridgesApprovalRequestsIntoHandleEvents(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &approvalRuntime{session: activeSession}
	sessions := staticSessionService{
		session: activeSession,
		state:   map[string]any{StateCurrentApprovalMode: string(ApprovalModeManual)},
	}
	gw, err := New(Config{
		Sessions: sessions,
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{}}},
		Clock: func() time.Time {
			return time.Unix(100, 0)
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}

	first := <-result.Handle.ACPEvents()
	if first.Kind != eventstream.KindRequestPermission {
		t.Fatalf("first event kind = %q, want request_permission", first.Kind)
	}
	if err := result.Handle.Submit(context.Background(), SubmitRequest{
		Kind: SubmissionKindApproval,
		Approval: &ApprovalDecision{
			RequestID: first.ApprovalRequestID,
			Approved:  true,
			Outcome:   "approved",
		},
	}); err != nil {
		t.Fatalf("Submit(approval) error = %v", err)
	}
	_ = collectHandleEvents(t, result.Handle)
}

func TestBeginTurnPublishesChildApprovalThroughControlQueue(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	runtime := &childApprovalRuntime{
		session:   activeSession,
		responses: make(chan agent.ApprovalResponse, 1),
	}
	gw, err := New(Config{
		Sessions: staticSessionService{
			session: activeSession,
			state:   map[string]any{StateCurrentApprovalMode: string(ApprovalModeManual)},
		},
		Runtime:  runtime,
		Resolver: staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{}}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	var permission eventstream.Envelope
	select {
	case permission = <-result.Handle.ACPEvents():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Control-published child approval")
	}
	if permission.ApprovalRequestID == "" {
		t.Fatal("Control-published child approval has no request id")
	}
	assertChildPermissionEnvelope(t, permission, permission.ApprovalRequestID, "task-1", "child.txt")
	handle, ok := result.Handle.(*turnHandle)
	if !ok {
		t.Fatalf("turn handle = %T, want *turnHandle", result.Handle)
	}
	if events, _, err := handle.eventsAfter(""); err != nil || len(events) != 1 || events[0].ApprovalRequestID != permission.ApprovalRequestID {
		t.Fatalf("gateway direct approval events = %#v, %v; want one Control-owned permission", events, err)
	}

	if err := result.Handle.Submit(context.Background(), SubmitRequest{
		Kind: SubmissionKindApproval,
		Approval: &ApprovalDecision{
			RequestID: permission.ApprovalRequestID,
			Outcome:   string(ApprovalStatusApproved),
			Approved:  true,
		},
	}); err != nil {
		t.Fatalf("Submit(child approval) error = %v", err)
	}
	select {
	case response := <-runtime.responses:
		if !response.Approved || response.Outcome != string(ApprovalStatusApproved) {
			t.Fatalf("child approval response = %+v, want approved response", response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child runtime did not resume after exact approval response")
	}
	_ = collectHandleEvents(t, result.Handle)
}

func TestBeginTurnPublishesChildAutoReviewAsTransient(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
	}}
	runtime := &childApprovalRuntime{
		session:   activeSession,
		responses: make(chan agent.ApprovalResponse, 1),
	}
	gw, err := New(Config{
		Sessions: staticSessionService{
			session: activeSession,
			state:   map[string]any{StateCurrentApprovalMode: string(ApprovalModeAutoReview)},
		},
		Runtime:  runtime,
		Resolver: staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{}}},
		ApprovalReviewer: staticApprovalReviewer{
			result:            ApprovalReviewResult{Approved: true},
			sessionAccounting: &UsageSnapshot{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectHandleEvents(t, result.Handle)
	reviewCount := 0
	usageCount := 0
	for _, envelope := range events {
		isReview := envelope.Kind == eventstream.KindApprovalReview
		isUsage := eventstream.UsageSnapshotFromEnvelope(envelope) != nil
		if !isReview && !isUsage {
			continue
		}
		if isReview {
			reviewCount++
		} else {
			usageCount++
		}
		if envelope.Scope != eventstream.ScopeSubagent || envelope.ScopeID != "task-1" {
			t.Fatalf("child auto-review scope = %q/%q, want subagent/task-1", envelope.Scope, envelope.ScopeID)
		}
		if envelope.ParentTool == nil || envelope.ParentTool.ToolCallID != "spawn-call-1" {
			t.Fatalf("child auto-review parent = %#v, want spawn-call-1", envelope.ParentTool)
		}
		if envelope.Delivery == nil || envelope.Delivery.Mode != eventstream.DeliveryTransient {
			t.Fatalf("child auto-review delivery = %#v, want transient", envelope.Delivery)
		}
		if envelope.Position != nil {
			t.Fatalf("child auto-review position = %#v, want broker-assigned transient position later", envelope.Position)
		}
	}
	if reviewCount != 2 || usageCount != 1 {
		t.Fatalf("child auto-review events = %#v, want two reviews and one usage", events)
	}
	select {
	case response := <-runtime.responses:
		if !response.Approved {
			t.Fatalf("child auto-review response = %#v, want approved", response)
		}
	case <-time.After(time.Second):
		t.Fatal("child runtime did not receive auto-review response")
	}
}

func TestResolveApprovalRequestQueuesAutoReviewAtActiveHead(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	release := make(chan struct{})
	approver := &serialApprovalApprover{
		started: make(chan string, 2),
		release: release,
	}
	gw, err := New(Config{
		Sessions: staticSessionService{
			session: activeSession,
			state:   map[string]any{StateCurrentApprovalMode: string(ApprovalModeAutoReview)},
		},
		Runtime:          mockRuntime{},
		Resolver:         staticResolver{},
		ApprovalApprover: approver,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	handle := newTestTurnHandle()
	request := func(callID string) *agent.ApprovalRequest {
		return &agent.ApprovalRequest{
			SessionRef: activeSession.SessionRef,
			Session:    activeSession,
			RunID:      "run-1",
			TurnID:     "turn-1",
			Tool:       tool.Definition{Name: "RUN_COMMAND"},
			Call:       tool.Call{ID: callID, Name: "RUN_COMMAND"},
		}
	}
	type result struct {
		callID string
		resp   agent.ApprovalResponse
		err    error
	}
	results := make(chan result, 2)
	go func() {
		resp, err := gw.resolveApprovalRequest(context.Background(), context.Background(), handle, request("first"), nil)
		results <- result{callID: "first", resp: resp, err: err}
	}()
	select {
	case callID := <-approver.started:
		if callID != "first" {
			t.Fatalf("first auto-review call = %q, want first", callID)
		}
	case <-time.After(time.Second):
		t.Fatal("first auto-review did not start")
	}

	go func() {
		resp, err := gw.resolveApprovalRequest(context.Background(), context.Background(), handle, request("second"), nil)
		results <- result{callID: "second", resp: resp, err: err}
	}()
	waitForApprovalQueueLength(t, handle, 2)
	second := handle.approvals.queueSnapshot()[1]
	select {
	case <-second.activated:
		t.Fatal("queued auto-review became active before the first request resolved")
	default:
	}
	select {
	case callID := <-approver.started:
		t.Fatalf("queued auto-review called approver before becoming active: %q", callID)
	default:
	}

	close(release)
	select {
	case callID := <-approver.started:
		if callID != "second" {
			t.Fatalf("second auto-review call = %q, want second", callID)
		}
	case <-time.After(time.Second):
		t.Fatal("second auto-review did not start after the first resolved")
	}
	for range 2 {
		select {
		case got := <-results:
			if got.err != nil || !got.resp.Approved {
				t.Fatalf("auto-review result for %s = %+v, %v; want approved", got.callID, got.resp, got.err)
			}
		case <-time.After(time.Second):
			t.Fatal("auto-review request did not resolve")
		}
	}
}

func TestBeginTurnDefaultManualApprovalModePromptsClient(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &approvalRuntime{session: activeSession}
	gw, err := New(Config{
		Sessions: staticSessionService{
			session: activeSession,
			state:   map[string]any{},
		},
		Runtime:             rt,
		Resolver:            staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{}}},
		DefaultApprovalMode: ApprovalModeManual,
		ApprovalReviewer: staticApprovalReviewer{
			result: ApprovalReviewResult{Approved: true},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	first := <-result.Handle.ACPEvents()
	if first.Kind != eventstream.KindRequestPermission {
		t.Fatalf("first event kind = %q, want request_permission from default manual mode", first.Kind)
	}
	if err := result.Handle.Submit(context.Background(), SubmitRequest{
		Kind: SubmissionKindApproval,
		Approval: &ApprovalDecision{
			RequestID: first.ApprovalRequestID,
			Approved:  true,
			Outcome:   "approved",
		},
	}); err != nil {
		t.Fatalf("Submit(approval) error = %v", err)
	}
	_ = collectHandleEvents(t, result.Handle)
}

func TestBeginTurnSessionApprovalModeOverridesDefaultManual(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &approvalRuntime{session: activeSession}
	gw, err := New(Config{
		Sessions: staticSessionService{
			session: activeSession,
			state:   map[string]any{StateCurrentApprovalMode: string(ApprovalModeAutoReview)},
		},
		Runtime:             rt,
		Resolver:            staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{}}},
		DefaultApprovalMode: ApprovalModeManual,
		ApprovalReviewer: staticApprovalReviewer{
			result: ApprovalReviewResult{Approved: true},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	first := <-result.Handle.ACPEvents()
	if first.Kind != eventstream.KindApprovalReview {
		t.Fatalf("first event kind = %q, want approval_review from session auto-review override", first.Kind)
	}
	got := collectHandleEvents(t, result.Handle)
	for _, env := range append([]eventstream.Envelope{first}, got...) {
		if env.Kind == eventstream.KindRequestPermission {
			t.Fatal("got request_permission, want session auto-review override to beat default manual")
		}
	}
}

func TestBeginTurnUsesConfiguredGuardianModelForMainTurnApproval(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{SessionRef: session.SessionRef{
		AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
	}}
	mainModel := fakeLLM{name: "main-model"}
	guardianModel := fakeLLM{name: "guardian-model"}
	reviewer := &recordingApprovalReviewer{result: ApprovalReviewResult{Approved: true}}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  &approvalRuntime{session: activeSession},
		Resolver: approvalModelResolverStub{
			staticResolver: staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{
				AgentSpec: agent.AgentSpec{Model: mainModel},
			}}},
			model: guardianModel,
		},
		ApprovalReviewer: reviewer,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	_ = collectHandleEvents(t, result.Handle)
	if reviewer.req.Model == nil || reviewer.req.Model.Name() != guardianModel.Name() {
		t.Fatalf("approval review model = %#v, want configured Guardian %q", reviewer.req.Model, guardianModel.Name())
	}
}

func TestBeginTurnRequestModeManualIgnoredUnderAutoReview(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &approvalRuntime{session: activeSession, mode: string(ApprovalModeManual)}
	gw, err := New(Config{
		Sessions: staticSessionService{
			session: activeSession,
			state:   map[string]any{},
		},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{}}},
		ApprovalReviewer: staticApprovalReviewer{
			result: ApprovalReviewResult{Approved: true},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	first := <-result.Handle.ACPEvents()
	if first.Kind != eventstream.KindApprovalReview {
		t.Fatalf("first event kind = %q, want auto approval_review", first.Kind)
	}
	got := collectHandleEvents(t, result.Handle)
	if len(got) == 0 {
		t.Fatal("collectHandleEvents() = empty, want terminal approval review event")
	}
	for _, env := range append([]eventstream.Envelope{first}, got...) {
		if env.Kind == eventstream.KindRequestPermission {
			t.Fatal("got manual request_permission, want request mode ignored under auto-review")
		}
	}
}

func TestBeginTurnApprovalModeSnapshotErrorFailsTurn(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &approvalRuntime{session: activeSession}
	gw, err := New(Config{
		Sessions: &recordingSessionService{
			sessionResult: activeSession,
			snapshotErr:   errors.New("state unavailable"),
		},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{}}},
		ApprovalReviewer: staticApprovalReviewer{
			result: ApprovalReviewResult{Approved: true},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	first := <-result.Handle.ACPEvents()
	if first.Kind != eventstream.KindError || first.Err == nil {
		t.Fatalf("first event = %+v, want eventstream error on state read failure", first)
	}
	for range result.Handle.ACPEvents() {
	}
}

func TestBeginTurnAutoReviewDenialDoesNotInterruptTurn(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &approvalRuntime{session: activeSession}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{}}},
		ApprovalReviewer: staticApprovalReviewer{
			result: ApprovalReviewResult{
				Approved:      false,
				Risk:          "medium",
				Authorization: "medium",
				Rationale:     "not narrow enough",
			},
			sessionAccounting: &UsageSnapshot{PromptTokens: 7, CachedInputTokens: 3, CompletionTokens: 2, ReasoningTokens: 1, TotalTokens: 9},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	events := collectHandleEvents(t, result.Handle)
	if len(events) < 3 {
		t.Fatalf("events len = %d, want in-progress, denied, and runtime completion", len(events))
	}
	if events[0].Kind != eventstream.KindApprovalReview || events[0].ApprovalReview == nil {
		t.Fatalf("first event = %#v, want approval_review", events[0])
	}
	if got := events[0].ApprovalReview.Status; got != string(ApprovalReviewStatusInProgress) {
		t.Fatalf("first review status = %q, want in_progress", got)
	}
	if events[1].Kind != eventstream.KindApprovalReview || events[1].ApprovalReview == nil {
		t.Fatalf("terminal event = %#v, want approval_review", events[1])
	}
	if got := events[1].ApprovalReview.Status; got != string(ApprovalReviewStatusDenied) {
		t.Fatalf("terminal review status = %q, want denied", got)
	}
	if text := events[1].ApprovalReview.Text; !strings.Contains(text, "not narrow enough") {
		t.Fatalf("review text = %q, want reviewer rationale", text)
	}
	usage := firstUsageSnapshot(events)
	if usage == nil || usage.PromptTokens != 7 || usage.CachedInputTokens != 3 || usage.CompletionTokens != 2 || usage.ReasoningTokens != 1 || usage.TotalTokens != 9 {
		t.Fatalf("terminal review session accounting = %+v, want reviewer session accounting", usage)
	}
	if events[len(events)-1].Kind == eventstream.KindError {
		t.Fatalf("last event error = %v, want normal turn continuation", events[len(events)-1].Err)
	}
}

func TestBeginTurnAutoReviewCancelPublishesTerminalReview(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	reviewer := &contextBlockingApprovalReviewer{started: make(chan struct{})}
	rt := &approvalRuntime{session: activeSession}
	gw, err := New(Config{
		Sessions:         staticSessionService{session: activeSession},
		Runtime:          rt,
		Resolver:         staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{}}},
		ApprovalReviewer: reviewer,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	events := result.Handle.ACPEvents()
	var got []eventstream.Envelope
	timeout := time.After(2 * time.Second)
	select {
	case first := <-events:
		got = append(got, first)
		if first.Kind != eventstream.KindApprovalReview || first.ApprovalReview == nil || first.ApprovalReview.Status != string(ApprovalReviewStatusInProgress) {
			t.Fatalf("first event = %#v, want in-progress approval review", first)
		}
	case <-timeout:
		t.Fatal("timed out waiting for in-progress approval review")
	}
	select {
	case <-reviewer.started:
	case <-timeout:
		t.Fatal("timed out waiting for blocking reviewer")
	}
	result.Handle.Cancel()
	for {
		select {
		case env, ok := <-events:
			if !ok {
				for _, event := range got {
					review := event.ApprovalReview
					if event.Kind == eventstream.KindApprovalReview && review != nil && review.Status == string(ApprovalReviewStatusFailed) {
						return
					}
				}
				t.Fatalf("events = %#v, want failed terminal approval review after cancel", got)
			}
			got = append(got, env)
		case <-timeout:
			t.Fatalf("timed out waiting for terminal approval review: %#v", got)
		}
	}
}

func TestPersistApprovalReviewSessionAccountingUsesSessionStateNotHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sessions := inmemory.NewStore(inmemory.Config{})
	activeSession, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "u",
		Workspace:          session.WorkspaceRef{Key: "ws"},
		PreferredSessionID: "s1",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	gw := &Gateway{
		sessions: sessions,
		clock:    time.Now,
	}
	req := &agent.ApprovalRequest{
		SessionRef: activeSession.SessionRef,
		TurnID:     "turn-1",
	}
	usage := &UsageSnapshot{PromptTokens: 7, CachedInputTokens: 3, CompletionTokens: 2, ReasoningTokens: 1, TotalTokens: 9}
	invocation := &session.EventInvocation{Provider: "deepseek", Model: "deepseek-v4-pro"}
	if err := gw.persistApprovalReviewSessionAccounting(ctx, req, usage, string(ApprovalModeAutoReview), invocation); err != nil {
		t.Fatalf("persistApprovalReviewSessionAccounting() error = %v", err)
	}
	events, err := sessions.Events(ctx, session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Events() count = %d, want no canonical accounting event", len(events))
	}
	state, err := sessions.SnapshotState(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	accounting, _ := state[StateUsageAccounting].(map[string]any)
	got := UsageSnapshotFromMap(anyMapValue(accounting["auto_review"]))
	if got == nil || *got != *usage {
		t.Fatalf("auto-review usage state = %+v, want %+v", got, usage)
	}
	if accounting["auto_review_provider"] != "deepseek" || accounting["auto_review_model"] != "deepseek-v4-pro" {
		t.Fatalf("auto-review attribution = %#v/%#v, want deepseek/deepseek-v4-pro", accounting["auto_review_provider"], accounting["auto_review_model"])
	}
	rows, _ := accounting["by_model"].([]any)
	if len(rows) != 1 {
		t.Fatalf("by_model = %#v, want one row", accounting["by_model"])
	}
	row := anyMapValue(rows[0])
	if row["provider"] != "deepseek" || row["model"] != "deepseek-v4-pro" {
		t.Fatalf("by_model row = %#v, want deepseek/deepseek-v4-pro", row)
	}
	if row["category"] != "auto_review" {
		t.Fatalf("by_model category = %#v, want auto_review", row["category"])
	}
	modelUsage := UsageSnapshotFromMap(anyMapValue(row["usage"]))
	if modelUsage == nil || *modelUsage != *usage {
		t.Fatalf("by_model usage = %+v, want %+v", modelUsage, usage)
	}
}

func TestBeginTurnAutoReviewRepeatedDenialsDoNotReplaceReviewerDecision(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &approvalRuntime{session: activeSession, requests: 3}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{RunRequest: agent.RunRequest{}}},
		ApprovalReviewer: staticApprovalReviewer{
			result: ApprovalReviewResult{
				Approved:      false,
				Risk:          "high",
				Authorization: "low",
				Rationale:     "repeated unsafe request",
			},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	events := collectHandleEvents(t, result.Handle)
	if len(events) == 0 {
		t.Fatalf("events = %#v, want approval review events", events)
	}
	if events[len(events)-1].Kind == eventstream.KindError {
		t.Fatalf("last event error = %#v, want repeated denials to preserve reviewer decisions", events[len(events)-1].Err)
	}
	denials := 0
	for _, env := range events {
		review := env.ApprovalReview
		if env.Kind != eventstream.KindApprovalReview || review == nil || review.Status != string(ApprovalReviewStatusDenied) {
			continue
		}
		denials++
		if !strings.Contains(review.Text, "repeated unsafe request") {
			t.Fatalf("review text = %q, want reviewer rationale", review.Text)
		}
	}
	if denials != 3 {
		t.Fatalf("denial events = %d, want 3: %#v", denials, events)
	}
}

func TestBindSessionExpiresBinding(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0)
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
		Clock: func() time.Time {
			return now
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := gw.BindSession(context.Background(), BindSessionRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: "telegram:user-1",
		Binding: BindingDescriptor{
			Surface:   "telegram",
			ActorKind: "user",
			ActorID:   "user-1",
			Owner:     "telegram-bot",
			ExpiresAt: now.Add(time.Minute),
		},
	}); err != nil {
		t.Fatalf("BindSession() error = %v", err)
	}
	if current, ok := gw.CurrentSession("telegram:user-1"); !ok || current.SessionID != "s1" {
		t.Fatalf("CurrentSession() = %+v, %v; want s1", current, ok)
	}

	now = now.Add(2 * time.Minute)
	if _, ok := gw.CurrentSession("telegram:user-1"); ok {
		t.Fatal("CurrentSession() ok = true, want expired binding to be cleared")
	}
}
