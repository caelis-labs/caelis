package gatewayapp

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestSessionRuntimeLifecycleTypesDoNotRetainHostStack(t *testing.T) {
	t.Parallel()

	stackType := reflect.TypeFor[Stack]()
	tests := []struct {
		name string
		typ  reflect.Type
	}{
		{name: "Registry", typ: reflect.TypeFor[sessionRuntimeRegistry]()},
		{name: "Runtime", typ: reflect.TypeFor[sessionRuntime]()},
		{name: "Runtime instance", typ: reflect.TypeFor[sessionRuntimeInstance]()},
		{name: "Workspace assembler", typ: reflect.TypeFor[workspaceConfigAssembler]()},
		{name: "Assembly dependencies", typ: reflect.TypeFor[sessionRuntimeAssemblyDeps]()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if fieldPath, ok := retainedConcreteStack(tt.typ, stackType, nil); ok {
				t.Fatalf("%s retains gatewayapp.Stack through %s", tt.name, fieldPath)
			}
		})
	}
}

func retainedConcreteStack(typ reflect.Type, stackType reflect.Type, seen map[reflect.Type]bool) (string, bool) {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Array || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Chan {
		typ = typ.Elem()
	}
	if typ == stackType {
		return typ.Name(), true
	}
	if typ.Kind() == reflect.Map {
		if path, ok := retainedConcreteStack(typ.Key(), stackType, seen); ok {
			return "map key." + path, true
		}
		return retainedConcreteStack(typ.Elem(), stackType, seen)
	}
	if typ.Kind() != reflect.Struct || typ.PkgPath() != stackType.PkgPath() {
		return "", false
	}
	if seen == nil {
		seen = map[reflect.Type]bool{}
	}
	if seen[typ] {
		return "", false
	}
	seen[typ] = true
	for i := range typ.NumField() {
		field := typ.Field(i)
		if path, ok := retainedConcreteStack(field.Type, stackType, seen); ok {
			return field.Name + "." + path, true
		}
	}
	return "", false
}

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
	drain, err := registry.beginQuiesce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !registry.isClosed() {
		t.Fatal("beginQuiesce did not close Runtime admission")
	}
	if err := drain.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := registry.closeRuntimeResources(); err != nil {
		t.Fatal(err)
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
) (*sessionRuntimeInstance, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.sessions = sessions
	return &sessionRuntimeInstance{runtimeComposition: runtimeComposition{Sessions: sessions}}, nil
}

func (a *recordingSessionRuntimeAssembler) snapshot() (int, session.Service) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls, a.sessions
}
