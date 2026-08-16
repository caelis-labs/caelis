package gatewayapp

import (
	"context"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestSessionRuntimeRegistryUsesInjectedDependenciesWithoutHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	workspace := session.WorkspaceRef{Key: "workspace-a", CWD: t.TempDir()}
	sessions := inmemory.NewStore(inmemory.Config{})
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "test-user",
		Workspace:          workspace,
		PreferredSessionID: "session-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	assembler := &recordingSessionRuntimeAssembler{}
	registry, err := newSessionRuntimeRegistry(sessionRuntimeRegistryConfig{
		Sessions:         sessions,
		LifecycleContext: ctx,
		DefaultWorkspace: workspace,
		ModelRecovery:    newSessionModelRecovery(nil, nil, nil),
		Assembler:        assembler,
	})
	if err != nil {
		t.Fatal(err)
	}

	first, loaded, assembled, err := registry.activateSessionTracked(ctx, active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !assembled || loaded.SessionRef != active.SessionRef {
		t.Fatalf("first activation = assembled:%v session:%#v, want true and %#v", assembled, loaded, active.SessionRef)
	}
	second, loaded, assembled, err := registry.activateSessionTracked(ctx, active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if assembled || second != first || loaded.SessionRef != active.SessionRef {
		t.Fatalf("reused activation = runtime:%p assembled:%v session:%#v, want %p false and %#v", second, assembled, loaded, first, active.SessionRef)
	}
	if calls, received := assembler.snapshot(); calls != 1 || received != sessions {
		t.Fatalf("assembler calls = %d, sessions = %T; want 1 and injected store", calls, received)
	}
	dormant, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "test-user",
		Workspace:          workspace,
		PreferredSessionID: "session-b",
	})
	if err != nil {
		t.Fatal(err)
	}

	cancel()
	_, _, _, err = registry.activateSessionTracked(context.Background(), dormant.SessionID)
	if err == nil || !errorcode.Is(err, errorcode.Unavailable) {
		t.Fatalf("new activation after Host lifecycle cancellation = %v, want unavailable", err)
	}
}

type recordingSessionRuntimeAssembler struct {
	mu       sync.Mutex
	calls    int
	sessions session.Service
}

func (a *recordingSessionRuntimeAssembler) assembleSnapshot(
	_ context.Context,
	_ session.Session,
	_ sessionRuntimeActivity,
	sessions session.Service,
) (*Stack, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.sessions = sessions
	return &Stack{Sessions: sessions}, nil
}

func (a *recordingSessionRuntimeAssembler) snapshot() (int, session.Service) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls, a.sessions
}
