package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/controlserver"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/client/httpclient"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/internal/testenv"
)

func TestSessionRuntimePinsWorkspaceConfigUntilRelease(t *testing.T) {
	ctx := context.Background()
	workspaceA := newWorkspaceRuntimeTestDir(t, "workspace-a", "Workspace A rule v1.")
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
	remote := newWorkspaceRuntimeHTTPClient(t, stack, "local-user")

	sessionA1 := createWorkspaceRuntimeTestSession(t, remote, "create-a1", "session-a1", "workspace-a", workspaceA)
	sessionA2 := createWorkspaceRuntimeTestSession(t, remote, "create-a2", "session-a2", "workspace-a", workspaceA)
	sessionB := createWorkspaceRuntimeTestSession(t, remote, "create-b", "session-b", "workspace-b", workspaceB)

	runtimeA1 := activateSessionRuntime(t, stack, sessionA1)
	runtimeA2 := activateSessionRuntime(t, stack, sessionA2)
	runtimeB := activateSessionRuntime(t, stack, sessionB)
	if runtimeA1 == runtimeA2 || runtimeA1.stack == runtimeA2.stack {
		t.Fatal("two Sessions in one workspace shared a live Runtime")
	}
	if runtimeA1.stack.currentGateway() == runtimeA2.stack.currentGateway() {
		t.Fatal("two Sessions in one workspace shared a Gateway")
	}
	if runtimeA1.workspace != runtimeA2.workspace {
		t.Fatalf("same-workspace Session refs = %#v and %#v", runtimeA1.workspace, runtimeA2.workspace)
	}
	if runtimeA1.workspace == runtimeB.workspace {
		t.Fatal("different workspaces resolved to one workspace identity")
	}
	assertWorkspaceRuntimeComposition(
		t,
		runtimeA1.stack,
		workspaceA,
		"Workspace A rule v1.",
		"Workspace B rule.",
		"workspace-a-skill",
		"workspace-b-skill",
	)
	assertWorkspaceRuntimeComposition(
		t,
		runtimeB.stack,
		workspaceB,
		"Workspace B rule.",
		"Workspace A rule v1.",
		"workspace-b-skill",
		"workspace-a-skill",
	)

	writeWorkspaceRuntimeInstruction(t, workspaceA, "Workspace A rule v2.")
	pinned, active, err := stack.sessionRuntimes.activateSession(ctx, sessionA1)
	if err != nil {
		t.Fatal(err)
	}
	if pinned != runtimeA1 || active.CWD != workspaceA {
		t.Fatalf("active Session Runtime changed before release: runtime=%p active=%#v", pinned, active)
	}
	assertWorkspaceRuntimeComposition(
		t,
		pinned.stack,
		workspaceA,
		"Workspace A rule v1.",
		"Workspace A rule v2.",
		"workspace-a-skill",
		"workspace-b-skill",
	)

	sessionA3 := createWorkspaceRuntimeTestSession(t, remote, "create-a3", "session-a3", "workspace-a", workspaceA)
	runtimeA3 := activateSessionRuntime(t, stack, sessionA3)
	assertWorkspaceRuntimeComposition(
		t,
		runtimeA3.stack,
		workspaceA,
		"Workspace A rule v2.",
		"Workspace A rule v1.",
		"workspace-a-skill",
		"workspace-b-skill",
	)
	assertWorkspaceRuntimeComposition(
		t,
		runtimeA2.stack,
		workspaceA,
		"Workspace A rule v1.",
		"Workspace A rule v2.",
		"workspace-a-skill",
		"workspace-b-skill",
	)

	if err := stack.sessionRuntimes.release(ctx, sessionA1); err != nil {
		t.Fatal(err)
	}
	if _, ok := stack.sessionRuntimes.loaded(sessionA1); ok {
		t.Fatal("released Session retained a live Runtime")
	}
	refreshed, _, err := stack.sessionRuntimes.activateSession(ctx, sessionA1)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed == runtimeA1 || refreshed.stack == runtimeA1.stack {
		t.Fatal("reactivated Session reused its released Runtime")
	}
	assertWorkspaceRuntimeComposition(
		t,
		refreshed.stack,
		workspaceA,
		"Workspace A rule v2.",
		"Workspace A rule v1.",
		"workspace-a-skill",
		"workspace-b-skill",
	)

	if _, err := remote.CloseSession(ctx, controlclient.CloseSessionRequest{
		WriteBase: controlclient.WriteBase{OperationID: "close-b", SessionID: sessionB},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := stack.sessionRuntimes.loaded(sessionB); ok {
		t.Fatal("closed Session retained a live Runtime")
	}
	if state, err := remote.InspectSession(ctx, controlclient.StateRequest{SessionID: sessionB}); err != nil || state.CWD != workspaceB {
		t.Fatalf("inspect closed Session = %#v, %v", state, err)
	}
	if _, ok := stack.sessionRuntimes.loaded(sessionB); ok {
		t.Fatal("inspect reactivated a closed Session Runtime")
	}
}

func TestDefaultRuntimeSessionRemainsControlRoutedDuringTUIMigration(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	allowStop := make(chan struct{})
	var stopOnce sync.Once
	t.Cleanup(func() {
		stopOnce.Do(func() { close(allowStop) })
		_ = stack.Close()
	})

	defaultGateway, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.Sessions,
		Runtime:  &blockingRuntime{session: active, release: allowStop},
		Resolver: blockingResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	stack.mu.Lock()
	stack.gateway = defaultGateway
	stack.mu.Unlock()

	started, err := defaultGateway.BeginTurn(ctx, kernelimpl.BeginTurnRequest{
		SessionRef: active.SessionRef,
		Input:      "default Runtime turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stack.sessionRuntimes.defaultSession(active.SessionID) {
		t.Fatal("TUI Session lost its transitional default Runtime ownership")
	}
	if _, loaded := stack.sessionRuntimes.loaded(active.SessionID); loaded {
		t.Fatal("TUI Session incorrectly allocated a detached Runtime")
	}

	state, err := stack.ControlClientRuntimeState(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Run.Active || state.Run.HandleID != started.Handle.HandleID() {
		t.Fatalf("ControlClientRuntimeState() = %#v, want active default Runtime turn", state)
	}

	promptResult, err := stack.ExecuteControlCommand(
		ctx,
		controlclient.Principal{ID: active.UserID},
		controlclient.ActionPrompt,
		controlclient.PromptRequest{
			WriteBase: controlclient.WriteBase{
				OperationID: "default-runtime-overlap",
				SessionID:   active.SessionID,
			},
			Input: "must remain on the default Runtime",
		},
	)
	if err == nil || errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("Prompt on active default Runtime = %#v, %v; want active-turn conflict", promptResult, err)
	}
	if _, loaded := stack.sessionRuntimes.loaded(active.SessionID); loaded {
		t.Fatal("Prompt on a default Runtime Session allocated a detached Runtime")
	}

	cancelResult, err := stack.ExecuteControlCommand(
		ctx,
		controlclient.Principal{ID: active.UserID},
		controlclient.ActionCancel,
		controlclient.CancelRequest{
			WriteBase: controlclient.WriteBase{
				OperationID: "cancel-default-runtime",
				SessionID:   active.SessionID,
			},
			Target: controlclient.TurnTarget{
				HandleID: started.Handle.HandleID(),
				RunID:    started.Handle.RunID(),
				TurnID:   started.Handle.TurnID(),
			},
			Reason: "test complete",
		},
	)
	if err != nil || cancelResult.Outcome != controlclient.OutcomeCommitted {
		t.Fatalf("Cancel default Runtime turn = %#v, %v", cancelResult, err)
	}
	stopOnce.Do(func() { close(allowStop) })
	closeResult, err := stack.ExecuteControlCommand(
		ctx,
		controlclient.Principal{ID: active.UserID},
		controlclient.ActionSessionClose,
		controlclient.CloseSessionRequest{WriteBase: controlclient.WriteBase{
			OperationID: "close-default-runtime",
			SessionID:   active.SessionID,
		}},
	)
	if err != nil || closeResult.Outcome != controlclient.OutcomeCommitted {
		t.Fatalf("Close default Runtime Session = %#v, %v", closeResult, err)
	}
	if stack.sessionRuntimes.defaultSession(active.SessionID) {
		t.Fatal("closed TUI Session retained default Runtime ownership")
	}
}

func TestSessionRuntimeReleaseWaitsForRoutedControlMutation(t *testing.T) {
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
	client, err := controlclient.BindSessionClient(
		stack.ControlClient(),
		controlclient.Principal{ID: "local-user"},
	)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := createWorkspaceRuntimeTestSession(
		t,
		client,
		"create-control-mutation-release",
		"control-mutation-release",
		"workspace",
		workspace,
	)
	runtime := activateSessionRuntime(t, stack, sessionID)
	active, err := stack.Sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	control := &blockingSessionControl{
		active:  active,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	gateway, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.Sessions,
		Runtime:  controlClientBlockingRuntime{},
		Resolver: blockingResolver{},
		Control:  control,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.stack.mu.Lock()
	runtime.stack.gateway = gateway
	runtime.stack.mu.Unlock()

	commandDone := make(chan error, 1)
	go func() {
		_, commandErr := stack.ExecuteControlCommand(
			ctx,
			controlclient.Principal{ID: "local-user"},
			controlclient.ActionControllerHandoff,
			controlclient.HandoffRequest{
				WriteBase: controlclient.WriteBase{
					OperationID: "blocking-handoff",
					SessionID:   sessionID,
				},
				Kind:   session.ControllerKindKernel,
				Source: "test",
			},
		)
		commandDone <- commandErr
	}()
	<-control.entered

	releaseDone := make(chan error, 1)
	go func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		releaseDone <- stack.sessionRuntimes.release(releaseCtx, sessionID)
	}()
	waitForSessionRuntimeReleasing(t, stack.sessionRuntimes, sessionID)
	select {
	case err := <-releaseDone:
		t.Fatalf("release returned before routed Control mutation completed: %v", err)
	default:
	}

	close(control.release)
	if err := <-commandDone; err != nil {
		t.Fatalf("Handoff command error = %v", err)
	}
	if err := <-releaseDone; err != nil {
		t.Fatalf("release error = %v", err)
	}
}

func TestHostCloseRetriesFailedSessionRuntimeResourceRelease(t *testing.T) {
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
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = stack.Close()
		}
	})
	client, err := controlclient.BindSessionClient(
		stack.ControlClient(),
		controlclient.Principal{ID: "local-user"},
	)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := createWorkspaceRuntimeTestSession(
		t,
		client,
		"create-release-retry",
		"release-retry",
		"workspace",
		workspace,
	)
	runtime := activateSessionRuntime(t, stack, sessionID)

	closeErr := errors.New("Session Runtime close failed")
	retryable := newSandboxLifecycleTestRuntime(sandbox.BackendHost, sandbox.BackendHost)
	retryable.closeErr = closeErr
	runtime.stack.workspaceCloseMu.Lock()
	runtime.stack.mu.Lock()
	original := runtime.stack.exec
	runtime.stack.exec = retryable
	runtime.stack.mu.Unlock()
	runtime.stack.workspaceCloseMu.Unlock()
	if original != nil {
		if err := original.Close(); err != nil {
			t.Fatal(err)
		}
	}

	if err := stack.sessionRuntimes.release(ctx, sessionID); !errors.Is(err, closeErr) {
		t.Fatalf("release() error = %v, want %v", err, closeErr)
	}
	if _, loaded := stack.sessionRuntimes.loaded(sessionID); loaded {
		t.Fatal("failed release remained routable")
	}
	retryable.closeErr = nil
	if err := stack.Close(); err != nil {
		t.Fatalf("Host Close() retry error = %v", err)
	}
	closed = true
	if retryable.closeCalls != 2 {
		t.Fatalf("Session Runtime Close() calls = %d, want 2", retryable.closeCalls)
	}
}

func TestSessionRuntimeConfigIsDetachedFromHostMutation(t *testing.T) {
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
	firstID := createWorkspaceRuntimeTestSession(t, client, "create-first", "session-first", "workspace", workspace)
	first := activateSessionRuntime(t, stack, firstID)
	assertSessionRuntimeSharingContract(t, stack, first.stack)

	blockingRuntime := &controlClientLifecycleRuntime{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	blockingGateway, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.Sessions,
		Runtime:  blockingRuntime,
		Resolver: controlClientIngressResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	first.stack.mu.Lock()
	first.stack.gateway = blockingGateway
	first.stack.mu.Unlock()
	prompt, err := client.Prompt(ctx, controlclient.PromptRequest{
		WriteBase: controlclient.WriteBase{OperationID: "prompt-first", SessionID: firstID},
		Input:     "hold the Session Runtime active",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-blockingRuntime.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Session Runtime did not start")
	}

	status, err := stack.SetSandboxBackend(ctx, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if stack.sandbox.RequestedType != "auto" {
		t.Fatalf("host sandbox config = %q, want auto (status %#v)", stack.sandbox.RequestedType, status)
	}
	if first.stack.sandbox.RequestedType != "host" {
		t.Fatalf("live Session sandbox changed to %q, want pinned host", first.stack.sandbox.RequestedType)
	}
	if _, err := client.Cancel(ctx, controlclient.CancelRequest{
		WriteBase: controlclient.WriteBase{OperationID: "cancel-first", SessionID: firstID},
		Target:    prompt.Target,
		Reason:    "test complete",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blockingRuntime.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Session Runtime did not stop")
	}

	secondID := createWorkspaceRuntimeTestSession(t, client, "create-second", "session-second", "workspace", workspace)
	second := activateSessionRuntime(t, stack, secondID)
	if second.stack.sandbox.RequestedType != "auto" {
		t.Fatalf("new Session sandbox = %q, want current auto config", second.stack.sandbox.RequestedType)
	}
	if err := stack.sessionRuntimes.release(ctx, firstID); err != nil {
		t.Fatal(err)
	}
	refreshed, _, err := stack.sessionRuntimes.activateSession(ctx, firstID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.stack.sandbox.RequestedType != "auto" {
		t.Fatalf("reactivated Session sandbox = %q, want current auto config", refreshed.stack.sandbox.RequestedType)
	}
}

func TestSessionRuntimeReleaseTombstoneRejectsConcurrentPromptAndDrainsOnHostClose(t *testing.T) {
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
	sessionID := createWorkspaceRuntimeTestSession(t, client, "create-release-race", "release-race", "workspace", workspace)
	runtime := activateSessionRuntime(t, stack, sessionID)
	active, err := stack.Sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	allowStop := make(chan struct{})
	blockingGateway, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.Sessions,
		Runtime:  &blockingRuntime{session: active, release: allowStop},
		Resolver: blockingResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.stack.mu.Lock()
	runtime.stack.gateway = blockingGateway
	runtime.stack.mu.Unlock()
	if _, err := client.Prompt(ctx, controlclient.PromptRequest{
		WriteBase: controlclient.WriteBase{OperationID: "prompt-release-race", SessionID: sessionID},
		Input:     "hold release open",
	}); err != nil {
		t.Fatal(err)
	}

	releaseDone := make(chan error, 1)
	go func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		releaseDone <- stack.sessionRuntimes.release(releaseCtx, sessionID)
	}()
	waitForSessionRuntimeReleasing(t, stack.sessionRuntimes, sessionID)

	result, err := client.Prompt(ctx, controlclient.PromptRequest{
		WriteBase: controlclient.WriteBase{OperationID: "prompt-during-release", SessionID: sessionID},
		Input:     "must not reach the releasing Runtime",
	})
	if err == nil || errorcode.CodeOf(err) != errorcode.Unavailable {
		t.Fatalf("Prompt during release = %#v, %v; want unavailable", result, err)
	}
	if loaded, ok := stack.sessionRuntimes.loaded(sessionID); ok || loaded != nil {
		t.Fatalf("loaded releasing Runtime = %p, %t; want hidden tombstone", loaded, ok)
	}

	hostQuiesceDone := make(chan error, 1)
	go func() {
		quiesceCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		hostQuiesceDone <- stack.Quiesce(quiesceCtx)
	}()
	waitForSessionRuntimeRegistryState(t, stack.sessionRuntimes, true, 0)
	select {
	case err := <-hostQuiesceDone:
		t.Fatalf("Host Quiesce returned before Runtime release drained: %v", err)
	default:
	}

	close(allowStop)
	if err := <-releaseDone; err != nil {
		t.Fatalf("release() error = %v", err)
	}
	if err := <-hostQuiesceDone; err != nil {
		t.Fatalf("Host Quiesce() error = %v", err)
	}
	stack.sessionRuntimes.mu.RLock()
	_, retained := stack.sessionRuntimes.sessions[sessionID]
	stack.sessionRuntimes.mu.RUnlock()
	if retained {
		t.Fatal("successful Runtime release retained its tombstone")
	}
}

func TestSessionRuntimeReassemblesCurrentConfigAfterHostRestart(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := newWorkspaceRuntimeTestDir(t, "workspace", "Workspace rule v1.")
	config := Config{
		StoreDir:     storeDir,
		WorkspaceKey: "workspace",
		WorkspaceCWD: workspace,
		SkillDirs:    []string{},
		Sandbox:      SandboxConfig{RequestedType: "host"},
	}
	first, err := NewLocalStack(config)
	if err != nil {
		t.Fatal(err)
	}
	client, err := controlclient.BindSessionClient(first.ControlClient(), controlclient.Principal{ID: "local-user"})
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	sessionID := createWorkspaceRuntimeTestSession(t, client, "create-restart", "restart", "workspace", workspace)
	writeWorkspaceRuntimeInstruction(t, workspace, "Workspace rule v2.")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewLocalStack(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	reloadedClient, err := controlclient.BindSessionClient(
		reloaded.ControlClient(),
		controlclient.Principal{ID: "local-user"},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := reloadedClient.InspectSession(ctx, controlclient.StateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if state.CWD != workspace {
		t.Fatalf("reloaded Session state = %#v", state)
	}
	if _, ok := reloaded.sessionRuntimes.loaded(sessionID); ok {
		t.Fatal("Host restart or observation eagerly restored a Session Runtime")
	}
	runtime, _, err := reloaded.sessionRuntimes.activateSession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceRuntimeComposition(
		t,
		runtime.stack,
		workspace,
		"Workspace rule v2.",
		"Workspace rule v1.",
		"workspace-skill",
		"",
	)
}

func TestSessionObservationDoesNotActivateRuntime(t *testing.T) {
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
	sessionID := createWorkspaceRuntimeTestSession(t, client, "create-observed", "observed", "workspace", workspace)
	if _, ok := stack.sessionRuntimes.loaded(sessionID); ok {
		t.Fatal("CreateSession eagerly activated a Runtime")
	}

	state, err := client.InspectSession(ctx, controlclient.StateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if state.Run.Active || state.CWD != workspace {
		t.Fatalf("observed Session state = %#v", state)
	}
	first, err := client.Reconnect(ctx, controlclient.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Reconnect(ctx, controlclient.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		_ = first.Subscription.Close()
		t.Fatal(err)
	}
	if err := first.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := stack.sessionRuntimes.loaded(sessionID); ok {
		t.Fatal("inspect/reconnect observers activated a Session Runtime")
	}
}

func TestSessionRuntimeIdentityDoesNotPartitionByUser(t *testing.T) {
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

	firstRuntime := activateSessionRuntime(t, stack, firstID)
	secondRuntime := activateSessionRuntime(t, stack, secondID)
	firstSession, err := stack.Sessions.Session(ctx, session.SessionRef{SessionID: firstID})
	if err != nil {
		t.Fatal(err)
	}
	secondSession, err := stack.Sessions.Session(ctx, session.SessionRef{SessionID: secondID})
	if err != nil {
		t.Fatal(err)
	}
	if firstSession.UserID == secondSession.UserID {
		t.Fatalf("compatibility UserIDs unexpectedly equal: %q", firstSession.UserID)
	}
	if firstRuntime.workspace != secondRuntime.workspace {
		t.Fatal("UserID incorrectly partitioned one workspace identity")
	}
	if firstRuntime == secondRuntime {
		t.Fatal("different Sessions shared one Runtime")
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
	stack.sessionRuntimes.mu.RLock()
	sessionCount := len(stack.sessionRuntimes.sessions)
	_, loadedForeignWorkspace := stack.sessionRuntimes.workspaceCWD["workspace-b"]
	stack.sessionRuntimes.mu.RUnlock()
	if sessionCount != 0 || loadedForeignWorkspace {
		t.Fatalf("foreign preferred Session changed Runtime registry: sessions=%d foreign_workspace=%t", sessionCount, loadedForeignWorkspace)
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
	if err := stack.sessionRuntimes.release(ctx, sessionID); err != nil {
		t.Fatal(err)
	}

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

func TestSessionRuntimeRegistryCreatesSessionsLazilyConcurrently(t *testing.T) {
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

	stack.sessionRuntimes.mu.RLock()
	sessionCount := len(stack.sessionRuntimes.sessions)
	workspaceCount := len(stack.sessionRuntimes.workspaceCWD)
	stack.sessionRuntimes.mu.RUnlock()
	if sessionCount != 0 || workspaceCount != 2 {
		t.Fatalf("Runtime registry counts = sessions:%d workspaces:%d, want 0 and 2", sessionCount, workspaceCount)
	}
}

func TestSessionRuntimeRegistryQuiesceWaitsForInFlightActivation(t *testing.T) {
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
	active, err := stack.Sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName:            stack.AppName,
		UserID:             "local-user",
		Workspace:          session.WorkspaceRef{Key: "workspace-b", CWD: workspaceB},
		PreferredSessionID: "activation-during-close",
	})
	if err != nil {
		t.Fatal(err)
	}

	gate := stack.reconfigureLock()
	gate.Lock()
	gateLocked := true
	defer func() {
		if gateLocked {
			gate.Unlock()
		}
	}()

	activateDone := make(chan error, 1)
	go func() {
		_, _, activateErr := stack.sessionRuntimes.activateSession(context.Background(), active.SessionID)
		activateDone <- activateErr
	}()
	waitForSessionRuntimeRegistryState(t, stack.sessionRuntimes, false, 1)

	quiesceDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		quiesceDone <- stack.Quiesce(ctx)
	}()
	waitForSessionRuntimeRegistryState(t, stack.sessionRuntimes, true, 1)
	select {
	case err := <-quiesceDone:
		t.Fatalf("Quiesce returned before in-flight activation drained: %v", err)
	default:
	}

	gate.Unlock()
	gateLocked = false
	if err := <-activateDone; errorcode.CodeOf(err) != errorcode.Unavailable {
		t.Fatalf("activation during Quiesce error = %v, want unavailable", err)
	}
	if err := <-quiesceDone; err != nil {
		t.Fatalf("Quiesce() error = %v", err)
	}
	if _, loaded := stack.sessionRuntimes.loaded(active.SessionID); loaded {
		t.Fatal("Session Runtime completed registration after Quiesce")
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
	writeWorkspaceRuntimeInstruction(t, root, instruction)
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

func writeWorkspaceRuntimeInstruction(t *testing.T, root, instruction string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Workspace\n\n"+instruction+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillName := filepath.Base(root) + "-skill"
	skillPath := filepath.Join(root, ".agents", "skills", skillName, "SKILL.md")
	if _, err := os.Stat(skillPath); err == nil {
		skillContents := "---\nname: " + skillName + "\ndescription: " + instruction + "\n---\n# " + skillName + "\n"
		if err := os.WriteFile(skillPath, []byte(skillContents), 0o600); err != nil {
			t.Fatal(err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
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
	controlServer, err := controlserver.New(controlserver.HandlerConfig{
		Service:       stack.ControlClient(),
		Authenticator: authenticator,
		AllowedHosts:  []string{"127.0.0.1", "localhost", "::1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := testenv.NewHTTPServer(t, controlServer.Handler())
	remote, err := httpclient.New(httpclient.Config{
		BaseURL:     server.URL,
		BearerToken: token,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return remote
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

type blockingSessionControl struct {
	active  session.Session
	entered chan struct{}
	release chan struct{}
}

func (c *blockingSessionControl) HandoffController(
	ctx context.Context,
	_ agent.HandoffControllerRequest,
) (session.Session, error) {
	close(c.entered)
	select {
	case <-c.release:
		return c.active, nil
	case <-ctx.Done():
		return session.Session{}, ctx.Err()
	}
}

func (c *blockingSessionControl) AttachParticipant(
	context.Context,
	agent.AttachParticipantRequest,
) (session.Session, error) {
	return c.active, nil
}

func (c *blockingSessionControl) PromptParticipant(
	context.Context,
	agent.PromptParticipantRequest,
) (agent.RunResult, error) {
	return agent.RunResult{Session: c.active}, nil
}

func (c *blockingSessionControl) DetachParticipant(
	context.Context,
	agent.DetachParticipantRequest,
) (session.Session, error) {
	return c.active, nil
}

func waitForSessionRuntimeRegistryState(
	t *testing.T,
	registry *sessionRuntimeRegistry,
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
				"Session Runtime registry state = closed:%t building:%d, want closed:%t building:%d",
				actualClosed,
				actualBuilding,
				closed,
				building,
			)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForSessionRuntimeReleasing(
	t *testing.T,
	registry *sessionRuntimeRegistry,
	sessionID string,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if registry.isReleasing(sessionID) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Session %q Runtime did not enter releasing state", sessionID)
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

func activateSessionRuntime(t *testing.T, stack *Stack, sessionID string) *sessionRuntime {
	t.Helper()
	runtime, _, err := stack.sessionRuntimes.activateSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("activate Session %q: %v", sessionID, err)
	}
	return runtime
}

func assertSessionRuntimeSharingContract(t *testing.T, host, child *Stack) {
	t.Helper()
	if child == nil || child == host {
		t.Fatalf("Session child Stack = %p, host = %p", child, host)
	}
	if child.lookup == host.lookup || child.placementCache == host.placementCache {
		t.Fatal("Session child shared mutable model or placement configuration")
	}
	if child.store != host.store ||
		!sameSessionRuntimeReference(child.Sessions, host.Sessions) ||
		!sameSessionRuntimeReference(child.taskStore, host.taskStore) ||
		!sameSessionRuntimeReference(child.controlFeeds, host.controlFeeds) ||
		!sameSessionRuntimeReference(child.lifecycleCtx, host.lifecycleCtx) ||
		child.approvalRecovery != host.approvalRecovery ||
		child.codexAuth != host.codexAuth ||
		child.grokAuth != host.grokAuth ||
		child.apiKeyCredentials != host.apiKeyCredentials ||
		child.providerUsage != host.providerUsage ||
		child.reconfigureLock() != host.reconfigureLock() ||
		child.assemblyMutationLock() != host.assemblyMutationLock() {
		t.Fatal("Session child did not receive the required Host-shared state")
	}
	if child.currentGateway() == host.currentGateway() ||
		child.engine == host.engine ||
		child.exec == host.exec {
		t.Fatal("Session child shared execution composition state")
	}
	if child.mcpMgr != nil && child.mcpMgr == host.mcpMgr {
		t.Fatal("Session child shared its MCP manager")
	}
	if child.sessionRuntimes != nil ||
		child.controlState != nil ||
		child.controlCommands != nil ||
		child.controlClient != nil ||
		child.taskStreams != nil ||
		child.operations != nil ||
		child.lifecycleCancel != nil ||
		child.sandboxLifecycleFactory != nil ||
		child.refreshConfiguredAgentsHook != nil {
		t.Fatal("Session child received Host-only ownership state")
	}
}

func sameSessionRuntimeReference(left any, right any) bool {
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
		t.Fatalf("Session Runtime CWD = %q, want %q", stack.Workspace.CWD, wantCWD)
	}
	actualCWD, err := stack.exec.FileSystem().Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if actualCWD != wantCWD {
		t.Fatalf("Session sandbox CWD = %q, want %q", actualCWD, wantCWD)
	}
	prompt := stringFromMap(stack.runtime.BaseMetadata, "system_prompt")
	if !strings.Contains(prompt, wantInstruction) {
		t.Fatalf("Session prompt does not contain %q:\n%s", wantInstruction, prompt)
	}
	if strings.Contains(prompt, forbiddenInstruction) {
		t.Fatalf("Session prompt contains forbidden instruction %q:\n%s", forbiddenInstruction, prompt)
	}
	skills := make(map[string]struct{})
	for _, meta := range stack.runtime.SkillCatalog.Metas() {
		skills[meta.Name] = struct{}{}
	}
	if _, ok := skills[wantSkill]; !ok {
		t.Fatalf("Session skill catalog does not contain %q: %#v", wantSkill, skills)
	}
	if _, ok := skills[forbiddenSkill]; ok {
		t.Fatalf("Session skill catalog contains foreign skill %q: %#v", forbiddenSkill, skills)
	}
}
