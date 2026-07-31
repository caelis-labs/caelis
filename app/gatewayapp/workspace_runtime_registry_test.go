package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/controlserver"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/client/httpclient"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

func TestControlHTTPClientSeparatesWorkspaceRuntimesAndRestoresBindings(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspaceA := newWorkspaceRuntimeTestDir(t, "workspace-a", "Workspace A rule.")
	workspaceB := newWorkspaceRuntimeTestDir(t, "workspace-b", "Workspace B rule.")
	config := Config{
		StoreDir:     storeDir,
		WorkspaceKey: "workspace-a",
		WorkspaceCWD: workspaceA,
		SkillDirs:    []string{},
		Sandbox:      SandboxConfig{RequestedType: "host"},
	}

	stack, err := NewLocalStack(config)
	if err != nil {
		t.Fatal(err)
	}
	remote := newWorkspaceRuntimeHTTPClient(t, stack, "local-user")

	sessionA1 := createWorkspaceRuntimeTestSession(t, remote, "create-a1", "session-a1", "workspace-a", workspaceA)
	sessionA2 := createWorkspaceRuntimeTestSession(t, remote, "create-a2", "session-a2", "workspace-a", workspaceA)
	sessionB := createWorkspaceRuntimeTestSession(t, remote, "create-b", "session-b", "workspace-b", workspaceB)

	runtimeA1, activeA1, err := stack.workspaceRuntimes.resolveSession(ctx, sessionA1)
	if err != nil {
		t.Fatal(err)
	}
	runtimeA2, _, err := stack.workspaceRuntimes.resolveSession(ctx, sessionA2)
	if err != nil {
		t.Fatal(err)
	}
	runtimeB, activeB, err := stack.workspaceRuntimes.resolveSession(ctx, sessionB)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeA1.workspace != runtimeA2.workspace {
		t.Fatal("Sessions in one workspace did not share one Workspace Runtime")
	}
	if runtimeA1.workspace == runtimeB.workspace {
		t.Fatal("Sessions from different workspaces shared one Workspace Runtime")
	}
	if runtimeA1.workspace.stack.currentGateway() == runtimeB.workspace.stack.currentGateway() {
		t.Fatal("different Workspace Runtimes shared one Gateway")
	}
	if runtimeA1.workspace.stack.engine == runtimeB.workspace.stack.engine {
		t.Fatal("different Workspace Runtimes shared one execution engine")
	}
	assertWorkspaceRuntimeComposition(
		t,
		runtimeA1.workspace.stack,
		workspaceA,
		"Workspace A rule.",
		"Workspace B rule.",
		"workspace-a-skill",
		"workspace-b-skill",
	)
	assertWorkspaceRuntimeComposition(
		t,
		runtimeB.workspace.stack,
		workspaceB,
		"Workspace B rule.",
		"Workspace A rule.",
		"workspace-b-skill",
		"workspace-a-skill",
	)
	if activeA1.CWD != workspaceA || activeB.CWD != workspaceB {
		t.Fatalf("durable Session CWDs = %q and %q, want %q and %q", activeA1.CWD, activeB.CWD, workspaceA, workspaceB)
	}

	routedRuntime := &controlClientLifecycleRuntime{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	routedGateway, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.Sessions,
		Runtime:  routedRuntime,
		Resolver: controlClientIngressResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeB.workspace.stack.mu.Lock()
	runtimeB.workspace.stack.gateway = routedGateway
	runtimeB.workspace.stack.mu.Unlock()
	prompt, err := remote.Prompt(ctx, controlclient.PromptRequest{
		WriteBase: controlclient.WriteBase{OperationID: "prompt-b", SessionID: sessionB},
		Input:     "run only in workspace B",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-routedRuntime.started:
	case <-time.After(2 * time.Second):
		t.Fatal("workspace B Runtime did not start")
	}
	if err := stack.rejectReconfigureWhileActive("test Host guard"); err == nil ||
		!strings.Contains(err.Error(), sessionB) {
		t.Fatalf("Host active-Turn guard error = %v, want workspace B Session", err)
	}
	stateB, err := remote.InspectSession(ctx, controlclient.StateRequest{SessionID: sessionB})
	if err != nil {
		t.Fatal(err)
	}
	stateA2, err := remote.InspectSession(ctx, controlclient.StateRequest{SessionID: sessionA2})
	if err != nil {
		t.Fatal(err)
	}
	if !stateB.Run.Active || stateA2.Run.Active {
		t.Fatalf("routed Run states = B:%#v A2:%#v", stateB.Run, stateA2.Run)
	}
	if _, err := remote.Cancel(ctx, controlclient.CancelRequest{
		WriteBase: controlclient.WriteBase{OperationID: "cancel-b", SessionID: sessionB},
		Target:    prompt.Target,
		Reason:    "test complete",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-routedRuntime.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("workspace B Runtime did not stop")
	}

	configBefore, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	sandboxBefore := stack.sandbox
	if _, err := stack.SetSandboxBackend(ctx, "auto"); err == nil ||
		!strings.Contains(err.Error(), "workspace Runtimes are loaded") {
		t.Fatalf("SetSandboxBackend() error = %v, want multi-Runtime rejection", err)
	}
	configAfter, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(configAfter, configBefore) || !reflect.DeepEqual(stack.sandbox, sandboxBefore) {
		t.Fatal("multi-Runtime rejection occurred after configuration mutation")
	}

	if _, err := remote.CloseSession(ctx, controlclient.CloseSessionRequest{
		WriteBase: controlclient.WriteBase{OperationID: "close-a1", SessionID: sessionA1},
	}); err != nil {
		t.Fatal(err)
	}
	if state, err := remote.InspectSession(ctx, controlclient.StateRequest{SessionID: sessionA1}); err != nil || state.CWD != workspaceA {
		t.Fatalf("inspect closed Session = %#v, %v", state, err)
	}
	if state, err := remote.InspectSession(ctx, controlclient.StateRequest{SessionID: sessionA2}); err != nil || state.CWD != workspaceA {
		t.Fatalf("inspect sibling Session = %#v, %v", state, err)
	}
	if state, err := remote.InspectSession(ctx, controlclient.StateRequest{SessionID: sessionB}); err != nil || state.CWD != workspaceB {
		t.Fatalf("inspect other-workspace Session = %#v, %v", state, err)
	}

	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewLocalStack(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	reloadedRemote := newWorkspaceRuntimeHTTPClient(t, reloaded, "local-user")
	state, err := reloadedRemote.InspectSession(ctx, controlclient.StateRequest{SessionID: sessionB})
	if err != nil {
		t.Fatal(err)
	}
	if state.WorkspaceKey != "workspace-b" || state.CWD != workspaceB {
		t.Fatalf("restored Session state = %#v", state)
	}
	restored, _, err := reloaded.workspaceRuntimes.resolveSession(ctx, sessionB)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceRuntimeComposition(
		t,
		restored.workspace.stack,
		workspaceB,
		"Workspace B rule.",
		"Workspace A rule.",
		"workspace-b-skill",
		"workspace-a-skill",
	)
}

func TestWorkspaceCompositionStackSharingContract(t *testing.T) {
	workspaceA := newWorkspaceRuntimeTestDir(t, "workspace-a", "Workspace A rule.")
	workspaceB := newWorkspaceRuntimeTestDir(t, "workspace-b", "Workspace B rule.")
	stack, err := NewLocalStack(Config{
		StoreDir:     t.TempDir(),
		WorkspaceKey: "workspace-a",
		WorkspaceCWD: workspaceA,
		SkillDirs:    []string{},
		Sandbox:      SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })

	runtimeB, err := stack.workspaceRuntimes.resolveWorkspace(context.Background(), session.WorkspaceRef{
		Key: "workspace-b",
		CWD: workspaceB,
	})
	if err != nil {
		t.Fatal(err)
	}
	child := runtimeB.stack
	if child == nil || child == stack {
		t.Fatalf("workspace child Stack = %p, host = %p", child, stack)
	}
	if child.lookup != stack.lookup ||
		child.store != stack.store ||
		!sameWorkspaceRuntimeReference(child.Sessions, stack.Sessions) ||
		!sameWorkspaceRuntimeReference(child.taskStore, stack.taskStore) ||
		!sameWorkspaceRuntimeReference(child.controlFeeds, stack.controlFeeds) ||
		!sameWorkspaceRuntimeReference(child.lifecycleCtx, stack.lifecycleCtx) ||
		child.approvalRecovery != stack.approvalRecovery ||
		child.codexAuth != stack.codexAuth ||
		child.grokAuth != stack.grokAuth ||
		child.apiKeyCredentials != stack.apiKeyCredentials ||
		child.providerUsage != stack.providerUsage ||
		child.reconfigureLock() != stack.reconfigureLock() ||
		child.assemblyMutationLock() != stack.assemblyMutationLock() {
		t.Fatal("workspace child did not receive the required Host-shared state")
	}
	if child.currentGateway() == stack.currentGateway() ||
		child.engine == stack.engine ||
		child.exec == stack.exec {
		t.Fatal("workspace child shared workspace-scoped composition state")
	}
	if child.mcpMgr != nil && child.mcpMgr == stack.mcpMgr {
		t.Fatal("workspace child shared its MCP manager")
	}
	if child.workspaceRuntimes != nil ||
		child.controlState != nil ||
		child.controlCommands != nil ||
		child.controlClient != nil ||
		child.taskStreams != nil ||
		child.operations != nil ||
		child.lifecycleCancel != nil ||
		child.sandboxLifecycleFactory != nil ||
		child.refreshConfiguredAgentsHook != nil {
		t.Fatal("workspace child received Host-only ownership state")
	}
}

func sameWorkspaceRuntimeReference(left any, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftValue := reflect.ValueOf(left)
	rightValue := reflect.ValueOf(right)
	if leftValue.Type() != rightValue.Type() {
		return false
	}
	switch leftValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return leftValue.Pointer() == rightValue.Pointer()
	default:
		return reflect.DeepEqual(left, right)
	}
}

func TestWorkspaceRuntimeIdentityDoesNotPartitionByUser(t *testing.T) {
	ctx := context.Background()
	workspace := newWorkspaceRuntimeTestDir(t, "workspace", "Shared workspace rule.")
	stack, err := NewLocalStack(Config{
		StoreDir:     t.TempDir(),
		WorkspaceKey: "workspace",
		WorkspaceCWD: workspace,
		SkillDirs:    []string{},
		Sandbox:      SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })

	first, err := controlclient.BindSessionClient(stack.ControlClient(), controlclient.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := controlclient.BindSessionClient(stack.ControlClient(), controlclient.Principal{ID: "compatibility-principal"})
	if err != nil {
		t.Fatal(err)
	}
	firstID := createWorkspaceRuntimeTestSession(t, first, "create-first", "session-first", "workspace", workspace)
	secondID := createWorkspaceRuntimeTestSession(t, second, "create-second", "session-second", "workspace", workspace)

	firstRuntime, firstSession, err := stack.workspaceRuntimes.resolveSession(ctx, firstID)
	if err != nil {
		t.Fatal(err)
	}
	secondRuntime, secondSession, err := stack.workspaceRuntimes.resolveSession(ctx, secondID)
	if err != nil {
		t.Fatal(err)
	}
	if firstSession.UserID == secondSession.UserID {
		t.Fatalf("compatibility UserIDs unexpectedly equal: %q", firstSession.UserID)
	}
	if firstRuntime.workspace != secondRuntime.workspace {
		t.Fatal("UserID incorrectly partitioned one workspace into multiple Runtimes")
	}
}

func TestControlClientRejectsForeignPreferredSessionBeforeWorkspaceDisclosure(t *testing.T) {
	ctx := context.Background()
	workspaceA := newWorkspaceRuntimeTestDir(t, "workspace-a", "Workspace A rule.")
	workspaceB := newWorkspaceRuntimeTestDir(t, "workspace-b", "Workspace B rule.")
	stack, err := NewLocalStack(Config{
		StoreDir:     t.TempDir(),
		WorkspaceKey: "workspace-a",
		WorkspaceCWD: workspaceA,
		SkillDirs:    []string{},
		Sandbox:      SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })

	owner, err := controlclient.BindSessionClient(stack.ControlClient(), controlclient.Principal{ID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := controlclient.BindSessionClient(stack.ControlClient(), controlclient.Principal{ID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := createWorkspaceRuntimeTestSession(
		t,
		owner,
		"create-owner",
		"private-session",
		"workspace-a",
		workspaceA,
	)

	result, err := other.CreateSession(ctx, controlclient.CreateSessionRequest{
		WriteBase:          controlclient.WriteBase{OperationID: "probe-foreign-session"},
		PreferredSessionID: sessionID,
		WorkspaceKey:       "workspace-b",
		CWD:                workspaceB,
	})
	if err == nil ||
		result.Outcome != controlclient.OutcomeRejected ||
		errorcode.CodeOf(err) != errorcode.PermissionDenied {
		t.Fatalf("foreign preferred CreateSession() = %#v, %v; want rejected permission_denied", result, err)
	}
	if strings.Contains(result.Detail, workspaceA) ||
		strings.Contains(result.Detail, workspaceB) ||
		strings.Contains(err.Error(), workspaceA) ||
		strings.Contains(err.Error(), workspaceB) {
		t.Fatalf("foreign preferred Session error disclosed a workspace path: %#v, %v", result, err)
	}
	active, err := stack.Sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if active.UserID != "owner" || active.WorkspaceKey != "workspace-a" || active.CWD != workspaceA {
		t.Fatalf("foreign preferred Session changed durable identity: %#v", active)
	}
	stack.workspaceRuntimes.mu.RLock()
	workspaceCount := len(stack.workspaceRuntimes.workspaces)
	stack.workspaceRuntimes.mu.RUnlock()
	if workspaceCount != 1 {
		t.Fatalf("foreign preferred Session loaded %d workspaces, want only the default Runtime", workspaceCount)
	}
}

func TestControlClientRecordsPreDispatchRoutingFailureAsRejected(t *testing.T) {
	ctx := context.Background()
	workspace := newWorkspaceRuntimeTestDir(t, "workspace", "Workspace rule.")
	stack, err := NewLocalStack(Config{
		StoreDir:     t.TempDir(),
		WorkspaceKey: "workspace",
		WorkspaceCWD: workspace,
		SkillDirs:    []string{},
		Sandbox:      SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client, err := controlclient.BindSessionClient(stack.ControlClient(), controlclient.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := createWorkspaceRuntimeTestSession(
		t,
		client,
		"create-routing-failure",
		"routing-failure",
		"workspace",
		workspace,
	)

	originalSessions := stack.Sessions
	stack.Sessions = sessionLookupErrorService{
		Service: originalSessions,
		err:     errors.New("synthetic routing read failure"),
	}
	request := controlclient.CloseSessionRequest{
		WriteBase: controlclient.WriteBase{
			OperationID: "close-routing-failure",
			SessionID:   sessionID,
		},
	}
	result, err := client.CloseSession(ctx, request)
	stack.Sessions = originalSessions
	if err == nil ||
		result.Outcome != controlclient.OutcomeRejected ||
		errorcode.CodeOf(err) != errorcode.Internal {
		t.Fatalf("pre-dispatch routing failure = %#v, %v; want rejected internal", result, err)
	}
	closed, err := controlclient.IsSessionClosed(ctx, originalSessions, session.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if closed {
		t.Fatal("pre-dispatch routing failure closed the Session")
	}

	replayed, err := client.CloseSession(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Outcome != controlclient.OutcomeRejected {
		t.Fatalf("persisted routing failure = %#v, want rejected", replayed)
	}
}

func TestWorkspaceRuntimeRegistryCreatesSessionsConcurrently(t *testing.T) {
	workspaceA := newWorkspaceRuntimeTestDir(t, "workspace-a", "Workspace A rule.")
	workspaceB := newWorkspaceRuntimeTestDir(t, "workspace-b", "Workspace B rule.")
	stack, err := NewLocalStack(Config{
		StoreDir:     t.TempDir(),
		WorkspaceKey: "workspace-a",
		WorkspaceCWD: workspaceA,
		SkillDirs:    []string{},
		Sandbox:      SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client, err := controlclient.BindSessionClient(stack.ControlClient(), controlclient.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}

	const count = 12
	errs := make(chan error, count)
	var wait sync.WaitGroup
	for index := range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			key, cwd := "workspace-a", workspaceA
			if index%2 != 0 {
				key, cwd = "workspace-b", workspaceB
			}
			sessionID := fmt.Sprintf("concurrent-%02d", index)
			result, createErr := client.CreateSession(context.Background(), controlclient.CreateSessionRequest{
				WriteBase:          controlclient.WriteBase{OperationID: "create-" + sessionID},
				PreferredSessionID: sessionID,
				WorkspaceKey:       key,
				CWD:                cwd,
			})
			if createErr != nil {
				errs <- createErr
				return
			}
			if result.Outcome != controlclient.OutcomeCommitted || result.SessionID != sessionID {
				errs <- fmt.Errorf("CreateSession(%q) = %#v", sessionID, result)
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	stack.workspaceRuntimes.mu.RLock()
	workspaceCount := len(stack.workspaceRuntimes.workspaces)
	stack.workspaceRuntimes.mu.RUnlock()
	if workspaceCount != 2 {
		t.Fatalf("Runtime registry workspace count = %d, want 2", workspaceCount)
	}
}

func TestWorkspaceRuntimeRegistryQuiesceWaitsForInFlightLoad(t *testing.T) {
	workspaceA := newWorkspaceRuntimeTestDir(t, "workspace-a", "Workspace A rule.")
	workspaceB := newWorkspaceRuntimeTestDir(t, "workspace-b", "Workspace B rule.")
	stack, err := NewLocalStack(Config{
		StoreDir:     t.TempDir(),
		WorkspaceKey: "workspace-a",
		WorkspaceCWD: workspaceA,
		SkillDirs:    []string{},
		Sandbox:      SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })

	gate := stack.reconfigureLock()
	gate.Lock()
	gateLocked := true
	defer func() {
		if gateLocked {
			gate.Unlock()
		}
	}()

	resolveDone := make(chan error, 1)
	go func() {
		_, resolveErr := stack.workspaceRuntimes.resolveWorkspace(context.Background(), session.WorkspaceRef{
			Key: "workspace-b",
			CWD: workspaceB,
		})
		resolveDone <- resolveErr
	}()
	waitForWorkspaceRuntimeRegistryState(t, stack.workspaceRuntimes, false, 1)

	quiesceDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		quiesceDone <- stack.Quiesce(ctx)
	}()
	waitForWorkspaceRuntimeRegistryState(t, stack.workspaceRuntimes, true, 1)
	select {
	case err := <-quiesceDone:
		t.Fatalf("Quiesce returned before the in-flight workspace load drained: %v", err)
	default:
	}

	gate.Unlock()
	gateLocked = false
	if err := <-resolveDone; errorcode.CodeOf(err) != errorcode.Unavailable {
		t.Fatalf("workspace resolution during Quiesce error = %v, want unavailable", err)
	}
	if err := <-quiesceDone; err != nil {
		t.Fatalf("Quiesce() error = %v", err)
	}
	stack.workspaceRuntimes.mu.RLock()
	_, loaded := stack.workspaceRuntimes.workspaces["workspace-b"]
	stack.workspaceRuntimes.mu.RUnlock()
	if loaded {
		t.Fatal("workspace Runtime completed registration after Quiesce")
	}
}

func TestControlClientRejectsAmbiguousWorkspaceBindingBeforeSessionCreation(t *testing.T) {
	ctx := context.Background()
	workspaceA := newWorkspaceRuntimeTestDir(t, "workspace-a", "Workspace A rule.")
	workspaceB := newWorkspaceRuntimeTestDir(t, "workspace-b", "Workspace B rule.")
	stack, err := NewLocalStack(Config{
		StoreDir:     t.TempDir(),
		WorkspaceKey: "shared-key",
		WorkspaceCWD: workspaceA,
		SkillDirs:    []string{},
		Sandbox:      SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client, err := controlclient.BindSessionClient(stack.ControlClient(), controlclient.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}

	result, err := client.CreateSession(ctx, controlclient.CreateSessionRequest{
		WriteBase:          controlclient.WriteBase{OperationID: "ambiguous-workspace"},
		PreferredSessionID: "must-not-exist",
		WorkspaceKey:       "shared-key",
		CWD:                workspaceB,
	})
	if err == nil {
		t.Fatalf("CreateSession() = %#v, nil; want ambiguous workspace rejection", result)
	}
	if result.Outcome != controlclient.OutcomeRejected || errorcode.CodeOf(err) != errorcode.InvalidArgument {
		t.Fatalf("CreateSession() = %#v, %v; want rejected invalid_argument", result, err)
	}
	if _, loadErr := stack.Sessions.Session(ctx, session.SessionRef{SessionID: "must-not-exist"}); !errors.Is(loadErr, session.ErrSessionNotFound) {
		t.Fatalf("rejected Session lookup error = %v, want ErrSessionNotFound", loadErr)
	}
}

func newWorkspaceRuntimeTestDir(t *testing.T, name, instruction string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	skillName := name + "-skill"
	skillDir := filepath.Join(root, ".agents", "skills", skillName)
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Workspace\n\n"+instruction+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillContents := "---\nname: " + skillName + "\ndescription: " + instruction + "\n---\n# " + skillName + "\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContents), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func newWorkspaceRuntimeHTTPClient(
	t *testing.T,
	stack *Stack,
	principalID string,
) controlclient.SessionClient {
	t.Helper()
	const token = "0123456789abcdef0123456789abcdef"
	authenticator, err := controlserver.BearerTokenAuthenticator(
		token,
		controlclient.Principal{ID: principalID},
	)
	if err != nil {
		t.Fatal(err)
	}
	server, err := controlserver.New(controlserver.HandlerConfig{
		Service:       stack.ControlClient(),
		Authenticator: authenticator,
		AllowedHosts:  []string{"127.0.0.1", "localhost", "::1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := httpclient.New(httpclient.Config{
		BaseURL:     "http://127.0.0.1",
		BearerToken: token,
		HTTPClient: &http.Client{
			Transport: workspaceRuntimeHandlerTransport{handler: server.Handler()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return remote
}

type workspaceRuntimeHandlerTransport struct {
	handler http.Handler
}

func (transport workspaceRuntimeHandlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	transport.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

type sessionLookupErrorService struct {
	session.Service
	err error
}

func (s sessionLookupErrorService) Session(
	context.Context,
	session.SessionRef,
) (session.Session, error) {
	return session.Session{}, s.err
}

func waitForWorkspaceRuntimeRegistryState(
	t *testing.T,
	registry *workspaceRuntimeRegistry,
	closed bool,
	building int,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		registry.mu.RLock()
		actualClosed := registry.closed
		actualBuilding := registry.building
		registry.mu.RUnlock()
		if actualClosed == closed && actualBuilding == building {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"workspace Runtime registry state = closed:%t building:%d, want closed:%t building:%d",
				actualClosed,
				actualBuilding,
				closed,
				building,
			)
		}
		time.Sleep(time.Millisecond)
	}
}

func createWorkspaceRuntimeTestSession(
	t *testing.T,
	client controlclient.SessionClient,
	operationID string,
	sessionID string,
	workspaceKey string,
	cwd string,
) string {
	t.Helper()
	result, err := client.CreateSession(context.Background(), controlclient.CreateSessionRequest{
		WriteBase:          controlclient.WriteBase{OperationID: operationID},
		PreferredSessionID: sessionID,
		WorkspaceKey:       workspaceKey,
		CWD:                cwd,
		Title:              sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != controlclient.OutcomeCommitted || result.SessionID != sessionID {
		t.Fatalf("CreateSession(%q) = %#v", sessionID, result)
	}
	return result.SessionID
}

func assertWorkspaceRuntimeComposition(
	t *testing.T,
	stack *Stack,
	wantCWD string,
	wantInstruction string,
	forbiddenInstruction string,
	wantSkill string,
	forbiddenSkill string,
) {
	t.Helper()
	if stack.Workspace.CWD != wantCWD {
		t.Fatalf("Workspace Runtime CWD = %q, want %q", stack.Workspace.CWD, wantCWD)
	}
	actualCWD, err := stack.exec.FileSystem().Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if actualCWD != wantCWD {
		t.Fatalf("workspace sandbox CWD = %q, want %q", actualCWD, wantCWD)
	}
	prompt := stringFromMap(stack.runtime.BaseMetadata, "system_prompt")
	if !strings.Contains(prompt, wantInstruction) {
		t.Fatalf("workspace prompt does not contain %q:\n%s", wantInstruction, prompt)
	}
	if strings.Contains(prompt, forbiddenInstruction) {
		t.Fatalf("workspace prompt contains foreign instruction %q:\n%s", forbiddenInstruction, prompt)
	}
	skills := make(map[string]struct{})
	for _, meta := range stack.runtime.SkillCatalog.Metas() {
		skills[meta.Name] = struct{}{}
	}
	if _, ok := skills[wantSkill]; !ok {
		t.Fatalf("workspace skill catalog does not contain %q: %#v", wantSkill, skills)
	}
	if _, ok := skills[forbiddenSkill]; ok {
		t.Fatalf("workspace skill catalog contains foreign skill %q: %#v", forbiddenSkill, skills)
	}
}
