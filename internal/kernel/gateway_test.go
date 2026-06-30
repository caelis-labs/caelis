package kernel

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/session/memory"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/model"
	policyapi "github.com/OnslaughtSnail/caelis/ports/policy"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
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

func TestForkSessionCopiesSourceMetadataAndBinds(t *testing.T) {
	t.Parallel()

	source := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		CWD:      "/tmp/ws",
		Title:    "Original",
		Metadata: map[string]any{"mode": "main"},
	}
	forked := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s2", WorkspaceKey: "ws",
		},
		CWD: "/tmp/ws",
	}
	svc := &recordingSessionService{
		sessionResult:      source,
		startSessionResult: forked,
	}
	gw, err := New(Config{
		Sessions: svc,
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	started, err := gw.ForkSession(context.Background(), ForkSessionRequest{
		SourceSessionRef: source.SessionRef,
		BindingKey:       "surface-fork",
		Metadata:         map[string]any{"mode": "fork"},
	})
	if err != nil {
		t.Fatalf("ForkSession() error = %v", err)
	}
	if started.SessionID != "s2" || svc.startReq.AppName != "caelis" || svc.startReq.Title != "Original" {
		t.Fatalf("ForkSession() started=%+v startReq=%+v", started, svc.startReq)
	}
	if got := svc.startReq.Metadata["forked_from_session_id"]; got != "s1" {
		t.Fatalf("fork metadata = %+v", svc.startReq.Metadata)
	}
	current, ok := gw.CurrentSession("surface-fork")
	if !ok || current.SessionID != "s2" {
		t.Fatalf("CurrentSession() = %+v, %v", current, ok)
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

func TestHandoffControllerDelegatesToRuntimeControlPlaneAndUpdatesBinding(t *testing.T) {
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

func TestAttachParticipantDelegatesToRuntimeControlPlaneAndUpdatesBinding(t *testing.T) {
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
		Agent:      "copilot",
		Role:       session.ParticipantRoleSidecar,
		Source:     "user_attach",
		Label:      "Copilot",
	})
	if err != nil {
		t.Fatalf("AttachParticipant() error = %v", err)
	}
	if len(updated.Participants) != 1 || rt.attachReq.Agent != "copilot" || rt.attachReq.SessionRef.SessionID != "s1" {
		t.Fatalf("updated=%+v attachReq=%+v", updated, rt.attachReq)
	}
	if binding, err := gw.LookupBinding(BindingStateRequest{BindingKey: "surface-agent"}); err != nil {
		t.Fatalf("LookupBinding() error = %v", err)
	} else if binding.SessionRef.SessionID != "s1" {
		t.Fatalf("binding session = %q, want s1", binding.SessionRef.SessionID)
	}
}

func TestDetachParticipantDelegatesToRuntimeControlPlaneAndUpdatesBinding(t *testing.T) {
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
	for len(runner.submissions) == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for active submission to reach runner")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if got, want := len(runner.submissions), 1; got != want {
		t.Fatalf("len(submissions) = %d, want %d", got, want)
	}
	if got := runner.submissions[0].Text; got != "steer next step" {
		t.Fatalf("submission text = %q, want steer text", got)
	}
	if got := runner.submissions[0].Metadata["source"]; got != "test" {
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

func TestBeginTurnDefaultsHeadlessSurfaceToNonStreaming(t *testing.T) {
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

	if rt.lastReq.Request.StreamEnabled(true) {
		t.Fatalf("runtime request stream = true, want false for headless default")
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
			Approved: true,
			Outcome:  "approved",
		},
	}); err != nil {
		t.Fatalf("Submit(approval) error = %v", err)
	}
	_ = collectHandleEvents(t, result.Handle)
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
			Approved: true,
			Outcome:  "approved",
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
				Usage:         &UsageSnapshot{PromptTokens: 7, CachedInputTokens: 3, CompletionTokens: 2, ReasoningTokens: 1, TotalTokens: 9},
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
		t.Fatalf("terminal review usage = %+v, want reviewer usage", usage)
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

func TestPersistApprovalReviewUsageUsesSessionStateNotHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
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
	if err := gw.persistApprovalReviewUsage(ctx, req, usage, string(ApprovalModeAutoReview), invocation); err != nil {
		t.Fatalf("persistApprovalReviewUsage() error = %v", err)
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

func TestBindSessionStoresBindingMetadataAndExpires(t *testing.T) {
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
	state, err := gw.LookupBinding(BindingStateRequest{BindingKey: "telegram:user-1"})
	if err != nil {
		t.Fatalf("LookupBinding() error = %v", err)
	}
	if state.Surface != "telegram" || state.ActorID != "user-1" || state.Owner != "telegram-bot" {
		t.Fatalf("binding state = %+v", state)
	}

	now = now.Add(2 * time.Minute)
	if _, ok := gw.CurrentSession("telegram:user-1"); ok {
		t.Fatal("CurrentSession() ok = true, want expired binding to be cleared")
	}
	_, err = gw.LookupBinding(BindingStateRequest{BindingKey: "telegram:user-1"})
	if err == nil {
		t.Fatal("LookupBinding() error = nil, want binding_not_found after expiry")
	}
}

func TestBeginTurnUpdatesBindingReplayState(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	runner := &recordingRunner{
		events: []*session.Event{{ID: "e1", Type: session.EventTypeAssistant}},
	}
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
	if err := gw.BindSession(context.Background(), BindSessionRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: "surface-1",
		Binding:    BindingDescriptor{Surface: "interactive"},
	}); err != nil {
		t.Fatalf("BindSession() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	_ = collectHandleEvents(t, result.Handle)

	state, err := gw.LookupBinding(BindingStateRequest{BindingKey: "surface-1"})
	if err != nil {
		t.Fatalf("LookupBinding() error = %v", err)
	}
	if state.LastEventCursor != "e1" || state.LastHandleID == "" || state.LastRunID == "" || state.LastTurnID == "" {
		t.Fatalf("binding replay state = %+v", state)
	}
}

func TestBeginTurnResolveFailurePreservesBindingReplayState(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	runner := &recordingRunner{
		events: []*session.Event{{ID: "e1", Type: session.EventTypeAssistant}},
	}
	resolver := &recordingResolver{resolved: ResolvedTurn{}}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime: &recordingRuntime{
			session: activeSession,
			result:  agent.RunResult{Session: activeSession, Handle: runner},
		},
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := gw.BindSession(context.Background(), BindSessionRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: "surface-1",
		Binding:    BindingDescriptor{Surface: "interactive"},
	}); err != nil {
		t.Fatalf("BindSession() error = %v", err)
	}
	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "first",
	})
	if err != nil {
		t.Fatalf("BeginTurn(first) error = %v", err)
	}
	_ = collectHandleEvents(t, result.Handle)
	before, err := gw.LookupBinding(BindingStateRequest{BindingKey: "surface-1"})
	if err != nil {
		t.Fatalf("LookupBinding(before) error = %v", err)
	}
	if before.LastHandleID == "" || before.LastRunID == "" || before.LastTurnID == "" || before.LastEventCursor != "e1" {
		t.Fatalf("binding before resolver failure = %+v", before)
	}

	resolver.err = errors.New("resolve failed")
	_, err = gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "second",
	})
	if err == nil {
		t.Fatal("BeginTurn(second) error = nil, want resolver failure")
	}
	after, err := gw.LookupBinding(BindingStateRequest{BindingKey: "surface-1"})
	if err != nil {
		t.Fatalf("LookupBinding(after) error = %v", err)
	}
	if after.LastHandleID != before.LastHandleID ||
		after.LastRunID != before.LastRunID ||
		after.LastTurnID != before.LastTurnID ||
		after.LastEventCursor != before.LastEventCursor {
		t.Fatalf("binding changed after resolver failure: before=%+v after=%+v", before, after)
	}
}

func TestPromptParticipantUpdatesBindingReplayState(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	runner := &recordingRunner{
		events: []*session.Event{{ID: "participant-e1", Type: session.EventTypeParticipant}},
	}
	rt := &controlPlaneRuntime{
		session:    activeSession,
		attachResp: activeSession,
		promptResp: agent.RunResult{Session: activeSession, Handle: runner},
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  rt,
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := gw.PromptParticipant(context.Background(), PromptParticipantRequest{
		SessionRef:    activeSession.SessionRef,
		BindingKey:    "surface-agent",
		ParticipantID: "side-1",
		Input:         "hello",
	})
	if err != nil {
		t.Fatalf("PromptParticipant() error = %v", err)
	}
	_ = collectHandleEvents(t, result.Handle)

	state, err := gw.LookupBinding(BindingStateRequest{BindingKey: "surface-agent"})
	if err != nil {
		t.Fatalf("LookupBinding() error = %v", err)
	}
	if state.LastEventCursor != "participant-e1" ||
		state.LastHandleID == "" ||
		state.LastRunID == "" ||
		state.LastTurnID == "" {
		t.Fatalf("participant binding replay state = %+v", state)
	}
}

func TestReplayEventsReturnsSessionBackedCanonicalReplay(t *testing.T) {
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
	}
	svc := &recordingSessionService{
		sessionResult: activeSession,
		eventsResult: []*session.Event{
			{ID: "e1", Type: session.EventTypeUser, Message: modelMessagePtr(model.NewTextMessage(model.RoleUser, "prompt")), Scope: &session.EventScope{TurnID: "turn-1"}},
			{ID: "e2", Type: session.EventTypeAssistant, Message: modelMessagePtr(model.NewTextMessage(model.RoleAssistant, "done")), Scope: &session.EventScope{
				TurnID:     "turn-1",
				Controller: session.ControllerRef{Kind: session.ControllerKindACP, ID: "acp-controller", EpochID: "epoch-1"},
				ACP:        session.ACPRef{SessionID: "acp-main", EventType: "agent_message_chunk"},
			}},
			{ID: "e3", Type: session.EventTypeToolResult, Scope: &session.EventScope{
				TurnID:      "turn-1",
				Controller:  session.ControllerRef{Kind: session.ControllerKindACP, ID: "acp-controller", EpochID: "epoch-1"},
				Participant: session.ParticipantRef{ID: "participant-1"},
			}, Tool: &session.EventTool{
				ID:     "tool-1",
				Name:   "RUN_COMMAND",
				Status: "completed",
				Output: map[string]any{"stdout": "ok"},
			}},
		},
	}
	rt := &controlPlaneRuntime{
		session:  activeSession,
		runState: agent.RunState{Status: agent.RunLifecycleStatusCompleted},
	}
	gw, err := New(Config{
		Sessions: svc,
		Runtime:  rt,
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := gw.BindSession(context.Background(), BindSessionRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: "surface-replay",
		Binding:    BindingDescriptor{Surface: "headless"},
	}); err != nil {
		t.Fatalf("BindSession() error = %v", err)
	}

	replayed, err := gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		BindingKey: "surface-replay",
		Cursor:     "e1",
	})
	if err != nil {
		t.Fatalf("ReplayEvents() error = %v", err)
	}
	// Participant-scoped tool trace stays out of transcript replay; control
	// plane continuity below still advances over e3.
	if len(replayed.Events) != 1 || replayed.Events[0].Cursor != "acp-projection:ZTI:0" || replayed.Events[0].EventID != "e2" {
		t.Fatalf("ReplayEvents() = %#v", replayed.Events)
	}
	if replayed.Events[0].Kind != eventstream.KindSessionUpdate ||
		eventstream.UpdateType(replayed.Events[0].Update) != schema.UpdateAgentMessage ||
		replayed.Events[0].TurnID != "turn-1" {
		t.Fatalf("first replay event = %+v", replayed.Events[0])
	}
	if !replayed.Durable || replayed.NextCursor != "acp-projection:ZTI:0" {
		t.Fatalf("replay result = %+v", replayed)
	}
	if replayed.ControlPlane.Continuity.LastEventCursor != "e3" || replayed.ControlPlane.Continuity.ControllerCursor != "e3" {
		t.Fatalf("replay continuity = %+v", replayed.ControlPlane.Continuity)
	}
	if replayed.ControlPlane.Continuity.ACPProjection.Cursor != "e2" {
		t.Fatalf("acp projection = %+v", replayed.ControlPlane.Continuity.ACPProjection)
	}
}

func TestReplayEventsIncludesDurableMirrorTranscriptEvents(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	mirror := session.MarkMirror(&session.Event{
		ID:   "e2",
		Type: session.EventTypeAssistant,
		Text: "partial answer",
		Scope: &session.EventScope{
			TurnID: "turn-1",
		},
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				Content: map[string]any{
					"type":          "assistant_snapshot",
					"text":          "partial answer",
					"reasoningText": "partial thought",
				},
			},
		},
	})
	svc := &recordingSessionService{
		sessionResult: activeSession,
		eventsResult: []*session.Event{
			{ID: "e1", Type: session.EventTypeUser, Text: "prompt", Message: modelMessagePtr(model.NewTextMessage(model.RoleUser, "prompt")), Scope: &session.EventScope{TurnID: "turn-1"}},
			mirror,
			session.MarkUIOnly(&session.Event{ID: "ui-1", Type: session.EventTypeAssistant, Text: "live only"}),
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

	replayed, err := gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("ReplayEvents() error = %v", err)
	}
	if !svc.eventsReq.IncludeTransient {
		t.Fatal("ReplayEvents() did not request durable transcript events")
	}
	if len(replayed.Events) != 3 {
		t.Fatalf("ReplayEvents().Events = %#v, want user + mirror thought + mirror assistant", replayed.Events)
	}
	thought, ok := replayed.Events[1].Update.(schema.ContentChunk)
	if !ok || thought.SessionUpdate != schema.UpdateAgentThought || schema.ExtractTextValue(thought.Content) != "partial thought" || !replayed.Events[1].Final {
		t.Fatalf("mirror reasoning update = %#v final=%v, want final mirror thought", replayed.Events[1].Update, replayed.Events[1].Final)
	}
	got, ok := replayed.Events[2].Update.(schema.ContentChunk)
	if !ok || got.SessionUpdate != schema.UpdateAgentMessage || schema.ExtractTextValue(got.Content) != "partial answer" || !replayed.Events[2].Final {
		t.Fatalf("mirror replay update = %#v final=%v, want final mirror assistant text", replayed.Events[2].Update, replayed.Events[2].Final)
	}
	if replayed.ControlPlane.Continuity.LastEventCursor != "e1" {
		t.Fatalf("control continuity = %+v, want mirror ignored", replayed.ControlPlane.Continuity)
	}
}

func TestReplayEventsResolvesBindingAndAppliesCursorLimit(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	svc := &recordingSessionService{
		sessionResult: activeSession,
		eventsResult: []*session.Event{
			{ID: "e1", Type: session.EventTypeUser, Text: "first", Message: modelMessagePtr(model.NewTextMessage(model.RoleUser, "first"))},
			{ID: "e2", Type: session.EventTypeAssistant, Text: "second", Message: modelMessagePtr(model.NewTextMessage(model.RoleAssistant, "second")), Visibility: session.VisibilityCanonical},
			{ID: "e3", Type: session.EventTypeAssistant, Text: "third", Message: modelMessagePtr(model.NewTextMessage(model.RoleAssistant, "third")), Visibility: session.VisibilityCanonical},
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
	if err := gw.BindSession(context.Background(), BindSessionRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: "surface-replay",
		Binding:    BindingDescriptor{Surface: "cli-tui"},
	}); err != nil {
		t.Fatalf("BindSession() error = %v", err)
	}

	replayed, err := gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		BindingKey: "surface-replay",
		Cursor:     "e1",
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("ReplayEvents() error = %v", err)
	}
	if len(replayed.Events) != 1 {
		t.Fatalf("ReplayEvents().Events len = %d, want 1", len(replayed.Events))
	}
	if got := replayed.Events[0].Cursor; got != "acp-projection:ZTI:0" {
		t.Fatalf("ReplayEvents().Events[0].Cursor = %q, want e2 projection cursor", got)
	}
	if replayed.NextCursor != "acp-projection:ZTI:0" {
		t.Fatalf("ReplayEvents().NextCursor = %q, want e2 projection cursor", replayed.NextCursor)
	}
}

func TestReplayEventsAcceptsCursorFromTraceThatFellOutOfReplayFilter(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	svc := &recordingSessionService{
		sessionResult: activeSession,
		eventsResult: []*session.Event{
			{
				ID:      "turn-1-user",
				Type:    session.EventTypeUser,
				Message: modelMessagePtr(model.NewTextMessage(model.RoleUser, "run command")),
				Scope:   &session.EventScope{TurnID: "turn-1"},
			},
			{
				ID:    "turn-1-tool-call",
				Type:  session.EventTypeToolCall,
				Tool:  &session.EventTool{ID: "call-1", Name: "RUN_COMMAND", Status: "running", Input: map[string]any{"command": "sleep 10"}},
				Scope: &session.EventScope{TurnID: "turn-1"},
			},
			{
				ID:    "turn-1-tool-result",
				Type:  session.EventTypeToolResult,
				Tool:  &session.EventTool{ID: "call-1", Name: "RUN_COMMAND", Status: "interrupted", Output: map[string]any{"stderr": "interrupted"}},
				Scope: &session.EventScope{TurnID: "turn-1"},
			},
			{
				ID:      "turn-2-user",
				Type:    session.EventTypeUser,
				Message: modelMessagePtr(model.NewTextMessage(model.RoleUser, "next prompt")),
				Scope:   &session.EventScope{TurnID: "turn-2"},
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
	if err := gw.BindSession(context.Background(), BindSessionRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: "surface-replay",
		Binding:    BindingDescriptor{Surface: "cli-tui"},
	}); err != nil {
		t.Fatalf("BindSession() error = %v", err)
	}

	replayed, err := gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		BindingKey: "surface-replay",
		Cursor:     "turn-1-tool-result",
	})
	if err != nil {
		t.Fatalf("ReplayEvents() error = %v", err)
	}
	if len(replayed.Events) != 1 || replayed.Events[0].Cursor != "acp-projection:dHVybi0yLXVzZXI:0" {
		t.Fatalf("ReplayEvents().Events = %#v, want turn-2 user after raw cursor", replayed.Events)
	}
	if replayed.NextCursor != "acp-projection:dHVybi0yLXVzZXI:0" {
		t.Fatalf("ReplayEvents().NextCursor = %q, want turn-2-user projection cursor", replayed.NextCursor)
	}
}

func TestReplayEventsReturnsCursorNotFoundForStaleCursor(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	svc := &recordingSessionService{
		sessionResult: activeSession,
		eventsResult: []*session.Event{
			{ID: "e1", Type: session.EventTypeUser, Text: "first", Message: modelMessagePtr(model.NewTextMessage(model.RoleUser, "first"))},
			{ID: "e2", Type: session.EventTypeAssistant, Text: "second", Message: modelMessagePtr(model.NewTextMessage(model.RoleAssistant, "second"))},
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
	if err := gw.BindSession(context.Background(), BindSessionRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: "surface-replay",
		Binding:    BindingDescriptor{Surface: "cli-tui"},
	}); err != nil {
		t.Fatalf("BindSession() error = %v", err)
	}

	_, err = gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		BindingKey: "surface-replay",
		Cursor:     "missing",
	})
	if err == nil {
		t.Fatal("ReplayEvents() error = nil, want cursor_not_found")
	}
	var gwErr *Error
	if !As(err, &gwErr) || gwErr.Code != CodeCursorNotFound {
		t.Fatalf("ReplayEvents() error = %v, want cursor_not_found", err)
	}
}

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

type staticApprovalReviewer struct {
	result ApprovalReviewResult
}

func (r staticApprovalReviewer) ReviewApproval(context.Context, ApprovalReviewRequest) (ApprovalReviewResult, error) {
	return r.result, nil
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
	detachReq   agent.DetachParticipantRequest
	detachResp  session.Session
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
	if r.promptResp.Handle != nil || r.promptResp.Session.SessionID != "" {
		return r.promptResp, nil
	}
	return agent.RunResult{Session: r.attachResp}, nil
}

func (r *controlPlaneRuntime) DetachParticipant(_ context.Context, req agent.DetachParticipantRequest) (session.Session, error) {
	r.detachReq = req
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
	return req.Event, nil
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
func (s staticSessionService) ReplaceState(context.Context, session.SessionRef, map[string]any) error {
	return nil
}
func (s staticSessionService) UpdateState(context.Context, session.SessionRef, func(map[string]any) (map[string]any, error)) error {
	return nil
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
	sessionResult      session.Session
	eventsResult       []*session.Event
	snapshotErr        error
	startErr           error
	loadErr            error
	listErr            error
	sessionErr         error
	eventsErr          error
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

func (s *recordingSessionService) Events(_ context.Context, req session.EventsRequest) ([]*session.Event, error) {
	s.eventsReq = req
	if s.eventsErr != nil {
		return nil, s.eventsErr
	}
	return append([]*session.Event(nil), s.eventsResult...), nil
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
	if s.snapshotErr != nil {
		return nil, s.snapshotErr
	}
	return map[string]any{}, nil
}

func (s *recordingSessionService) ReplaceState(context.Context, session.SessionRef, map[string]any) error {
	return nil
}

func (s *recordingSessionService) UpdateState(context.Context, session.SessionRef, func(map[string]any) (map[string]any, error)) error {
	return nil
}

type staticResolver struct {
	resolved ResolvedTurn
	err      error
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
	submissions []agent.Submission
}

func (r *submitRecordingBlockingRunner) RunID() string { return "run-submit-blocking" }

func (r *submitRecordingBlockingRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		<-r.release
	}
}

func (r *submitRecordingBlockingRunner) Submit(sub agent.Submission) error {
	r.submissions = append(r.submissions, agent.CloneSubmission(sub))
	return nil
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
