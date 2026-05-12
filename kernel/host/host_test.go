package host

import (
	"context"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestRemoteBindingKeyUsesRouteIdentity(t *testing.T) {
	t.Parallel()

	key := remoteBindingKey("", RemoteAddress{
		Surface:   "telegram",
		Channel:   "bot",
		AccountID: "user-1",
		ThreadID:  "chat-9",
	})
	if got, want := key, "telegram:bot:user-1:chat-9"; got != want {
		t.Fatalf("remoteBindingKey() = %q, want %q", got, want)
	}
}

func TestHostEnsureRemoteSessionPrefersCurrentBinding(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{SessionID: "session-bound", AppName: "caelis", UserID: "user-1", WorkspaceKey: "ws"},
	}
	sessions := &recordingSessionService{
		sessionResult:     activeSession,
		loadSessionResult: session.LoadedSession{Session: activeSession},
	}
	gw, err := kernel.New(kernel.Config{
		Sessions: sessions,
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := gw.BindSession(context.Background(), kernel.BindSessionRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: "telegram:bot:user-1:chat-9",
		Binding:    kernel.BindingDescriptor{Surface: "telegram"},
	}); err != nil {
		t.Fatalf("BindSession() error = %v", err)
	}

	host, err := NewHost(HostConfig{Gateway: gw, ID: "host-1"})
	if err != nil {
		t.Fatalf("NewHost() error = %v", err)
	}
	got, err := host.EnsureRemoteSession(context.Background(), RemoteSessionRequest{
		AppName:   "caelis",
		UserID:    "user-1",
		Workspace: session.WorkspaceRef{Key: "ws"},
		Address:   RemoteAddress{Surface: "telegram", Channel: "bot", AccountID: "user-1", ThreadID: "chat-9"},
		Actor:     RemoteActor{ID: "user-1"},
		Owner:     "gateway-daemon",
	})
	if err != nil {
		t.Fatalf("EnsureRemoteSession() error = %v", err)
	}
	if got.SessionID != activeSession.SessionID {
		t.Fatalf("EnsureRemoteSession().SessionID = %q, want %q", got.SessionID, activeSession.SessionID)
	}
	if sessions.loadReq.SessionRef.SessionID != activeSession.SessionID {
		t.Fatalf("LoadSession() session = %q, want %q", sessions.loadReq.SessionRef.SessionID, activeSession.SessionID)
	}
}

func TestHostBeginRemoteTurnStartsSessionAndUsesRemoteSurface(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{SessionID: "session-new", AppName: "caelis", UserID: "user-1", WorkspaceKey: "ws"},
	}
	runtime := &recordingRuntime{
		session: activeSession,
		result: agent.RunResult{
			Session: activeSession,
			Handle: &recordingRunner{
				events: []*session.Event{{ID: "done", Type: session.EventTypeAssistant, Text: "ok"}},
			},
		},
	}
	resolver := &recordingResolver{resolved: kernel.ResolvedTurn{}}
	sessions := &recordingSessionService{
		startSessionResult: activeSession,
		sessionResult:      activeSession,
		listSessionsResult: session.SessionList{},
	}
	gw, err := kernel.New(kernel.Config{
		Sessions: sessions,
		Runtime:  runtime,
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	host, err := NewHost(HostConfig{Gateway: gw, Mode: HostModeDaemon})
	if err != nil {
		t.Fatalf("NewHost() error = %v", err)
	}

	result, err := host.BeginRemoteTurn(context.Background(), RemoteTurnRequest{
		Session: RemoteSessionRequest{
			AppName:   "caelis",
			UserID:    "user-1",
			Workspace: session.WorkspaceRef{Key: "ws"},
			Address:   RemoteAddress{Surface: "telegram", Channel: "bot", AccountID: "user-1", ThreadID: "chat-9"},
			Actor:     RemoteActor{Kind: "user", ID: "user-1"},
			Owner:     "gateway-daemon",
		},
		Input:    "remote hello",
		ModeName: "plan",
		Request:  agent.ModelRequestOptions{Stream: boolPtr(false)},
	})
	if err != nil {
		t.Fatalf("BeginRemoteTurn() error = %v", err)
	}
	defer result.Handle.Close()
	drained := false
	for range result.Handle.Events() {
		drained = true
	}
	if !drained {
		t.Fatal("expected remote turn to emit at least one event")
	}
	if resolver.lastIntent.Surface != "telegram" {
		t.Fatalf("resolver surface = %q, want telegram", resolver.lastIntent.Surface)
	}
	if resolver.lastIntent.ModeName != "plan" {
		t.Fatalf("resolver mode = %q, want plan", resolver.lastIntent.ModeName)
	}
	if runtime.lastReq.SessionRef.SessionID != activeSession.SessionID {
		t.Fatalf("runtime session = %q, want %q", runtime.lastReq.SessionRef.SessionID, activeSession.SessionID)
	}
	if runtime.lastReq.Request.StreamEnabled(true) {
		t.Fatalf("runtime request stream = true, want explicit false override")
	}
	if state, err := gw.LookupBinding(kernel.BindingStateRequest{BindingKey: "telegram:bot:user-1:chat-9"}); err != nil {
		t.Fatalf("LookupBinding() error = %v", err)
	} else if state.SessionRef.SessionID != activeSession.SessionID {
		t.Fatalf("binding session = %q, want %q", state.SessionRef.SessionID, activeSession.SessionID)
	}
}

func TestHostEnsureRemoteSessionPersistsBindingExpiry(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0)
	activeSession := session.Session{
		SessionRef: session.SessionRef{SessionID: "session-expiry", AppName: "caelis", UserID: "user-1", WorkspaceKey: "ws"},
	}
	sessions := &recordingSessionService{
		startSessionResult: activeSession,
		sessionResult:      activeSession,
		listSessionsResult: session.SessionList{},
	}
	gw, err := kernel.New(kernel.Config{
		Sessions: sessions,
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
		Clock: func() time.Time {
			return now
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	host, err := NewHost(HostConfig{Gateway: gw})
	if err != nil {
		t.Fatalf("NewHost() error = %v", err)
	}
	expiresAt := now.Add(time.Minute)
	got, err := host.EnsureRemoteSession(context.Background(), RemoteSessionRequest{
		AppName:   "caelis",
		UserID:    "user-1",
		Workspace: session.WorkspaceRef{Key: "ws"},
		Address:   RemoteAddress{Surface: "telegram", Channel: "bot", AccountID: "user-1", ThreadID: "chat-9"},
		ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("EnsureRemoteSession() error = %v", err)
	}
	if got.SessionID != activeSession.SessionID {
		t.Fatalf("EnsureRemoteSession().SessionID = %q, want %q", got.SessionID, activeSession.SessionID)
	}
	state, err := gw.LookupBinding(kernel.BindingStateRequest{BindingKey: "telegram:bot:user-1:chat-9"})
	if err != nil {
		t.Fatalf("LookupBinding() error = %v", err)
	}
	if !state.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("binding expiry = %s, want %s", state.ExpiresAt, expiresAt)
	}
}

func TestHostShutdownCancelsActiveTurns(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{SessionID: "session-1", AppName: "caelis", UserID: "user-1", WorkspaceKey: "ws"},
	}
	cancelled := make(chan struct{})
	runtime := &cancellableRuntime{
		session:   activeSession,
		cancelled: cancelled,
	}
	sessions := &recordingSessionService{
		startSessionResult: activeSession,
		sessionResult:      activeSession,
		listSessionsResult: session.SessionList{},
	}
	gw, err := kernel.New(kernel.Config{
		Sessions: sessions,
		Runtime:  runtime,
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	host, err := NewHost(HostConfig{Gateway: gw})
	if err != nil {
		t.Fatalf("NewHost() error = %v", err)
	}

	result, err := host.BeginRemoteTurn(context.Background(), RemoteTurnRequest{
		Session: RemoteSessionRequest{
			AppName:   "caelis",
			UserID:    "user-1",
			Workspace: session.WorkspaceRef{Key: "ws"},
			Address:   RemoteAddress{Surface: "discord", Channel: "bot", AccountID: "user-1", ThreadID: "thread-1"},
		},
		Input: "cancel me",
	})
	if err != nil {
		t.Fatalf("BeginRemoteTurn() error = %v", err)
	}
	defer result.Handle.Close()

	if err := host.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown() did not cancel active turn")
	}
	status := host.Status()
	if !status.ShuttingDown {
		t.Fatal("host status did not report shutting down")
	}
}

func boolPtr(v bool) *bool { return &v }
