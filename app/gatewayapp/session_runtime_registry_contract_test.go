package gatewayapp

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
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

func TestHostPrivateProjectionTypesDoNotRetainHostStack(t *testing.T) {
	t.Parallel()

	stackType := reflect.TypeFor[Stack]()
	for _, test := range []struct {
		name string
		typ  reflect.Type
	}{
		{name: "ACP surface", typ: reflect.TypeFor[gatewayACPSurface]()},
		{name: "Agent binding service", typ: reflect.TypeFor[AgentBindingService]()},
		{name: "Control Runtime service", typ: reflect.TypeFor[ControlRuntimeService]()},
		{name: "Agent message delivery", typ: reflect.TypeFor[AgentMessageDeliveryService]()},
		{name: "Workspace reads", typ: reflect.TypeFor[WorkspaceReadService]()},
		{name: "Task stream router", typ: reflect.TypeFor[hostTaskStreamService]()},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if fieldPath, ok := retainedConcreteStack(test.typ, stackType, nil); ok {
				t.Fatalf("%s retains gatewayapp.Stack through %s", test.name, fieldPath)
			}
		})
	}
}

func TestStackOwnsRuntimeCompositionAsNamedPrivateState(t *testing.T) {
	t.Parallel()

	stackType := reflect.TypeFor[Stack]()
	field, ok := stackType.FieldByName("composition")
	if !ok {
		t.Fatal("Stack has no named runtime composition field")
	}
	if field.Anonymous {
		t.Fatal("Stack anonymously embeds runtimeComposition")
	}
	if field.IsExported() {
		t.Fatal("Stack runtime composition field is exported")
	}
	if field.Type != reflect.TypeFor[runtimeComposition]() {
		t.Fatalf("Stack composition type = %v, want %v", field.Type, reflect.TypeFor[runtimeComposition]())
	}

	compositionType := reflect.TypeFor[runtimeComposition]()
	for i := range compositionType.NumField() {
		if compositionField := compositionType.Field(i); compositionField.IsExported() {
			t.Errorf("runtimeComposition field %s is exported", compositionField.Name)
		}
	}
}

func TestSessionRuntimeAssemblyDoesNotRetainRootComposition(t *testing.T) {
	t.Parallel()

	compositionType := reflect.TypeFor[runtimeComposition]()
	for _, test := range []struct {
		name string
		typ  reflect.Type
	}{
		{name: "Workspace assembler", typ: reflect.TypeFor[workspaceConfigAssembler]()},
		{name: "Assembly dependencies", typ: reflect.TypeFor[sessionRuntimeAssemblyDeps]()},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if fieldPath, ok := retainedConcreteStack(test.typ, compositionType, nil); ok {
				t.Fatalf("%s retains the Host root runtimeComposition through %s", test.name, fieldPath)
			}
		})
	}

	depsType := reflect.TypeFor[sessionRuntimeAssemblyDeps]()
	if _, ok := depsType.FieldByName("loadProcessSnapshot"); ok {
		t.Fatal("Session Runtime assembly dependencies retain a bound process snapshot loader")
	}
	field, ok := depsType.FieldByName("processConfig")
	if !ok || field.Type != reflect.TypeFor[*runtimeProcessConfigSource]() {
		t.Fatalf("process configuration source = %v, present:%v", field.Type, ok)
	}
}

func TestSessionRuntimeAssemblyUsesIndependentConfigurationSource(t *testing.T) {
	t.Parallel()

	storeDir := t.TempDir()
	rootStore := newAppConfigStore(storeDir)
	rootStore.savedHook = func() {}
	host := &Stack{composition: runtimeComposition{authorities: runtimeHostAuthorities{
		store:           rootStore,
		storeDir:        storeDir,
		configMigration: configstore.MigrationReport{FromSchema: 1, Migrated: true},
		hostedChildMailbox: func(context.Context, session.SessionRef, agentmessage.Request) (agentmessage.Response, error) {
			return agentmessage.Response{}, nil
		},
	}}}

	deps, err := newSessionRuntimeAssemblyDeps(host)
	if err != nil {
		t.Fatal(err)
	}
	if deps.authorities.store == nil || deps.authorities.store == rootStore {
		t.Fatal("Session Runtime assembly retained the Host configuration store")
	}
	if deps.authorities.store.path != rootStore.path {
		t.Fatalf("Session Runtime configuration path = %q, want %q", deps.authorities.store.path, rootStore.path)
	}
	if deps.authorities.store.saveHook != nil || deps.authorities.store.savedHook != nil {
		t.Fatal("Session Runtime configuration source retained Host write hooks")
	}
	if deps.authorities.configMigration.FromSchema != 1 || !deps.authorities.configMigration.Migrated {
		t.Fatalf("Session Runtime migration snapshot = %#v", deps.authorities.configMigration)
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
	return &sessionRuntimeInstance{runtimeComposition: runtimeComposition{sessions: sessions}}, nil
}

func (a *recordingSessionRuntimeAssembler) snapshot() (int, session.Service) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls, a.sessions
}
