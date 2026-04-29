package core

import (
	"context"
	"iter"
	"testing"
	"time"

	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

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

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		CWD: "/tmp/ws",
	}
	svc := staticSessionService{session: session}
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
		Workspace: sdksession.WorkspaceRef{
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

	loaded := sdksession.LoadedSession{
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{
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

	want := sdksession.SessionList{
		Sessions: []sdksession.SessionSummary{{SessionRef: sdksession.SessionRef{
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

func TestResumeSessionUsesMostRecentExcludingCurrentBinding(t *testing.T) {
	t.Parallel()

	current := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		CWD: "/tmp/ws",
	}
	next := sdksession.LoadedSession{
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s2", WorkspaceKey: "ws",
			},
			CWD: "/tmp/ws",
		},
	}
	svc := &recordingSessionService{
		startSessionResult: current,
		loadSessionResult:  next,
		listSessionsResult: sdksession.SessionList{
			Sessions: []sdksession.SessionSummary{
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
		Workspace:  sdksession.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
		BindingKey: "surface-1",
	}); err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	loaded, err := gw.ResumeSession(context.Background(), ResumeSessionRequest{
		AppName:    "caelis",
		UserID:     "u",
		Workspace:  sdksession.WorkspaceRef{Key: "ws"},
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

	target := sdksession.LoadedSession{
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{
				AppName: "caelis", UserID: "u", SessionID: "s-12345678", WorkspaceKey: "ws",
			},
		},
	}
	svc := &recordingSessionService{
		loadSessionResult: target,
		listSessionsResult: sdksession.SessionList{
			Sessions: []sdksession.SessionSummary{
				{SessionRef: target.Session.SessionRef},
				{SessionRef: sdksession.SessionRef{AppName: "caelis", UserID: "u", SessionID: "s-87654321", WorkspaceKey: "ws"}},
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
		Workspace: sdksession.WorkspaceRef{Key: "ws"},
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
		listSessionsResult: sdksession.SessionList{
			Sessions: []sdksession.SessionSummary{
				{SessionRef: sdksession.SessionRef{AppName: "caelis", UserID: "u", SessionID: "s-12345678", WorkspaceKey: "ws"}},
				{SessionRef: sdksession.SessionRef{AppName: "caelis", UserID: "u", SessionID: "s-12349999", WorkspaceKey: "ws"}},
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
		Workspace: sdksession.WorkspaceRef{Key: "ws"},
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

	source := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		CWD:      "/tmp/ws",
		Title:    "Original",
		Metadata: map[string]any{"mode": "main"},
	}
	forked := sdksession.Session{
		SessionRef: sdksession.SessionRef{
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

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		CWD: "/tmp/ws",
	}
	rt := &cancellableRuntime{session: session, cancelled: make(chan struct{})}
	svc := &recordingSessionService{startSessionResult: session, sessionResult: session}
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
		Workspace:  sdksession.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
		BindingKey: "surface-1",
	}); err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	if _, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: session.SessionRef,
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

func TestHandoffControllerDelegatesToRuntimeControlPlaneAndUpdatesBinding(t *testing.T) {
	t.Parallel()

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		Controller: sdksession.ControllerBinding{
			Kind:         sdksession.ControllerKindACP,
			ControllerID: "acp-controller",
			EpochID:      "epoch-1",
		},
		Participants: []sdksession.ParticipantBinding{{
			ID:            "p-1",
			Kind:          sdksession.ParticipantKindACP,
			Role:          sdksession.ParticipantRoleDelegated,
			ControllerRef: "epoch-1",
		}},
	}
	rt := &controlPlaneRuntime{
		session:     session,
		runState:    sdkruntime.RunState{Status: sdkruntime.RunLifecycleStatusRunning, ActiveRunID: "run-1"},
		handoffResp: session,
	}
	svc := &recordingSessionService{
		sessionResult:      session,
		startSessionResult: session,
		eventsResult: []*sdksession.Event{
			{
				ID:   "evt-1",
				Type: sdksession.EventTypeHandoff,
				Scope: &sdksession.EventScope{
					Controller: sdksession.ControllerRef{Kind: sdksession.ControllerKindACP, ID: "acp-controller", EpochID: "epoch-1"},
					ACP:        sdksession.ACPRef{SessionID: "acp-main", EventType: "agent_message_chunk"},
				},
			},
			{
				ID:   "evt-2",
				Type: sdksession.EventTypeParticipant,
				Scope: &sdksession.EventScope{
					Controller:  sdksession.ControllerRef{Kind: sdksession.ControllerKindACP, ID: "acp-controller", EpochID: "epoch-1"},
					Participant: sdksession.ParticipantRef{ID: "p-1", Kind: sdksession.ParticipantKindACP},
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
		Workspace:  sdksession.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
		BindingKey: "surface-acp",
	}); err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	updated, err := gw.HandoffController(context.Background(), HandoffControllerRequest{
		BindingKey: "surface-acp",
		Kind:       sdksession.ControllerKindACP,
		Agent:      "codex",
		Source:     "user",
		Reason:     "delegate main control",
	})
	if err != nil {
		t.Fatalf("HandoffController() error = %v", err)
	}
	if updated.Controller.Kind != sdksession.ControllerKindACP || rt.handoffReq.Agent != "codex" || rt.handoffReq.SessionRef.SessionID != "s1" {
		t.Fatalf("updated=%+v handoffReq=%+v", updated, rt.handoffReq)
	}

	state, err := gw.ControlPlaneState(context.Background(), ControlPlaneStateRequest{BindingKey: "surface-acp"})
	if err != nil {
		t.Fatalf("ControlPlaneState() error = %v", err)
	}
	if state.Controller.Kind != sdksession.ControllerKindACP || state.Controller.EpochID != "epoch-1" || !state.HasActiveTurn {
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

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: session},
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = gw.HandoffController(context.Background(), HandoffControllerRequest{
		SessionRef: session.SessionRef,
		Kind:       sdksession.ControllerKindACP,
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

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		Participants: []sdksession.ParticipantBinding{{
			ID:    "participant-1",
			Kind:  sdksession.ParticipantKindACP,
			Role:  sdksession.ParticipantRoleSidecar,
			Label: "Copilot",
		}},
	}
	rt := &controlPlaneRuntime{
		session:    session,
		attachResp: session,
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: session},
		Runtime:  rt,
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := gw.StartSession(context.Background(), StartSessionRequest{
		AppName:    "caelis",
		UserID:     "u",
		Workspace:  sdksession.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
		BindingKey: "surface-agent",
	}); err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	updated, err := gw.AttachParticipant(context.Background(), AttachParticipantRequest{
		BindingKey: "surface-agent",
		Agent:      "copilot",
		Role:       sdksession.ParticipantRoleSidecar,
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

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &controlPlaneRuntime{
		session:    session,
		detachResp: session,
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: session},
		Runtime:  rt,
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := gw.StartSession(context.Background(), StartSessionRequest{
		AppName:    "caelis",
		UserID:     "u",
		Workspace:  sdksession.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
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

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &blockingRuntime{session: session}
	gw, err := New(Config{
		Sessions: staticSessionService{session: session},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	first, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "first",
	})
	if err != nil {
		t.Fatalf("BeginTurn(first) error = %v", err)
	}
	defer first.Handle.Close()

	_, err = gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: session.SessionRef,
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

func TestBeginTurnPassesIntentToResolver(t *testing.T) {
	t.Parallel()

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	resolver := &recordingResolver{resolved: ResolvedTurn{}}
	gw, err := New(Config{
		Sessions: staticSessionService{session: session},
		Runtime:  mockRuntime{},
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: session.SessionRef,
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

func TestBeginTurnLoadsSessionResolvesIntentRunsRuntimeAndPublishesEvents(t *testing.T) {
	t.Parallel()

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	runner := &recordingRunner{
		events: []*sdksession.Event{{ID: "e1", Type: sdksession.EventTypeAssistant, Text: "ok"}},
	}
	rt := &recordingRuntime{
		session: session,
		result:  sdkruntime.RunResult{Session: session, Handle: runner},
	}
	resolver := &recordingResolver{resolved: ResolvedTurn{
		RunRequest: sdkruntime.RunRequest{Input: "hello"},
	}}
	gw, err := New(Config{
		Sessions: staticSessionService{session: session},
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
		SessionRef: session.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	got := collectHandleEvents(t, result.Handle)
	if len(got) == 0 || got[len(got)-1].Cursor != "e1" || got[len(got)-1].Event.Narrative == nil {
		t.Fatalf("published events = %#v, want assistant event e1", got)
	}
	if rt.lastReq.SessionRef != session.SessionRef || rt.lastReq.Input != "hello" {
		t.Fatalf("runtime req = %+v", rt.lastReq)
	}
}

func TestGatewayActiveTurnsReportsSessionScopedState(t *testing.T) {
	t.Parallel()

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	runner := &blockingRunner{release: make(chan struct{})}
	rt := &recordingRuntime{
		session: session,
		result:  sdkruntime.RunResult{Session: session, Handle: runner},
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: session},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: session.SessionRef,
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

func TestBeginTurnDefaultsToStreamingRequestsAtGatewayBoundary(t *testing.T) {
	t.Parallel()

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &recordingRuntime{
		session: session,
		result:  sdkruntime.RunResult{Session: session, Handle: &recordingRunner{}},
		ran:     make(chan struct{}),
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: session},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: session.SessionRef,
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

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &recordingRuntime{
		session: session,
		result:  sdkruntime.RunResult{Session: session, Handle: &recordingRunner{}},
		ran:     make(chan struct{}),
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: session},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "hello",
		Surface:    "headless",
		Request:    sdkruntime.ModelRequestOptions{Stream: boolPtr(false)},
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

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &recordingRuntime{
		session: session,
		result:  sdkruntime.RunResult{Session: session, Handle: &recordingRunner{}},
		ran:     make(chan struct{}),
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: session},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: session.SessionRef,
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

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	rt := &approvalRuntime{session: session}
	gw, err := New(Config{
		Sessions: staticSessionService{session: session},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{RunRequest: sdkruntime.RunRequest{}}},
		Clock: func() time.Time {
			return time.Unix(100, 0)
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "hello",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}

	first := <-result.Handle.Events()
	if first.Event.Kind != EventKindApprovalRequested {
		t.Fatalf("first event kind = %q, want approval_requested", first.Event.Kind)
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
	got := collectHandleEvents(t, result.Handle)
	if len(got) == 0 {
		t.Fatal("collectHandleEvents() = empty, want completion event stream")
	}
}

func TestBindSessionStoresBindingMetadataAndExpires(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0)
	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: session},
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
		SessionRef: session.SessionRef,
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

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	runner := &recordingRunner{
		events: []*sdksession.Event{{ID: "e1", Type: sdksession.EventTypeAssistant}},
	}
	rt := &recordingRuntime{
		session: session,
		result:  sdkruntime.RunResult{Session: session, Handle: runner},
	}
	gw, err := New(Config{
		Sessions: staticSessionService{session: session},
		Runtime:  rt,
		Resolver: staticResolver{resolved: ResolvedTurn{}},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := gw.BindSession(context.Background(), BindSessionRequest{
		SessionRef: session.SessionRef,
		BindingKey: "surface-1",
		Binding:    BindingDescriptor{Surface: "interactive"},
	}); err != nil {
		t.Fatalf("BindSession() error = %v", err)
	}

	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: session.SessionRef,
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

func TestReplayEventsReturnsSessionBackedCanonicalReplay(t *testing.T) {
	t.Parallel()

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		Controller: sdksession.ControllerBinding{
			Kind:         sdksession.ControllerKindACP,
			ControllerID: "acp-controller",
			EpochID:      "epoch-1",
		},
	}
	svc := &recordingSessionService{
		sessionResult: session,
		eventsResult: []*sdksession.Event{
			{ID: "e1", Type: sdksession.EventTypeUser, Scope: &sdksession.EventScope{TurnID: "turn-1"}},
			{ID: "e2", Type: sdksession.EventTypeAssistant, Scope: &sdksession.EventScope{
				TurnID:     "turn-1",
				Controller: sdksession.ControllerRef{Kind: sdksession.ControllerKindACP, ID: "acp-controller", EpochID: "epoch-1"},
				ACP:        sdksession.ACPRef{SessionID: "acp-main", EventType: "agent_message_chunk"},
			}},
			{ID: "e3", Type: sdksession.EventTypeToolResult, Scope: &sdksession.EventScope{
				TurnID:      "turn-1",
				Controller:  sdksession.ControllerRef{Kind: sdksession.ControllerKindACP, ID: "acp-controller", EpochID: "epoch-1"},
				Participant: sdksession.ParticipantRef{ID: "participant-1"},
			}},
		},
	}
	rt := &controlPlaneRuntime{
		session:  session,
		runState: sdkruntime.RunState{Status: sdkruntime.RunLifecycleStatusCompleted},
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
		SessionRef: session.SessionRef,
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
	if len(replayed.Events) != 2 || replayed.Events[0].Cursor != "e2" || replayed.Events[1].Cursor != "e3" {
		t.Fatalf("ReplayEvents() = %#v", replayed.Events)
	}
	if replayed.Events[0].Event.Kind != EventKindAssistantMessage || replayed.Events[0].Event.TurnID != "turn-1" {
		t.Fatalf("first replay event = %+v", replayed.Events[0])
	}
	if !replayed.Durable || replayed.NextCursor != "e3" {
		t.Fatalf("replay result = %+v", replayed)
	}
	if replayed.ControlPlane.Continuity.LastEventCursor != "e3" || replayed.ControlPlane.Continuity.ControllerCursor != "e3" {
		t.Fatalf("replay continuity = %+v", replayed.ControlPlane.Continuity)
	}
	if replayed.ControlPlane.Continuity.ACPProjection.Cursor != "e2" {
		t.Fatalf("acp projection = %+v", replayed.ControlPlane.Continuity.ACPProjection)
	}
}

func TestReplayEventsResolvesBindingAndAppliesCursorLimit(t *testing.T) {
	t.Parallel()

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	svc := &recordingSessionService{
		sessionResult: session,
		eventsResult: []*sdksession.Event{
			{ID: "e1", Type: sdksession.EventTypeUser, Text: "first"},
			{ID: "e2", Type: sdksession.EventTypeAssistant, Text: "second", Visibility: sdksession.VisibilityCanonical},
			{ID: "e3", Type: sdksession.EventTypeAssistant, Text: "third", Visibility: sdksession.VisibilityCanonical},
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
		SessionRef: session.SessionRef,
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
	if got := replayed.Events[0].Cursor; got != "e2" {
		t.Fatalf("ReplayEvents().Events[0].Cursor = %q, want e2", got)
	}
	if replayed.NextCursor != "e2" {
		t.Fatalf("ReplayEvents().NextCursor = %q, want e2", replayed.NextCursor)
	}
}

func TestReplayEventsReturnsCursorNotFoundForStaleCursor(t *testing.T) {
	t.Parallel()

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	svc := &recordingSessionService{
		sessionResult: session,
		eventsResult: []*sdksession.Event{
			{ID: "e1", Type: sdksession.EventTypeUser, Text: "first"},
			{ID: "e2", Type: sdksession.EventTypeAssistant, Text: "second"},
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
		SessionRef: session.SessionRef,
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
	ran     chan struct{}
}

func (r *recordingRuntime) Run(_ context.Context, req sdkruntime.RunRequest) (sdkruntime.RunResult, error) {
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

func (r *recordingRuntime) RunState(context.Context, sdksession.SessionRef) (sdkruntime.RunState, error) {
	return sdkruntime.RunState{}, nil
}

type approvalRuntime struct {
	session sdksession.Session
}

func (r *approvalRuntime) Run(ctx context.Context, req sdkruntime.RunRequest) (sdkruntime.RunResult, error) {
	if req.ApprovalRequester == nil {
		return sdkruntime.RunResult{}, nil
	}
	_, err := req.ApprovalRequester.RequestApproval(ctx, sdkruntime.ApprovalRequest{
		SessionRef: r.session.SessionRef,
		Session:    r.session,
		RunID:      "run-1",
		TurnID:     "turn-1",
	})
	if err != nil {
		return sdkruntime.RunResult{}, err
	}
	return sdkruntime.RunResult{
		Session: r.session,
		Handle: &recordingRunner{
			events: []*sdksession.Event{{ID: "approved", Type: sdksession.EventTypeNotice}},
		},
	}, nil
}

func (r *approvalRuntime) RunState(context.Context, sdksession.SessionRef) (sdkruntime.RunState, error) {
	return sdkruntime.RunState{}, nil
}

type blockingRuntime struct {
	session sdksession.Session
	wait    chan struct{}
}

func (r *blockingRuntime) Run(context.Context, sdkruntime.RunRequest) (sdkruntime.RunResult, error) {
	if r.wait == nil {
		r.wait = make(chan struct{})
	}
	<-r.wait
	return sdkruntime.RunResult{
		Session: r.session,
		Handle:  &recordingRunner{},
	}, nil
}

func (r *blockingRuntime) RunState(context.Context, sdksession.SessionRef) (sdkruntime.RunState, error) {
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

type controlPlaneRuntime struct {
	session     sdksession.Session
	runState    sdkruntime.RunState
	handoffReq  sdkruntime.HandoffControllerRequest
	handoffResp sdksession.Session
	attachReq   sdkruntime.AttachACPParticipantRequest
	attachResp  sdksession.Session
	promptReq   sdkruntime.PromptACPParticipantRequest
	detachReq   sdkruntime.DetachACPParticipantRequest
	detachResp  sdksession.Session
}

func (r *controlPlaneRuntime) Run(context.Context, sdkruntime.RunRequest) (sdkruntime.RunResult, error) {
	return sdkruntime.RunResult{Session: r.session}, nil
}

func (r *controlPlaneRuntime) RunState(context.Context, sdksession.SessionRef) (sdkruntime.RunState, error) {
	return r.runState, nil
}

func (r *controlPlaneRuntime) HandoffController(_ context.Context, req sdkruntime.HandoffControllerRequest) (sdksession.Session, error) {
	r.handoffReq = req
	return r.handoffResp, nil
}

func (r *controlPlaneRuntime) AttachACPParticipant(_ context.Context, req sdkruntime.AttachACPParticipantRequest) (sdksession.Session, error) {
	r.attachReq = req
	return r.attachResp, nil
}

func (r *controlPlaneRuntime) PromptACPParticipant(_ context.Context, req sdkruntime.PromptACPParticipantRequest) (sdksession.Session, error) {
	r.promptReq = req
	return r.attachResp, nil
}

func (r *controlPlaneRuntime) DetachACPParticipant(_ context.Context, req sdkruntime.DetachACPParticipantRequest) (sdksession.Session, error) {
	r.detachReq = req
	return r.detachResp, nil
}

type staticSessionService struct {
	session sdksession.Session
}

func (s staticSessionService) StartSession(context.Context, sdksession.StartSessionRequest) (sdksession.Session, error) {
	return s.session, nil
}

func (s staticSessionService) LoadSession(context.Context, sdksession.LoadSessionRequest) (sdksession.LoadedSession, error) {
	return sdksession.LoadedSession{Session: s.session}, nil
}

func (s staticSessionService) Session(context.Context, sdksession.SessionRef) (sdksession.Session, error) {
	return s.session, nil
}

func (s staticSessionService) AppendEvent(_ context.Context, req sdksession.AppendEventRequest) (*sdksession.Event, error) {
	return req.Event, nil
}
func (s staticSessionService) Events(context.Context, sdksession.EventsRequest) ([]*sdksession.Event, error) {
	return nil, nil
}
func (s staticSessionService) ListSessions(context.Context, sdksession.ListSessionsRequest) (sdksession.SessionList, error) {
	return sdksession.SessionList{}, nil
}
func (s staticSessionService) BindController(context.Context, sdksession.BindControllerRequest) (sdksession.Session, error) {
	return s.session, nil
}
func (s staticSessionService) PutParticipant(context.Context, sdksession.PutParticipantRequest) (sdksession.Session, error) {
	return s.session, nil
}
func (s staticSessionService) RemoveParticipant(context.Context, sdksession.RemoveParticipantRequest) (sdksession.Session, error) {
	return s.session, nil
}
func (s staticSessionService) SnapshotState(context.Context, sdksession.SessionRef) (map[string]any, error) {
	return map[string]any{}, nil
}
func (s staticSessionService) ReplaceState(context.Context, sdksession.SessionRef, map[string]any) error {
	return nil
}
func (s staticSessionService) UpdateState(context.Context, sdksession.SessionRef, func(map[string]any) (map[string]any, error)) error {
	return nil
}

type mockSessionService struct{ staticSessionService }

type recordingSessionService struct {
	startReq           sdksession.StartSessionRequest
	loadReq            sdksession.LoadSessionRequest
	eventsReq          sdksession.EventsRequest
	listReq            sdksession.ListSessionsRequest
	sessionReq         sdksession.SessionRef
	startSessionResult sdksession.Session
	loadSessionResult  sdksession.LoadedSession
	listSessionsResult sdksession.SessionList
	sessionResult      sdksession.Session
	eventsResult       []*sdksession.Event
	startErr           error
	loadErr            error
	listErr            error
	sessionErr         error
	eventsErr          error
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

func (s *recordingSessionService) Events(_ context.Context, req sdksession.EventsRequest) ([]*sdksession.Event, error) {
	s.eventsReq = req
	if s.eventsErr != nil {
		return nil, s.eventsErr
	}
	return append([]*sdksession.Event(nil), s.eventsResult...), nil
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
	resolved ResolvedTurn
}

func (r staticResolver) ResolveTurn(context.Context, TurnIntent) (ResolvedTurn, error) {
	return r.resolved, nil
}

type recordingResolver struct {
	resolved   ResolvedTurn
	lastIntent TurnIntent
}

func (r *recordingResolver) ResolveTurn(_ context.Context, intent TurnIntent) (ResolvedTurn, error) {
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

type blockingRunner struct {
	release chan struct{}
}

func (blockingRunner) RunID() string { return "run-blocking" }

func (r blockingRunner) Events() iter.Seq2[*sdksession.Event, error] {
	return func(yield func(*sdksession.Event, error) bool) {
		<-r.release
	}
}

func (blockingRunner) Submit(sdkruntime.Submission) error { return nil }
func (blockingRunner) Cancel() bool                       { return true }
func (blockingRunner) Close() error                       { return nil }

func TestSanityTestClock(t *testing.T) {
	t.Parallel()
	if time.Unix(100, 0).IsZero() {
		t.Fatal("unexpected zero time")
	}
}

func collectHandleEvents(t *testing.T, handle TurnHandle) []EventEnvelope {
	t.Helper()

	var out []EventEnvelope
	timeout := time.After(2 * time.Second)
	for {
		select {
		case env, ok := <-handle.Events():
			if !ok {
				return out
			}
			out = append(out, env)
		case <-timeout:
			t.Fatalf("timed out waiting for handle events: %#v", out)
		}
	}
}
