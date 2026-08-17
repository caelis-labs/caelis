package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/control/agentbinding"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
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
	if runtimeA1 == runtimeA2 || runtimeA1.instance == runtimeA2.instance {
		t.Fatal("two Sessions in one workspace shared a live Runtime")
	}
	if runtimeA1.instance.currentGateway() == runtimeA2.instance.currentGateway() {
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
		runtimeA1.instance,
		workspaceA,
		"Workspace A rule v1.",
		"Workspace B rule.",
		"workspace-a-skill",
		"workspace-b-skill",
	)
	assertWorkspaceRuntimeComposition(
		t,
		runtimeB.instance,
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
		pinned.instance,
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
		runtimeA3.instance,
		workspaceA,
		"Workspace A rule v2.",
		"Workspace A rule v1.",
		"workspace-a-skill",
		"workspace-b-skill",
	)
	assertWorkspaceRuntimeComposition(
		t,
		runtimeA2.instance,
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
	if refreshed == runtimeA1 || refreshed.instance == runtimeA1.instance {
		t.Fatal("reactivated Session reused its released Runtime")
	}
	assertWorkspaceRuntimeComposition(
		t,
		refreshed.instance,
		workspaceA,
		"Workspace A rule v2.",
		"Workspace A rule v1.",
		"workspace-a-skill",
		"workspace-b-skill",
	)

	if _, err := remote.CloseSession(ctx, appserver.CloseSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "close-b", SessionID: sessionB},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := stack.sessionRuntimes.loaded(sessionB); ok {
		t.Fatal("closed Session retained a live Runtime")
	}
	if state, err := remote.InspectSession(ctx, appserver.StateRequest{SessionID: sessionB}); err != nil || state.CWD != workspaceB {
		t.Fatalf("inspect closed Session = %#v, %v", state, err)
	}
	closedPrompt, err := remote.Prompt(ctx, appserver.PromptRequest{
		WriteBase: appserver.WriteBase{OperationID: "prompt-closed-b", SessionID: sessionB},
		Input:     "must remain closed",
	})
	if !errors.Is(err, appserver.ErrSessionClosed) ||
		errorcode.CodeOf(err) != errorcode.FailedPrecondition ||
		closedPrompt.Outcome != appserver.OutcomeRejected {
		t.Fatalf("HTTP Prompt on closed Session = %#v, %v; want typed ErrSessionClosed", closedPrompt, err)
	}
	if _, ok := stack.sessionRuntimes.loaded(sessionB); ok {
		t.Fatal("inspect reactivated a closed Session Runtime")
	}
}

func TestSessionRuntimeSelectsNewCatalogModelPinsDeletionAndRepairsOnReactivation(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	t.Cleanup(func() { _ = stack.Close() })
	principal := appserver.Principal{ID: stack.composition.userID}
	runtime := activateSessionRuntime(t, stack, active.SessionID)
	initialID := stack.composition.lookup.DefaultID()

	profile, err := stack.connectTestModel(ModelConfig{
		Provider:        "ollama",
		API:             "ollama",
		Model:           "late-runtime-model",
		ReasoningLevels: []string{"high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lateID := profile.Backend.Provider.ModelConfigID
	active = mustCurrentSession(t, stack, active.SessionID)
	selected, err := stack.ConfigurationCommands().UseSessionModel(ctx, principal, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "select-late-runtime-model",
			SessionID:               active.SessionID,
			ExpectedRevision:        &active.Revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Model: lateID, ReasoningEffort: "high",
	})
	if err != nil || selected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("UseSessionModel(new catalog entry) = %#v, %v", selected, err)
	}
	if state, err := runtime.instance.SessionRuntimeState(ctx, active.SessionRef); err != nil || state.ModelID != lateID {
		t.Fatalf("active Runtime state after selection = %#v, %v; want %q", state, err, lateID)
	}

	if err := stack.deleteTestHostModel(ctx, session.SessionRef{}, lateID); err != nil {
		t.Fatal(err)
	}
	if state, err := runtime.instance.SessionRuntimeState(ctx, active.SessionRef); err != nil || state.ModelID != lateID {
		t.Fatalf("active Runtime state after deletion = %#v, %v; want pinned %q", state, err, lateID)
	}
	if _, err := stack.ConfigurationCommands().UseSessionModel(ctx, principal, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "reselect-deleted-runtime-model",
			SessionID:               active.SessionID,
			ExpectedRevision:        &selected.Revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Model: lateID,
	}); err == nil {
		t.Fatal("deleted catalog model remained selectable")
	}

	beforeRecovery := mustCurrentSession(t, stack, active.SessionID)
	beforeRecoveryState, err := stack.composition.sessions.SnapshotState(ctx, beforeRecovery.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := kernelimpl.CurrentModelAlias(beforeRecoveryState); got != lateID {
		t.Fatalf("durable model before Runtime release = %q, want stale reference %q", got, lateID)
	}
	if err := stack.sessionRuntimes.release(ctx, active.SessionID); err != nil {
		t.Fatal(err)
	}
	refreshed, recovered, err := stack.sessionRuntimes.activateSession(ctx, active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed == runtime {
		t.Fatal("reactivation reused deleted Runtime")
	}
	recoveredState, err := stack.composition.sessions.SnapshotState(ctx, recovered.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := kernelimpl.CurrentModelAlias(recoveredState); got != initialID {
		t.Fatalf("recovered Session model = %q, want current default %q", got, initialID)
	}
	if got := kernelimpl.CurrentReasoningEffort(recoveredState); got != "" {
		t.Fatalf("recovered Session reasoning effort = %q, want cleared incompatible override", got)
	}
	if recovered.Revision != beforeRecovery.Revision+1 {
		t.Fatalf("recovered Session revision = %d, want %d", recovered.Revision, beforeRecovery.Revision+1)
	}
}

func TestSpawnedSessionUsesParentRuntimeModelSnapshotAfterHostDeletion(t *testing.T) {
	ctx := context.Background()
	stack, parent := newLocalStateTestStack(t)
	t.Cleanup(func() { _ = stack.Close() })
	principal := appserver.Principal{ID: stack.composition.userID}

	profile, err := stack.connectTestModel(ModelConfig{
		Provider:            "ollama",
		API:                 "ollama",
		Model:               "spawn-frozen-model",
		BaseURL:             "http://frozen.example",
		ReasoningLevels:     []string{"high"},
		ContextWindowTokens: 196608,
	})
	if err != nil {
		t.Fatal(err)
	}
	modelID := profile.Backend.Provider.ModelConfigID
	parent = mustCurrentSession(t, stack, parent.SessionID)
	selected, err := stack.ConfigurationCommands().UseSessionModel(ctx, principal, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "select-spawn-frozen-model",
			SessionID:               parent.SessionID,
			ExpectedRevision:        &parent.Revision,
			ExpectedControllerEpoch: parent.Controller.EpochID,
		},
		Model: modelID, ReasoningEffort: "high",
	})
	if err != nil || selected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("UseSessionModel() = %#v, %v", selected, err)
	}
	parentRuntime := activateSessionRuntime(t, stack, parent.SessionID)
	if err := stack.deleteTestHostModel(ctx, session.SessionRef{}, modelID); err != nil {
		t.Fatal(err)
	}
	if stack.composition.lookup.HasAlias(modelID) {
		t.Fatalf("Host catalog still contains deleted model %q", modelID)
	}

	agentConfig, err := parentRuntime.instance.materializeDelegatedModel("", profile.ID, "high", parentRuntime.instance.runtime)
	if err != nil {
		t.Fatalf("materializeDelegatedModel() from frozen Runtime: %v", err)
	}
	if agentConfig.PinnedModel == nil || agentConfig.PinnedModel.ID != modelID {
		t.Fatalf("spawned Agent pin = %#v, want frozen model %q", agentConfig.PinnedModel, modelID)
	}
	child, err := startGatewayAppTestSession(ctx, stack, "spawn-frozen-child")
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := parentRuntime.instance.prepareSpawnedACPSession(ctx, tasksubagent.SpawnContext{}, child.SessionID, agentConfig)
	if err != nil {
		t.Fatalf("prepareSpawnedACPSession() = %v", err)
	}
	if remaining.ModelID != "" || remaining.ReasoningEffortConfigID != "" || remaining.ConfigValues[acpConfigReasoningID] != "" {
		t.Fatalf("remaining wire options = %#v, want provider identity kept process-local", remaining)
	}
	if remaining.ConfigValues[acpConfigModeID] != "manual" {
		t.Fatalf("remaining mode = %q, want manual", remaining.ConfigValues[acpConfigModeID])
	}
	state, err := stack.composition.sessions.SnapshotState(ctx, child.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := kernelimpl.CurrentModelAlias(state); got != modelID {
		t.Fatalf("child durable model = %q, want %q", got, modelID)
	}
	if got := kernelimpl.CurrentReasoningEffort(state); got != "high" {
		t.Fatalf("child durable effort = %q, want high", got)
	}

	childRuntime := activateSessionRuntime(t, stack, child.SessionID)
	pinned, err := childRuntime.instance.lookup.ResolveConfig(modelID)
	if err != nil {
		t.Fatalf("child Runtime did not receive process-local model pin: %v", err)
	}
	if pinned.BaseURL != "http://frozen.example" || pinned.ContextWindowTokens != 196608 {
		t.Fatalf("child Runtime model = %#v, want frozen provider snapshot", pinned)
	}
}

func TestSessionRuntimeModelPinIsInvisibleAndRollsBackWhenRevisionCASConflicts(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	t.Cleanup(func() { _ = stack.Close() })
	principal := appserver.Principal{ID: stack.composition.userID}
	runtime := activateSessionRuntime(t, stack, active.SessionID)
	beforeLookup := runtime.instance.lookup.Snapshot()
	beforeContextWindow := runtime.instance.lookup.contextWindow

	profile, err := stack.connectTestModel(ModelConfig{
		Provider:            "ollama",
		API:                 "ollama",
		Model:               "conflicted-runtime-model",
		ContextWindowTokens: beforeContextWindow + 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	lateID := profile.Backend.Provider.ModelConfigID
	active = mustCurrentSession(t, stack, active.SessionID)
	blocked := &blockingUpdateSessionService{
		Service: runtime.instance.sessions,
		entered: make(chan struct{}),
		proceed: make(chan struct{}),
	}
	runtime.instance.sessions = blocked

	type commandResult struct {
		result appserver.CommandResult
		err    error
	}
	commandDone := make(chan commandResult, 1)
	go func() {
		result, commandErr := stack.ConfigurationCommands().UseSessionModel(ctx, principal, appserver.SessionModelRequest{
			WriteBase: appserver.WriteBase{
				OperationID:             "conflicted-runtime-model-pin",
				SessionID:               active.SessionID,
				ExpectedRevision:        &active.Revision,
				ExpectedControllerEpoch: active.Controller.EpochID,
			},
			Model: lateID,
		})
		commandDone <- commandResult{result: result, err: commandErr}
	}()
	<-blocked.entered

	readDone := make(chan bool, 1)
	go func() {
		_, ok := runtime.instance.lookup.Config(lateID)
		readDone <- ok
	}()
	select {
	case visible := <-readDone:
		t.Fatalf("uncommitted Runtime model lookup was observable, present=%v", visible)
	case <-time.After(50 * time.Millisecond):
	}

	if _, err := stack.composition.sessions.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
		Update: func(state map[string]any) (map[string]any, error) {
			next := session.CloneState(state)
			if next == nil {
				next = map[string]any{}
			}
			next["test.concurrent_revision"] = true
			return next, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	close(blocked.proceed)
	command := <-commandDone
	if !errors.Is(command.err, session.ErrRevisionConflict) || command.result.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("UseSessionModel(conflicted) = %#v, %v", command.result, command.err)
	}
	if visible := <-readDone; visible {
		t.Fatal("conflicted model remained in the Runtime lookup")
	}
	if after := runtime.instance.lookup.Snapshot(); !reflect.DeepEqual(after, beforeLookup) {
		t.Fatalf("Runtime lookup after conflict = %#v, want %#v", after, beforeLookup)
	}
	if runtime.instance.lookup.contextWindow != beforeContextWindow {
		t.Fatalf("Runtime context window after conflict = %d, want %d", runtime.instance.lookup.contextWindow, beforeContextWindow)
	}
}

type blockingUpdateSessionService struct {
	session.Service
	entered chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (s *blockingUpdateSessionService) UpdateState(ctx context.Context, request session.UpdateStateRequest) (session.Session, error) {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-ctx.Done():
		return session.Session{}, ctx.Err()
	case <-s.proceed:
		return s.Service.UpdateState(ctx, request)
	}
}

func TestDormantSessionModelRecoveryClearsSelectionWhenCatalogIsEmpty(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	t.Cleanup(func() { _ = stack.Close() })
	modelID := stack.composition.lookup.DefaultID()
	updated, err := stack.composition.sessions.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
		Update: func(state map[string]any) (map[string]any, error) {
			next := session.CloneState(state)
			if next == nil {
				next = map[string]any{}
			}
			next[kernelimpl.StateCurrentModelAlias] = modelID
			next[kernelimpl.StateCurrentReasoningEffort] = "high"
			return next, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.deleteTestHostModel(ctx, session.SessionRef{}, modelID); err != nil {
		t.Fatal(err)
	}
	inspected, err := stack.ControlClient().InspectSession(ctx, appserver.Principal{ID: stack.composition.userID}, appserver.StateRequest{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Revision != updated.Revision {
		t.Fatalf("read-only InspectSession revision = %d, want unchanged %d", inspected.Revision, updated.Revision)
	}
	_, recovered, err := stack.sessionRuntimes.activateSession(ctx, active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := stack.composition.sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := kernelimpl.CurrentModelAlias(state); got != "" {
		t.Fatalf("recovered model = %q, want no configured model", got)
	}
	if got := kernelimpl.CurrentReasoningEffort(state); got != "" {
		t.Fatalf("recovered reasoning effort = %q, want cleared", got)
	}
	if recovered.Revision != updated.Revision+1 {
		t.Fatalf("recovered revision = %d, want %d", recovered.Revision, updated.Revision+1)
	}
}

func TestDormantSessionModelRecoveryReturnsLastRevisionConflict(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	t.Cleanup(func() { _ = stack.Close() })
	modelID := stack.composition.lookup.DefaultID()
	updated, err := stack.composition.sessions.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
		Update: func(state map[string]any) (map[string]any, error) {
			next := session.CloneState(state)
			if next == nil {
				next = map[string]any{}
			}
			next[kernelimpl.StateCurrentModelAlias] = modelID
			return next, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.deleteTestHostModel(ctx, session.SessionRef{}, modelID); err != nil {
		t.Fatal(err)
	}
	original := stack.composition.sessions
	conflicting := &alwaysRevisionConflictSessionService{Service: original}
	stack.composition.sessions = conflicting
	t.Cleanup(func() { stack.composition.sessions = original })

	_, err = stack.modelRecovery.repairMissingSessionModelSelection(ctx, stack.composition.sessions, updated)
	if !errors.Is(err, session.ErrRevisionConflict) {
		t.Fatalf("repairMissingSessionModelSelection() error = %v, want revision conflict", err)
	}
	if conflicting.calls != sessionModelRecoveryMaxAttempts {
		t.Fatalf("recovery UpdateState calls = %d, want %d", conflicting.calls, sessionModelRecoveryMaxAttempts)
	}
}

type alwaysRevisionConflictSessionService struct {
	session.Service
	calls int
}

func (s *alwaysRevisionConflictSessionService) UpdateState(context.Context, session.UpdateStateRequest) (session.Session, error) {
	s.calls++
	return session.Session{}, session.ErrRevisionConflict
}

func TestClosedSessionReconnectDoesNotRepairDeletedModelReference(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	t.Cleanup(func() { _ = stack.Close() })
	modelID := stack.composition.lookup.DefaultID()
	updated, err := stack.composition.sessions.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
		Update: func(state map[string]any) (map[string]any, error) {
			next := session.CloneState(state)
			if next == nil {
				next = map[string]any{}
			}
			next[kernelimpl.StateCurrentModelAlias] = modelID
			return next, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := stack.ControlClient().CloseSession(ctx, appserver.Principal{ID: stack.composition.userID}, appserver.CloseSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "close-before-model-recovery", SessionID: active.SessionID, ExpectedRevision: &updated.Revision},
	})
	if err != nil || closed.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("CloseSession() = %#v, %v", closed, err)
	}
	if err := stack.deleteTestHostModel(ctx, session.SessionRef{}, modelID); err != nil {
		t.Fatal(err)
	}
	reconnected, err := stack.ControlClient().Reconnect(ctx, appserver.Principal{ID: stack.composition.userID}, appserver.ReconnectRequest{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if reconnected.Subscription != nil {
		_ = reconnected.Subscription.Close()
	}
	if reconnected.State.Revision != closed.Revision {
		t.Fatalf("closed reconnect revision = %d, want unchanged %d", reconnected.State.Revision, closed.Revision)
	}
	state, err := stack.composition.sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := kernelimpl.CurrentModelAlias(state); got != modelID {
		t.Fatalf("closed Session model = %q, want untouched stale reference %q", got, modelID)
	}
}

func TestRejectedFirstCommandDoesNotPinSessionRuntime(t *testing.T) {
	ctx := context.Background()
	workspace := newWorkspaceRuntimeTestDir(t, "workspace-rejected", "Rejected activation rule.")
	stack, err := NewLocalStack(Config{
		StoreDir: t.TempDir(), WorkspaceKey: "workspace-rejected", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client := newWorkspaceRuntimeHTTPClient(t, stack, "local-user")
	sessionID := createWorkspaceRuntimeTestSession(
		t,
		client,
		"create-rejected-activation",
		"rejected-activation",
		"workspace-rejected",
		workspace,
	)
	active, err := stack.composition.sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	staleRevision := active.Revision + 1
	result, err := client.Prompt(ctx, appserver.PromptRequest{
		WriteBase: appserver.WriteBase{
			OperationID:      "reject-first-prompt",
			SessionID:        sessionID,
			ExpectedRevision: &staleRevision,
		},
		Input: "must not activate",
	})
	var outcomeErr *appserver.OutcomeError
	if !errors.As(err, &outcomeErr) ||
		outcomeErr.Outcome != appserver.OutcomeConflicted ||
		result.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("stale first Prompt = %#v, %v; want conflicted", result, err)
	}
	if _, loaded := stack.sessionRuntimes.loaded(sessionID); loaded {
		t.Fatal("stale first Prompt retained a Session Runtime")
	}
}

func TestAcquireControlRuntimeObservesWithoutRetainingAndReusesLoadedRuntime(t *testing.T) {
	ctx := context.Background()
	workspace := newWorkspaceRuntimeTestDir(t, "workspace-observe", "Observe rule.")
	stack, err := NewLocalStack(Config{
		StoreDir: t.TempDir(), WorkspaceKey: "workspace-observe", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client := newWorkspaceRuntimeHTTPClient(t, stack, "local-user")
	sessionID := createWorkspaceRuntimeTestSession(t, client, "create-observe", "session-observe", "workspace-observe", workspace)
	principal := appserver.Principal{ID: "local-user"}

	observed, err := stack.AcquireControlRuntime(ctx, principal, appserver.ActionSessionInspect, sessionID, false)
	if err != nil {
		t.Fatal(err)
	}
	if observed.ControlRuntimeView() == nil || observed.Session().SessionID != sessionID || observed.Session().CWD != workspace {
		t.Fatalf("observed lease = view:%t session=%#v", observed.ControlRuntimeView() != nil, observed.Session())
	}
	if _, ok := stack.sessionRuntimes.loaded(sessionID); ok {
		t.Fatal("read-only Control observation retained an idle Session Runtime")
	}
	if err := observed.Close(ctx); err != nil {
		t.Fatal(err)
	}

	loaded := activateSessionRuntime(t, stack, sessionID)
	pinned, err := stack.AcquireControlRuntime(ctx, principal, appserver.ActionSessionInspect, sessionID, false)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.runtime != &loaded.instance.runtimeComposition {
		t.Fatalf("loaded Runtime lease = %p, want %p", pinned.runtime, &loaded.instance.runtimeComposition)
	}
	if err := pinned.Close(ctx); err != nil {
		t.Fatal(err)
	}
	waitForSessionRuntimeUnloaded(t, stack.sessionRuntimes, sessionID)
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
	client, err := appserver.BindSessionClient(
		stack.ControlClient(),
		appserver.Principal{ID: "local-user"},
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
	active, err := stack.composition.sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	control := &blockingSessionControl{
		active:  active,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	gateway, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.composition.sessions,
		Runtime:  controlClientBlockingRuntime{},
		Resolver: blockingResolver{},
		Control:  control,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.instance.mu.Lock()
	runtime.instance.gateway = gateway
	runtime.instance.mu.Unlock()

	commandDone := make(chan error, 1)
	go func() {
		_, commandErr := stack.ExecuteControlCommand(
			ctx,
			appserver.Principal{ID: "local-user"},
			appserver.ActionControllerHandoff,
			appserver.HandoffRequest{
				WriteBase: appserver.WriteBase{
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
	client, err := appserver.BindSessionClient(
		stack.ControlClient(),
		appserver.Principal{ID: "local-user"},
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
	runtime.instance.workspaceCloseMu.Lock()
	runtime.instance.mu.Lock()
	original := runtime.instance.exec
	runtime.instance.exec = retryable
	runtime.instance.mu.Unlock()
	runtime.instance.workspaceCloseMu.Unlock()
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

func TestSessionRuntimeSandboxConfigIsDetachedFromHostMutation(t *testing.T) {
	ctx := context.Background()
	workspace := newWorkspaceRuntimeTestDir(t, "workspace", "Workspace rule.")
	initialWritableRoot := filepath.Join(workspace, "initial-writable")
	updatedWritableRoot := filepath.Join(workspace, "updated-writable")
	if err := os.MkdirAll(initialWritableRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(updatedWritableRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	storeDir := t.TempDir()
	store := newAppConfigStore(storeDir)
	doc, err := store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.Sandbox = SandboxConfig{RequestedType: "host", WritableRoots: []string{initialWritableRoot}}
	if _, err := store.CompareAndSave(ctx, doc.ConfigurationRevision, doc); err != nil {
		t.Fatal(err)
	}
	stack, err := NewLocalStack(Config{
		StoreDir:     storeDir,
		WorkspaceKey: "workspace",
		WorkspaceCWD: workspace,
		SkillDirs:    []string{},
		Sandbox:      SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client := newWorkspaceRuntimeHTTPClient(t, stack, "local-user")
	firstID := createWorkspaceRuntimeTestSession(t, client, "create-first", "session-first", "workspace", workspace)
	observation, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: firstID})
	if err != nil {
		t.Fatal(err)
	}
	first := activateSessionRuntime(t, stack, firstID)
	assertSessionRuntimeIsolationContract(t, stack, first.instance)

	blockingRuntime := &controlClientLifecycleRuntime{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	blockingGateway, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.composition.sessions,
		Runtime:  blockingRuntime,
		Resolver: controlClientIngressResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	first.instance.mu.Lock()
	first.instance.gateway = blockingGateway
	first.instance.mu.Unlock()
	prompt, err := client.Prompt(ctx, appserver.PromptRequest{
		WriteBase: appserver.WriteBase{OperationID: "prompt-first", SessionID: firstID},
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
	if err := observation.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if current, loaded := stack.sessionRuntimes.loaded(firstID); !loaded || current != first {
		t.Fatal("detaching the last observer released a running Session Runtime")
	}

	doc, err = stack.composition.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.Sandbox.WritableRoots = []string{updatedWritableRoot}
	if _, err := stack.composition.store.CompareAndSave(ctx, doc.ConfigurationRevision, doc); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first.instance.sandbox.WritableRoots, []string{initialWritableRoot}) {
		t.Fatalf("live Session sandbox writable roots = %#v, want pinned initial snapshot", first.instance.sandbox.WritableRoots)
	}
	if _, err := client.Cancel(ctx, appserver.CancelRequest{
		WriteBase: appserver.WriteBase{OperationID: "cancel-first", SessionID: firstID},
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
	waitForSessionRuntimeUnloaded(t, stack.sessionRuntimes, firstID)
	secondID := createWorkspaceRuntimeTestSession(t, client, "create-second", "session-second", "workspace", workspace)
	second := activateSessionRuntime(t, stack, secondID)
	if !slices.Equal(second.instance.sandbox.WritableRoots, []string{updatedWritableRoot}) {
		t.Fatalf("new Session sandbox writable roots = %#v, want current snapshot", second.instance.sandbox.WritableRoots)
	}
	refreshed, _, err := stack.sessionRuntimes.activateSession(ctx, firstID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(refreshed.instance.sandbox.WritableRoots, []string{updatedWritableRoot}) {
		t.Fatalf("reactivated Session sandbox writable roots = %#v, want current snapshot", refreshed.instance.sandbox.WritableRoots)
	}
}

func TestSetSandboxBackendRollsBackWhenRequiredBackendIsUnavailable(t *testing.T) {
	ctx := context.Background()
	stack, err := NewLocalStack(Config{
		StoreDir:     t.TempDir(),
		WorkspaceKey: "workspace",
		WorkspaceCWD: newWorkspaceRuntimeTestDir(t, "workspace", "Workspace rule."),
		SkillDirs:    []string{},
		Sandbox:      SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	before := cloneSandboxConfig(stack.composition.sandbox)

	_, _, err = stack.setSandboxBackendAtRevision(ctx, "auto", nil)
	if err == nil {
		t.Skip("platform-required sandbox backend is available; unavailable rollback path is not applicable")
	}
	var unavailable *sandbox.BackendUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("SetSandboxBackend(auto) error = %v, want backend unavailable", err)
	}
	if !reflect.DeepEqual(stack.composition.sandbox, before) {
		t.Fatalf("failed sandbox mutation left config = %#v, want rolled back %#v", stack.composition.sandbox, before)
	}
}

func TestSessionRuntimeActivationWaitsForReleaseAndBuildsFreshRuntime(t *testing.T) {
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
	client, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := createWorkspaceRuntimeTestSession(t, client, "create-release-race", "release-race", "workspace", workspace)
	runtime := activateSessionRuntime(t, stack, sessionID)
	active, err := stack.composition.sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	allowStop := make(chan struct{})
	blockingGateway, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.composition.sessions,
		Runtime:  &blockingRuntime{session: active, release: allowStop},
		Resolver: blockingResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.instance.mu.Lock()
	runtime.instance.gateway = blockingGateway
	runtime.instance.mu.Unlock()
	if _, err := client.Prompt(ctx, appserver.PromptRequest{
		WriteBase: appserver.WriteBase{OperationID: "prompt-release-race", SessionID: sessionID},
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

	if loaded, ok := stack.sessionRuntimes.loaded(sessionID); ok || loaded != nil {
		t.Fatalf("loaded releasing Runtime = %p, %t; want hidden tombstone", loaded, ok)
	}

	type activationResult struct {
		runtime *sessionRuntime
		err     error
	}
	activationDone := make(chan activationResult, 1)
	go func() {
		activateCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		activated, _, activateErr := stack.sessionRuntimes.activateSession(activateCtx, sessionID)
		activationDone <- activationResult{runtime: activated, err: activateErr}
	}()
	select {
	case result := <-activationDone:
		t.Fatalf("activation returned before the previous Runtime released: %#v", result)
	default:
	}

	close(allowStop)
	if err := <-releaseDone; err != nil {
		t.Fatalf("release() error = %v", err)
	}
	result := <-activationDone
	if result.err != nil {
		t.Fatalf("activation after release error = %v", result.err)
	}
	if result.runtime == nil || result.runtime == runtime || result.runtime.instance == runtime.instance {
		t.Fatalf("activation after release reused old Runtime: old=%p new=%p", runtime, result.runtime)
	}
	stack.sessionRuntimes.mu.RLock()
	retained := stack.sessionRuntimes.sessions[sessionID]
	stack.sessionRuntimes.mu.RUnlock()
	if retained != result.runtime {
		t.Fatalf("registered Runtime after release = %p, want %p", retained, result.runtime)
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
	client, err := appserver.BindSessionClient(first.ControlClient(), appserver.Principal{ID: "local-user"})
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
	reloadedClient, err := appserver.BindSessionClient(
		reloaded.ControlClient(),
		appserver.Principal{ID: "local-user"},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := reloadedClient.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
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
		runtime.instance,
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
	client, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := createWorkspaceRuntimeTestSession(t, client, "create-observed", "observed", "workspace", workspace)
	if _, ok := stack.sessionRuntimes.loaded(sessionID); ok {
		t.Fatal("CreateSession eagerly activated a Runtime")
	}

	state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if state.Run.Active || state.CWD != workspace {
		t.Fatalf("observed Session state = %#v", state)
	}
	first, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
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

func TestSessionRuntimeLivesUntilLastObserverDetaches(t *testing.T) {
	ctx := context.Background()
	workspace := newWorkspaceRuntimeTestDir(t, "observer-lifetime", "Observer lifetime rule.")
	stack, err := NewLocalStack(Config{
		StoreDir: t.TempDir(), WorkspaceKey: "observer-lifetime", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client := newWorkspaceRuntimeHTTPClient(t, stack, "local-user")
	sessionID := createWorkspaceRuntimeTestSession(
		t, client, "create-observer-lifetime", "observer-lifetime", "observer-lifetime", workspace,
	)
	first, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		_ = first.Subscription.Close()
		t.Fatal(err)
	}
	runtime := activateSessionRuntime(t, stack, sessionID)
	releaseUse, err := stack.sessionRuntimes.acquireRuntimeUse(runtime)
	if err != nil {
		t.Fatal(err)
	}
	releaseUse()
	if err := first.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if current, loaded := stack.sessionRuntimes.loaded(sessionID); !loaded || current != runtime {
		t.Fatal("closing one of two observers released the Session Runtime")
	}
	if err := second.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	waitForSessionRuntimeUnloaded(t, stack.sessionRuntimes, sessionID)

	writeWorkspaceRuntimeInstruction(t, workspace, "Observer lifetime rule v2.")
	refreshed := activateSessionRuntime(t, stack, sessionID)
	if refreshed == runtime || refreshed.instance == runtime.instance {
		t.Fatal("reactivation reused the Runtime released by the last observer")
	}
	assertWorkspaceRuntimeComposition(
		t, refreshed.instance, workspace,
		"Observer lifetime rule v2.", "Observer lifetime rule.",
		"observer-lifetime-skill", "",
	)
}

func TestSessionRuntimeRunningWorkSurvivesObserverDetach(t *testing.T) {
	ctx := context.Background()
	workspace := newWorkspaceRuntimeTestDir(t, "work-lifetime", "Work lifetime rule.")
	stack, err := NewLocalStack(Config{
		StoreDir: t.TempDir(), WorkspaceKey: "work-lifetime", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := createWorkspaceRuntimeTestSession(
		t, client, "create-work-lifetime", "work-lifetime", "work-lifetime", workspace,
	)
	observed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	runtime := activateSessionRuntime(t, stack, sessionID)
	releaseUse, err := stack.sessionRuntimes.acquireRuntimeUse(runtime)
	if err != nil {
		t.Fatal(err)
	}
	releaseWork := stack.sessionRuntimes.retainRuntimeWork(runtime, session.SessionRef{SessionID: sessionID})
	releaseUse()
	if err := observed.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if current, loaded := stack.sessionRuntimes.loaded(sessionID); !loaded || current != runtime {
		t.Fatal("observer detach released a Runtime with running work")
	}
	releaseWork()
	waitForSessionRuntimeUnloaded(t, stack.sessionRuntimes, sessionID)
}

func TestSessionRuntimeDurableRunningTaskSurvivesObserverDetach(t *testing.T) {
	ctx := context.Background()
	workspace := newWorkspaceRuntimeTestDir(t, "task-lifetime", "Task lifetime rule.")
	stack, err := NewLocalStack(Config{
		StoreDir: t.TempDir(), WorkspaceKey: "task-lifetime", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := createWorkspaceRuntimeTestSession(
		t, client, "create-task-lifetime", "task-lifetime", "task-lifetime", workspace,
	)
	observed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	runtime := activateSessionRuntime(t, stack, sessionID)
	releaseUse, err := stack.sessionRuntimes.acquireRuntimeUse(runtime)
	if err != nil {
		t.Fatal(err)
	}
	releaseUse()
	entry := &taskapi.Entry{
		TaskID: "task-running-after-detach", Kind: taskapi.KindSubagent,
		Session: session.SessionRef{SessionID: sessionID}, State: taskapi.StateRunning, Running: true,
	}
	if err := stack.composition.taskStore.Upsert(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if err := observed.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if current, loaded := stack.sessionRuntimes.loaded(sessionID); !loaded || current != runtime {
		t.Fatal("observer detach released a Runtime with a durable running Task")
	}
	entry.State = taskapi.StateCompleted
	entry.Running = false
	if err := stack.composition.taskStore.Upsert(ctx, entry); err != nil {
		t.Fatal(err)
	}
	runtime.instance.runtimeTaskChanged(session.SessionRef{SessionID: sessionID})
	waitForSessionRuntimeUnloaded(t, stack.sessionRuntimes, sessionID)
}

func TestSessionRuntimeIdleReleaseRetriesTransientTaskReadFailure(t *testing.T) {
	ctx := context.Background()
	workspace := newWorkspaceRuntimeTestDir(t, "task-read-retry", "Task read retry rule.")
	stack, err := NewLocalStack(Config{
		StoreDir: t.TempDir(), WorkspaceKey: "task-read-retry", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := createWorkspaceRuntimeTestSession(
		t, client, "create-task-read-retry", "task-read-retry", "task-read-retry", workspace,
	)
	observed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	runtime := activateSessionRuntime(t, stack, sessionID)
	releaseUse, err := stack.sessionRuntimes.acquireRuntimeUse(runtime)
	if err != nil {
		t.Fatal(err)
	}
	releaseUse()
	transientTasks := &transientListTaskStore{
		Store:        stack.composition.taskStore,
		failingUntil: time.Now().Add(15 * time.Millisecond),
	}
	stack.sessionRuntimes.tasks = transientTasks
	if err := observed.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	waitForSessionRuntimeUnloaded(t, stack.sessionRuntimes, sessionID)
	if transientTasks.failureCount() == 0 {
		t.Fatal("idle release did not exercise the injected transient Task reader")
	}
}

type transientListTaskStore struct {
	taskapi.Store
	mu           sync.Mutex
	failingUntil time.Time
	failures     int
}

func (s *transientListTaskStore) ListSession(ctx context.Context, ref session.SessionRef) ([]*taskapi.Entry, error) {
	s.mu.Lock()
	if time.Now().Before(s.failingUntil) {
		s.failures++
		s.mu.Unlock()
		return nil, errors.New("transient task index read")
	}
	s.mu.Unlock()
	return s.Store.ListSession(ctx, ref)
}

func (s *transientListTaskStore) failureCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures
}

func TestParticipantHandlesUseFixedSessionRuntimeSnapshot(t *testing.T) {
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
	if _, err := stack.connectTestModel(ModelConfig{Provider: "ollama", Model: "llama3"}); err != nil {
		t.Fatal(err)
	}
	status, err := stack.AgentBindings().AgentBindingStatus(ctx)
	if err != nil || len(status.Targets) == 0 {
		t.Fatalf("AgentBindingStatus() = %#v, %v", status, err)
	}
	if _, err := stack.testAgentBindings().BindAgentBinding(ctx, agentbinding.Binding{
		Handle:    agentbinding.HandleOrbit,
		ProfileID: status.Targets[0].ID,
		Effort:    status.Targets[0].Effort.DefaultEffort,
	}); err != nil {
		t.Fatal(err)
	}

	sessions, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	participants, err := appserver.BindParticipantClient(stack.ControlParticipants(), appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	firstID := createWorkspaceRuntimeTestSession(t, sessions, "create-first-handles", "first-handles", "workspace", workspace)
	if handles, err := participants.Handles(ctx, firstID); err != nil || !slices.Contains(handles, "orbit") {
		t.Fatalf("idle participant handles = %#v, %v; want orbit", handles, err)
	}
	activateSessionRuntime(t, stack, firstID)
	if _, err := stack.testAgentBindings().ResetAgentBinding(ctx, agentbinding.HandleOrbit); err != nil {
		t.Fatal(err)
	}
	if handles, err := participants.Handles(ctx, firstID); err != nil || !slices.Contains(handles, "orbit") {
		t.Fatalf("active participant handles = %#v, %v; want frozen orbit", handles, err)
	}

	secondID := createWorkspaceRuntimeTestSession(t, sessions, "create-second-handles", "second-handles", "workspace", workspace)
	if handles, err := participants.Handles(ctx, secondID); err != nil || slices.Contains(handles, "orbit") {
		t.Fatalf("new participant handles = %#v, %v; want current unbound catalog", handles, err)
	}
	if _, loaded := stack.sessionRuntimes.loaded(secondID); loaded {
		t.Fatal("participant handle observation activated an idle Session Runtime")
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

	first, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: "compatibility-principal"})
	if err != nil {
		t.Fatal(err)
	}
	firstID := createWorkspaceRuntimeTestSession(t, first, "create-first", "session-first", "workspace", workspace)
	secondID := createWorkspaceRuntimeTestSession(t, second, "create-second", "session-second", "workspace", workspace)

	firstRuntime := activateSessionRuntime(t, stack, firstID)
	secondRuntime := activateSessionRuntime(t, stack, secondID)
	firstSession, err := stack.composition.sessions.Session(ctx, session.SessionRef{SessionID: firstID})
	if err != nil {
		t.Fatal(err)
	}
	secondSession, err := stack.composition.sessions.Session(ctx, session.SessionRef{SessionID: secondID})
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

	owner, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: "other"})
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

	result, err := other.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "probe-foreign-session"},
		PreferredSessionID: sessionID,
		WorkspaceKey:       "workspace-b",
		CWD:                workspaceB,
	})
	if err == nil ||
		result.Outcome != appserver.OutcomeRejected ||
		errorcode.CodeOf(err) != errorcode.PermissionDenied {
		t.Fatalf("foreign preferred CreateSession() = %#v, %v; want rejected permission_denied", result, err)
	}
	if strings.Contains(result.Detail, workspaceA) ||
		strings.Contains(result.Detail, workspaceB) ||
		strings.Contains(err.Error(), workspaceA) ||
		strings.Contains(err.Error(), workspaceB) {
		t.Fatalf("foreign preferred Session error disclosed a workspace path: %#v, %v", result, err)
	}
	active, err := stack.composition.sessions.Session(ctx, session.SessionRef{SessionID: sessionID})
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
	client, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: "local-user"})
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

	originalSessions := stack.composition.sessions
	stack.composition.sessions = sessionLookupErrorService{
		Service: originalSessions,
		err:     errors.New("synthetic routing read failure"),
	}
	request := appserver.CloseSessionRequest{
		WriteBase: appserver.WriteBase{
			OperationID: "close-routing-failure",
			SessionID:   sessionID,
		},
	}
	result, err := client.CloseSession(ctx, request)
	stack.composition.sessions = originalSessions
	if err == nil ||
		result.Outcome != appserver.OutcomeRejected ||
		errorcode.CodeOf(err) != errorcode.Internal {
		t.Fatalf("pre-dispatch routing failure = %#v, %v; want rejected internal", result, err)
	}
	closed, err := appserver.IsSessionClosed(ctx, originalSessions, session.SessionRef{SessionID: sessionID})
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
	if replayed.Outcome != appserver.OutcomeRejected {
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
	client, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: "local-user"})
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
			result, createErr := client.CreateSession(context.Background(), appserver.CreateSessionRequest{
				WriteBase:          appserver.WriteBase{OperationID: "create-" + sessionID},
				PreferredSessionID: sessionID,
				WorkspaceKey:       key,
				CWD:                cwd,
			})
			if createErr != nil {
				errs <- createErr
				return
			}
			if result.Outcome != appserver.OutcomeCommitted || result.SessionID != sessionID {
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
	active, err := stack.composition.sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName:            stack.composition.appName,
		UserID:             "local-user",
		Workspace:          session.WorkspaceRef{Key: "workspace-b", CWD: workspaceB},
		PreferredSessionID: "activation-during-close",
	})
	if err != nil {
		t.Fatal(err)
	}

	gate := &stack.sessionRuntimes.activationMu
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

func TestAcquireControlRuntimeRetriesAcrossIdleRelease(t *testing.T) {
	workspace := newWorkspaceRuntimeTestDir(t, "acquire-retry", "Acquire retry rule.")
	stack, err := NewLocalStack(Config{
		StoreDir: t.TempDir(), WorkspaceKey: "acquire-retry", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	active, err := stack.composition.sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: stack.composition.appName, UserID: stack.composition.userID,
		Workspace:          session.WorkspaceRef{Key: "acquire-retry", CWD: workspace},
		PreferredSessionID: "acquire-retry-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := activateSessionRuntime(t, stack, active.SessionID)
	releaseUse, err := stack.sessionRuntimes.acquireRuntimeUse(runtime)
	if err != nil {
		t.Fatal(err)
	}
	releaseUse()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	acquired, _, release, err := stack.sessionRuntimes.acquireControlRuntime(ctx, active.SessionID, true)
	if err != nil {
		t.Fatalf("acquireControlRuntime() during idle release = %v", err)
	}
	if release != nil {
		t.Cleanup(func() { _ = release(context.Background()) })
	}
	if acquired == nil {
		t.Fatal("acquireControlRuntime() returned a nil Runtime")
	}
}

func TestCloseWaitsForScheduledIdleReleaseBeforeStoreCleanup(t *testing.T) {
	storeDir := t.TempDir()
	workspace := newWorkspaceRuntimeTestDir(t, "close-idle-release", "Close idle-release rule.")
	stack, err := NewLocalStack(Config{
		StoreDir: storeDir, WorkspaceKey: "close-idle-release", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.composition.sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: stack.composition.appName, UserID: stack.composition.userID,
		Workspace:          session.WorkspaceRef{Key: "close-idle-release", CWD: workspace},
		PreferredSessionID: "close-idle-release-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := activateSessionRuntime(t, stack, active.SessionID)
	releaseUse, err := stack.sessionRuntimes.acquireRuntimeUse(runtime)
	if err != nil {
		t.Fatal(err)
	}
	releaseUse()
	if err := stack.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	sessionsDir := filepath.Join(storeDir, "sessions")
	if err := os.RemoveAll(sessionsDir); err != nil {
		t.Fatalf("RemoveAll(%s) after Close() = %v", sessionsDir, err)
	}
}

func TestSessionRuntimeRegistryQuiesceWaitsForRuntimeWorkAfterGatewayDrain(t *testing.T) {
	workspace := newWorkspaceRuntimeTestDir(t, "quiesce-work", "Quiesce work rule.")
	stack, err := NewLocalStack(Config{
		StoreDir: t.TempDir(), WorkspaceKey: "quiesce-work", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	active, err := stack.composition.sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: stack.composition.appName, UserID: stack.composition.userID,
		Workspace:          session.WorkspaceRef{Key: "quiesce-work", CWD: workspace},
		PreferredSessionID: "quiesce-work-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := activateSessionRuntime(t, stack, active.SessionID)
	releaseUse, err := stack.sessionRuntimes.acquireRuntimeUse(runtime)
	if err != nil {
		t.Fatal(err)
	}
	releaseWork := stack.sessionRuntimes.retainRuntimeWork(runtime, active.SessionRef)
	releaseUse()

	quiesceDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		quiesceDone <- stack.Quiesce(ctx)
	}()
	waitForSessionRuntimeRegistryState(t, stack.sessionRuntimes, true, 0)
	select {
	case err := <-quiesceDone:
		t.Fatalf("Quiesce returned before Runtime work released: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	releaseWork()
	if err := <-quiesceDone; err != nil {
		t.Fatalf("Quiesce() error = %v", err)
	}
}

func TestSessionRuntimeActivationHasNoHostConfigurationMutationLock(t *testing.T) {
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
	active, err := stack.composition.sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName:            stack.composition.appName,
		UserID:             stack.composition.userID,
		Workspace:          stack.composition.workspace,
		PreferredSessionID: "activation-with-host-mutation",
	})
	if err != nil {
		t.Fatal(err)
	}

	activated := make(chan error, 1)
	go func() {
		_, _, activateErr := stack.sessionRuntimes.activateSession(context.Background(), active.SessionID)
		activated <- activateErr
	}()
	select {
	case err := <-activated:
		if err != nil {
			t.Fatalf("activate Session while Host config mutation is locked: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Session activation waited for the Host configuration mutation lock")
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
	client, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}

	result, err := client.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "ambiguous-workspace"},
		PreferredSessionID: "must-not-exist",
		WorkspaceKey:       "shared-key",
		CWD:                workspaceB,
	})
	if err == nil {
		t.Fatalf("CreateSession() = %#v, nil; want ambiguous workspace rejection", result)
	}
	if result.Outcome != appserver.OutcomeRejected || errorcode.CodeOf(err) != errorcode.InvalidArgument {
		t.Fatalf("CreateSession() = %#v, %v; want rejected invalid_argument", result, err)
	}
	if _, loadErr := stack.composition.sessions.Session(ctx, session.SessionRef{SessionID: "must-not-exist"}); !errors.Is(loadErr, session.ErrSessionNotFound) {
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
) appserver.SessionClient {
	t.Helper()
	const token = "0123456789abcdef0123456789abcdef"
	authenticator, err := controlserver.BearerTokenAuthenticator(
		token,
		appserver.Principal{ID: principalID},
	)
	if err != nil {
		t.Fatal(err)
	}
	controlServer, err := controlserver.New(controlserver.HandlerConfig{
		Services:      gatewayTestAppServerServices(stack.ControlClient(), gatewayTestStatusService{}, stack.TaskStreams()),
		Authenticator: authenticator,
		AllowedHosts:  []string{"127.0.0.1", "localhost", "::1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := testenv.NewHTTPServer(t, controlServer.Handler())
	remote, err := httpclient.New(httpclient.Config{
		BaseURL:       server.URL,
		BearerToken:   token,
		HTTPClient:    server.Client(),
		Compatibility: appserver.CurrentCompatibility(),
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

func waitForSessionRuntimeUnloaded(
	t *testing.T,
	registry *sessionRuntimeRegistry,
	sessionID string,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, loaded := registry.loaded(sessionID); !loaded && !registry.isReleasing(sessionID) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Session %q Runtime did not release after becoming idle", sessionID)
		}
		time.Sleep(time.Millisecond)
	}
}

func createWorkspaceRuntimeTestSession(
	t *testing.T,
	client appserver.SessionClient,
	operationID string,
	sessionID string,
	workspaceKey string,
	cwd string,
) string {
	t.Helper()
	result, err := client.CreateSession(context.Background(), appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: operationID},
		PreferredSessionID: sessionID,
		WorkspaceKey:       workspaceKey,
		CWD:                cwd,
		Title:              sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != appserver.OutcomeCommitted || result.SessionID != sessionID {
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

func assertSessionRuntimeIsolationContract(t *testing.T, host *Stack, instance *sessionRuntimeInstance) {
	t.Helper()
	if instance == nil {
		t.Fatal("Session Runtime instance is nil")
	}
	if instance.lookup == host.composition.lookup || instance.placementCache == host.composition.placementCache {
		t.Fatal("Session Runtime shared mutable model or placement configuration")
	}
	if instance.appName != host.composition.appName ||
		instance.userID != host.composition.userID ||
		instance.storeDir != host.composition.storeDir ||
		instance.leaseOwnerID != host.composition.leaseOwnerID ||
		instance.store != host.composition.store ||
		!sameSessionRuntimeReference(instance.sessions, host.composition.sessions) ||
		!sameSessionRuntimeReference(instance.taskStore, host.composition.taskStore) ||
		!sameSessionRuntimeReference(instance.controlFeeds, host.composition.controlFeeds) ||
		!sameSessionRuntimeReference(instance.lifecycleCtx, host.composition.lifecycleCtx) ||
		instance.approvalRecovery != host.composition.approvalRecovery ||
		instance.codexAuth != host.composition.codexAuth ||
		instance.grokAuth != host.composition.grokAuth ||
		instance.apiKeyCredentials != host.composition.apiKeyCredentials ||
		instance.providerUsage != host.composition.providerUsage ||
		instance.modelCatalog != host.composition.lookup ||
		instance.sessionModelPins != host.composition.sessionModelPins ||
		instance.hostedChildMailbox == nil {
		t.Fatal("Session Runtime did not receive the required borrowed Host authorities")
	}
	if instance.currentGateway() == host.composition.currentGateway() ||
		instance.engine == host.composition.engine ||
		instance.exec == host.composition.exec {
		t.Fatal("Session Runtime shared execution composition state")
	}
	if instance.mcpMgr != nil && instance.mcpMgr == host.composition.mcpMgr {
		t.Fatal("Session Runtime shared its MCP manager")
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
	instance *sessionRuntimeInstance,
	wantCWD string,
	wantInstruction string,
	forbiddenInstruction string,
	wantSkill string,
	forbiddenSkill string,
) {
	t.Helper()
	if instance.workspace.CWD != wantCWD {
		t.Fatalf("Session Runtime CWD = %q, want %q", instance.workspace.CWD, wantCWD)
	}
	actualCWD, err := instance.exec.FileSystem().Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if actualCWD != wantCWD {
		t.Fatalf("Session sandbox CWD = %q, want %q", actualCWD, wantCWD)
	}
	prompt := stringFromMap(instance.runtime.BaseMetadata, "system_prompt")
	if !strings.Contains(prompt, wantInstruction) {
		t.Fatalf("Session prompt does not contain %q:\n%s", wantInstruction, prompt)
	}
	if strings.Contains(prompt, forbiddenInstruction) {
		t.Fatalf("Session prompt contains forbidden instruction %q:\n%s", forbiddenInstruction, prompt)
	}
	skills := make(map[string]struct{})
	for _, meta := range instance.runtime.SkillCatalog.Metas() {
		skills[meta.Name] = struct{}{}
	}
	if _, ok := skills[wantSkill]; !ok {
		t.Fatalf("Session skill catalog does not contain %q: %#v", wantSkill, skills)
	}
	if _, ok := skills[forbiddenSkill]; ok {
		t.Fatalf("Session skill catalog contains foreign skill %q: %#v", forbiddenSkill, skills)
	}
}
