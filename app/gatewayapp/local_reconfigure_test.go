package gatewayapp

import (
	"context"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/plugin"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

func TestSandboxConfigurationCommandUsesHostCASAndSharedLedger(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.UserID}
	expected, err := stack.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := appserver.SandboxRequest{
		WriteBase: appserver.WriteBase{OperationID: "sandbox-host-cas", ExpectedRevision: &expected},
		Backend:   "host",
	}
	first, err := stack.ConfigurationCommands().SetSandboxBackend(ctx, principal, request)
	if err != nil || first.Outcome != appserver.OutcomeCommitted || first.Revision != expected+1 {
		t.Fatalf("SetSandboxBackend() = %#v, %v", first, err)
	}
	if actual, err := stack.ConfigurationRevision(ctx); err != nil || actual != first.Revision {
		t.Fatalf("ConfigurationRevision() = %d, %v; want %d", actual, err, first.Revision)
	}
	replayed, err := stack.ConfigurationCommands().SetSandboxBackend(ctx, principal, request)
	if err != nil || replayed != first {
		t.Fatalf("SetSandboxBackend(replay) = %#v, %v; want %#v", replayed, err, first)
	}
	changed := request
	changed.Backend = "auto"
	conflicted, err := stack.ConfigurationCommands().SetSandboxBackend(ctx, principal, changed)
	if !errors.Is(err, appserver.ErrOperationConflict) || conflicted.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("SetSandboxBackend(changed payload) = %#v, %v", conflicted, err)
	}
	stale, err := stack.ConfigurationCommands().ResetSandbox(ctx, principal, appserver.SandboxRequest{WriteBase: appserver.WriteBase{
		OperationID: "sandbox-stale", ExpectedRevision: &expected,
	}})
	if err == nil || stale.Outcome != appserver.OutcomeConflicted || stale.Revision != first.Revision || errorcode.CodeOf(err) != errorcode.Conflict {
		t.Fatalf("ResetSandbox(stale) = %#v, %v", stale, err)
	}
	invalidRevision := first.Revision
	invalid, err := stack.ConfigurationCommands().ResetSandbox(ctx, principal, appserver.SandboxRequest{WriteBase: appserver.WriteBase{
		OperationID: "sandbox-session-address", SessionID: "session-1", ExpectedRevision: &invalidRevision,
	}})
	if errorcode.CodeOf(err) != errorcode.InvalidArgument || invalid.Outcome != appserver.OutcomeRejected {
		t.Fatalf("ResetSandbox(Session address) = %#v, %v", invalid, err)
	}

	sharedOperationID := "shared-control-ledger"
	created, err := stack.ControlClient().CreateSession(ctx, principal, appserver.CreateSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: sharedOperationID}, PreferredSessionID: "shared-ledger-session",
		WorkspaceKey: stack.Workspace.Key, CWD: stack.Workspace.CWD,
	})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("CreateSession() = %#v, %v", created, err)
	}
	sharedConflict, err := stack.ConfigurationCommands().ResetSandbox(ctx, principal, appserver.SandboxRequest{WriteBase: appserver.WriteBase{
		OperationID: sharedOperationID, ExpectedRevision: &invalidRevision,
	}})
	if !errors.Is(err, appserver.ErrOperationConflict) || sharedConflict.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("ResetSandbox(shared operation ID) = %#v, %v", sharedConflict, err)
	}

	beforeCancelled, err := stack.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	cancelled, err := stack.ConfigurationCommands().SetSandboxBackend(cancelledCtx, principal, appserver.SandboxRequest{
		WriteBase: appserver.WriteBase{OperationID: "sandbox-cancelled", ExpectedRevision: &beforeCancelled},
		Backend:   "auto",
	})
	if err == nil || cancelled.Outcome == appserver.OutcomeCommitted {
		t.Fatalf("SetSandboxBackend(cancelled) = %#v, %v", cancelled, err)
	}
	if afterCancelled, loadErr := stack.ConfigurationRevision(ctx); loadErr != nil || afterCancelled != beforeCancelled {
		t.Fatalf("cancelled revision = %d, %v; want unchanged %d", afterCancelled, loadErr, beforeCancelled)
	}
}

func TestSandboxConfigurationCommandPreservesCanonicalPolicyFields(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	external := newAppConfigStore(filepath.Dir(stack.store.path))
	doc, err := external.Load()
	if err != nil {
		t.Fatal(err)
	}
	networkEnabled := false
	doc.Sandbox = SandboxConfig{
		RequestedType:    "auto",
		HelperPath:       "/canonical/helper",
		WritableRoots:    []string{"/canonical/write"},
		ReadOnlySubpaths: []string{"/canonical/read"},
		NetworkEnabled:   &networkEnabled,
	}
	doc.Runtime.ApprovalMode = "manual"
	saved, err := external.CompareAndSave(ctx, doc.ConfigurationRevision, doc)
	if err != nil {
		t.Fatal(err)
	}
	expected := saved.ConfigurationRevision
	result, err := stack.ConfigurationCommands().SetSandboxBackend(ctx, appserver.Principal{ID: stack.UserID}, appserver.SandboxRequest{
		WriteBase: appserver.WriteBase{OperationID: "sandbox-preserve-canonical-policy", ExpectedRevision: &expected},
		Backend:   "host",
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != expected+1 {
		t.Fatalf("SetSandboxBackend() = %#v, %v", result, err)
	}
	persisted, err := external.Load()
	if err != nil {
		t.Fatal(err)
	}
	wantSandbox := cloneSandboxConfig(saved.Sandbox)
	wantSandbox.RequestedType = "host"
	if !reflect.DeepEqual(persisted.Sandbox, wantSandbox) || persisted.Runtime.ApprovalMode != "manual" {
		t.Fatalf("persisted canonical document = %#v / %#v, want sandbox %#v and manual Runtime", persisted.Sandbox, persisted.Runtime, wantSandbox)
	}
	stack.mu.RLock()
	livePersisted := cloneSandboxConfig(stack.sandboxPersisted)
	liveRevision := stack.sandboxRevision
	stack.mu.RUnlock()
	if !reflect.DeepEqual(livePersisted, wantSandbox) || liveRevision != result.Revision {
		t.Fatalf("live binding = %#v @ %d, want %#v @ %d", livePersisted, liveRevision, wantSandbox, result.Revision)
	}
}

func TestSandboxConfigurationCommandRollsForwardAfterCommittedWriteFault(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	ctx, cancel := context.WithCancel(context.Background())
	fault := errors.New("directory fsync after sandbox CAS failed")
	writeCount := installCommittedConfigSaveFault(t, stack, "fsync", fault)
	committedFault := stack.store.saveHook
	stack.store.saveHook = func(doc AppConfig) error {
		cancel()
		return committedFault(doc)
	}
	expected, err := stack.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := appserver.SandboxRequest{
		WriteBase: appserver.WriteBase{OperationID: "sandbox-committed-write", ExpectedRevision: &expected},
		Backend:   "host",
	}
	result, err := stack.ConfigurationCommands().SetSandboxBackend(ctx, appserver.Principal{ID: stack.UserID}, request)
	if !errors.Is(err, fault) || result.Outcome != appserver.OutcomeUnknown || result.Revision != expected+1 {
		t.Fatalf("SetSandboxBackend(committed fault) = %#v, %v", result, err)
	}
	persisted, loadErr := stack.store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	stack.mu.RLock()
	live := cloneSandboxConfig(stack.sandbox)
	livePersisted := cloneSandboxConfig(stack.sandboxPersisted)
	liveRevision := stack.sandboxRevision
	stack.mu.RUnlock()
	if persisted.ConfigurationRevision != result.Revision || persisted.Sandbox.RequestedType != "host" ||
		live.RequestedType != "host" || !reflect.DeepEqual(livePersisted, persisted.Sandbox) || liveRevision != result.Revision {
		t.Fatalf("committed fault diverged durable/live: persisted=%#v live=%#v bound=%#v@%d result=%#v", persisted, live, livePersisted, liveRevision, result)
	}
	replayed, replayErr := stack.ConfigurationCommands().SetSandboxBackend(context.Background(), appserver.Principal{ID: stack.UserID}, request)
	if replayErr != nil || replayed != result || writeCount() != 1 {
		t.Fatalf("SetSandboxBackend(replay) = %#v, %v writes=%d; want %#v and one write", replayed, replayErr, writeCount(), result)
	}
}

func TestSandboxConfigurationCommandDoesNotGuessRevisionWhenCommittedWriteCannotBeReadBack(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	fault := errors.New("directory fsync after sandbox CAS failed")
	installCommittedConfigSaveFault(t, stack, "fsync", fault)
	committedFault := stack.store.saveHook
	committedPath := stack.store.path + ".committed"
	pathBlocked := false
	t.Cleanup(func() {
		if !pathBlocked {
			return
		}
		_ = os.Remove(stack.store.path)
		_ = os.Rename(committedPath, stack.store.path)
	})
	stack.store.saveHook = func(doc AppConfig) error {
		committedErr := committedFault(doc)
		if !configstore.WriteCommitted(committedErr) {
			return committedErr
		}
		if err := os.Rename(stack.store.path, committedPath); err != nil {
			return errors.Join(committedErr, err)
		}
		if err := os.Mkdir(stack.store.path, 0o700); err != nil {
			_ = os.Rename(committedPath, stack.store.path)
			return errors.Join(committedErr, err)
		}
		pathBlocked = true
		return committedErr
	}
	expected, err := stack.ConfigurationRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := stack.ConfigurationCommands().SetSandboxBackend(context.Background(), appserver.Principal{ID: stack.UserID}, appserver.SandboxRequest{
		WriteBase: appserver.WriteBase{OperationID: "sandbox-committed-write-readback-failed", ExpectedRevision: &expected},
		Backend:   "host",
	})
	if !errors.Is(err, fault) || result.Outcome != appserver.OutcomeUnknown || result.Revision != 0 {
		t.Fatalf("SetSandboxBackend(committed readback failure) = %#v, %v", result, err)
	}
	stack.mu.RLock()
	live := cloneSandboxConfig(stack.sandbox)
	livePersisted := cloneSandboxConfig(stack.sandboxPersisted)
	liveRevision := stack.sandboxRevision
	stack.mu.RUnlock()
	if liveRevision != 0 {
		t.Fatalf("live binding = %#v / %#v @ %d, want unknown revision", live, livePersisted, liveRevision)
	}
	if err := os.Remove(stack.store.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(committedPath, stack.store.path); err != nil {
		t.Fatal(err)
	}
	pathBlocked = false
	persisted, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ConfigurationRevision != expected+1 || persisted.Sandbox.RequestedType != "host" {
		t.Fatalf("persisted committed document = %#v, want host @ %d", persisted, expected+1)
	}
}

func TestSandboxConfigurationCommandDoesNotCompensateCommittedWrite(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	pluginRoot := filepath.Join(t.TempDir(), "malformed-plugin")
	manifestDir := filepath.Join(pluginRoot, ".caelis-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte("invalid-json{"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc.Plugins = []PluginConfig{{ID: "malformed-plugin", Root: pluginRoot, Enabled: true}}
	if err := stack.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	before, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	fault := errors.New("directory fsync after sandbox CAS failed")
	writeCount := installCommittedConfigSaveFault(t, stack, "fsync", fault)
	committedFault := stack.store.saveHook
	attempts := 0
	stack.store.saveHook = func(doc AppConfig) error {
		attempts++
		return committedFault(doc)
	}
	expected := before.ConfigurationRevision
	result, err := stack.ConfigurationCommands().SetSandboxBackend(context.Background(), appserver.Principal{ID: stack.UserID}, appserver.SandboxRequest{
		WriteBase: appserver.WriteBase{OperationID: "sandbox-committed-write-compensated", ExpectedRevision: &expected},
		Backend:   "host",
	})
	if !errors.Is(err, fault) || result.Outcome != appserver.OutcomeUnknown || result.Revision != expected+1 {
		t.Fatalf("SetSandboxBackend(committed warning) = %#v, %v", result, err)
	}
	after, loadErr := stack.store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if after.Sandbox.RequestedType != "host" || after.ConfigurationRevision != result.Revision || attempts != 1 || writeCount() != 1 {
		t.Fatalf("committed document = %#v attempts/writes=%d/%d, want host revision %d", after, attempts, writeCount(), result.Revision)
	}
}

func TestSandboxLifecycleCommandClassifiesPreEffectAndEffectFailures(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc.Sandbox.RequestedType = "windows"
	saved, err := stack.store.CompareAndSave(ctx, doc.ConfigurationRevision, doc)
	if err != nil {
		t.Fatal(err)
	}
	stack.mu.Lock()
	previousExec := stack.exec
	previousOverride := stack.sandboxOverride
	stack.sandbox = configstore.DefaultSandboxConfig(saved.Sandbox)
	stack.sandboxPersisted = cloneSandboxConfig(saved.Sandbox)
	stack.sandboxRevision = saved.ConfigurationRevision
	stack.sandboxOverride = SandboxConfig{}
	stack.exec = nil
	stack.mu.Unlock()
	t.Cleanup(func() {
		stack.mu.Lock()
		stack.exec = previousExec
		stack.sandboxOverride = previousOverride
		stack.mu.Unlock()
	})

	principal := appserver.Principal{ID: stack.UserID}
	expected := saved.ConfigurationRevision
	preEffectRequest := appserver.SandboxRequest{WriteBase: appserver.WriteBase{
		OperationID: "sandbox-lifecycle-pre-effect", ExpectedRevision: &expected,
	}}
	rejected, err := stack.ConfigurationCommands().PrepareSandbox(ctx, principal, preEffectRequest)
	if err == nil || rejected.Outcome != appserver.OutcomeRejected || rejected.Revision != expected || errorcode.CodeOf(err) != errorcode.Unavailable {
		t.Fatalf("PrepareSandbox(pre-effect) = %#v, %v", rejected, err)
	}
	replayedRejected, replayErr := stack.ConfigurationCommands().PrepareSandbox(ctx, principal, preEffectRequest)
	if replayErr != nil || replayedRejected != rejected {
		t.Fatalf("PrepareSandbox(pre-effect replay) = %#v, %v; want %#v", replayedRejected, replayErr, rejected)
	}

	effectErr := errors.New("sandbox prepare effect failed")
	runtime := &sandboxLifecyclePrepareRuntime{
		sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
		prepareErr:                  effectErr,
	}
	stack.mu.Lock()
	stack.exec = runtime
	stack.mu.Unlock()
	effectRequest := appserver.SandboxRequest{WriteBase: appserver.WriteBase{
		OperationID: "sandbox-lifecycle-effect", ExpectedRevision: &expected,
	}}
	unknown, err := stack.ConfigurationCommands().PrepareSandbox(ctx, principal, effectRequest)
	if !errors.Is(err, effectErr) || unknown.Outcome != appserver.OutcomeUnknown || unknown.Revision != expected || runtime.prepareCalls != 1 {
		t.Fatalf("PrepareSandbox(effect failure) = %#v, %v calls=%d", unknown, err, runtime.prepareCalls)
	}
	replayedUnknown, replayErr := stack.ConfigurationCommands().PrepareSandbox(ctx, principal, effectRequest)
	if replayErr != nil || replayedUnknown != unknown || runtime.prepareCalls != 1 {
		t.Fatalf("PrepareSandbox(effect replay) = %#v, %v calls=%d; want %#v and one effect", replayedUnknown, replayErr, runtime.prepareCalls, unknown)
	}
}

func TestSandboxLifecycleCommandUsesCanonicalExternalPolicy(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc.Sandbox.RequestedType = "windows"
	doc.Sandbox.WritableRoots = []string{"/external/policy"}
	saved, err := stack.store.CompareAndSave(ctx, doc.ConfigurationRevision, doc)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &sandboxLifecyclePrepareRuntime{
		sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
	}
	stack.mu.Lock()
	previousExec := stack.exec
	previousOverride := stack.sandboxOverride
	stack.sandboxOverride = SandboxConfig{}
	stack.exec = runtime
	stack.mu.Unlock()
	defer func() {
		stack.mu.Lock()
		stack.exec = previousExec
		stack.sandboxOverride = previousOverride
		stack.mu.Unlock()
	}()
	expected := saved.ConfigurationRevision
	result, err := stack.ConfigurationCommands().PrepareSandbox(ctx, appserver.Principal{ID: stack.UserID}, appserver.SandboxRequest{WriteBase: appserver.WriteBase{
		OperationID: "sandbox-lifecycle-unreconciled", ExpectedRevision: &expected,
	}})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != expected || runtime.prepareCalls != 1 {
		t.Fatalf("PrepareSandbox(canonical policy) = %#v, %v calls=%d", result, err, runtime.prepareCalls)
	}
}

func TestSandboxLifecycleCommandDoesNotHoldConfigurationWriteBoundary(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc.Sandbox.RequestedType = "windows"
	saved, err := stack.store.CompareAndSave(ctx, doc.ConfigurationRevision, doc)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &blockingSandboxPrepareRuntime{
		sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
		started:                     make(chan struct{}),
		release:                     make(chan struct{}),
	}
	stack.mu.Lock()
	previousOverride := stack.sandboxOverride
	stack.sandbox = configstore.DefaultSandboxConfig(saved.Sandbox)
	stack.sandboxPersisted = cloneSandboxConfig(saved.Sandbox)
	stack.sandboxRevision = saved.ConfigurationRevision
	stack.sandboxOverride = SandboxConfig{}
	stack.exec = runtime
	stack.mu.Unlock()
	t.Cleanup(func() {
		stack.mu.Lock()
		stack.sandboxOverride = previousOverride
		stack.mu.Unlock()
	})
	expected := saved.ConfigurationRevision
	principal := appserver.Principal{ID: stack.UserID}
	prepareDone := make(chan appserver.CommandResult, 1)
	prepareErr := make(chan error, 1)
	go func() {
		result, commandErr := stack.ConfigurationCommands().PrepareSandbox(ctx, principal, appserver.SandboxRequest{WriteBase: appserver.WriteBase{
			OperationID: "sandbox-lifecycle-serialized", ExpectedRevision: &expected,
		}})
		prepareDone <- result
		prepareErr <- commandErr
	}()
	select {
	case <-runtime.started:
	case <-time.After(5 * time.Second):
		t.Fatal("PrepareSandbox() did not start")
	}

	setDone := make(chan appserver.CommandResult, 1)
	setErr := make(chan error, 1)
	go func() {
		result, commandErr := stack.ConfigurationCommands().SetSandboxBackend(ctx, principal, appserver.SandboxRequest{
			WriteBase: appserver.WriteBase{OperationID: "sandbox-backend-after-lifecycle", ExpectedRevision: &expected},
			Backend:   "host",
		})
		setDone <- result
		setErr <- commandErr
	}()
	if result, commandErr := <-setDone, <-setErr; commandErr != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != expected+1 {
		t.Fatalf("SetSandboxBackend() = %#v, %v", result, commandErr)
	}
	close(runtime.release)
	if result, commandErr := <-prepareDone, <-prepareErr; commandErr != nil || result.Outcome != appserver.OutcomeCommitted || runtime.prepareCalls != 1 {
		t.Fatalf("PrepareSandbox() = %#v, %v calls=%d", result, commandErr, runtime.prepareCalls)
	}
}

func TestHostConfigurationMutationsDoNotReplaceActiveSessionRuntime(t *testing.T) {
	ctx := context.Background()
	hostRef := session.SessionRef{}
	stack, session := newLocalStateTestStack(t)
	altProfile, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "alt-model",
	})
	if err != nil {
		t.Fatalf("Connect(alt-model) error = %v", err)
	}
	altAlias := altProfile.Backend.Provider.ModelConfigID

	blocking := &blockingRuntime{session: session, release: make(chan struct{})}
	gw, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.Sessions,
		Runtime:  blocking,
		Resolver: blockingResolver{},
	})
	if err != nil {
		t.Fatalf("kernel.New() error = %v", err)
	}
	loaded, _, _, err := stack.sessionRuntimes.activateSessionTracked(ctx, session.SessionID)
	if err != nil {
		t.Fatalf("activateSessionTracked() error = %v", err)
	}
	loaded.instance.gateway = gw
	stack.gateway = gw

	handle, err := stack.currentGateway().BeginTurn(ctx, kernelimpl.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "hold active",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer handle.Handle.Close()
	if got := len(stack.currentGateway().ActiveTurns()); got != 1 {
		t.Fatalf("ActiveTurns() len = %d, want 1", got)
	}
	disconnectConnection := controlagents.Connection{
		ID: "disconnect-acp", Launcher: controlagents.Launcher{Command: writeExternalAgentExecutable(t, t.TempDir(), "disconnect-acp")},
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	doc.ExternalAgents = controlagents.Configuration{
		Connections: []controlagents.Connection{disconnectConnection},
		Agents:      []controlagents.Agent{{ID: "disconnect-agent", ConnectionID: disconnectConnection.ID}},
		Discoveries: []controlagents.DiscoverySnapshot{{ConnectionID: disconnectConnection.ID, SelectedModelID: "opus"}},
	}
	if err := stack.store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	tests := []struct {
		name      string
		run       func() error
		wantError bool
		want      func(*testing.T)
	}{
		{
			name: "connect",
			run: func() error {
				_, err := stack.connectTestModel(ModelConfig{
					Provider: "ollama",
					API:      providers.APIOllama,
					Model:    "blocked-model",
				})
				return err
			},
			want: func(t *testing.T) {
				t.Helper()
				if !stack.lookup.HasAlias("ollama/blocked-model") {
					t.Fatal("Connect() did not update the future activation catalog")
				}
			},
		},
		{
			name: "disconnect ACP Agent",
			run: func() error {
				_, err := disconnectACPCommand(ctx, stack, "disconnect-agent")
				return err
			},
			want: func(t *testing.T) {
				t.Helper()
				doc, err := stack.store.Load()
				if err != nil {
					t.Fatalf("Load() error = %v", err)
				}
				if _, ok := controlagents.LookupAgent(doc.ExternalAgents, "disconnect-agent"); ok {
					t.Fatal("DisconnectACP() retained the future activation Agent")
				}
				if _, ok := controlagents.LookupConnection(doc.ExternalAgents, disconnectConnection.ID); ok {
					t.Fatal("DisconnectACP() retained the future activation Connection")
				}
			},
		},
		{
			name: "use model",
			run: func() error {
				return stack.useTestHostModel(ctx, hostRef, altAlias)
			},
			want: func(t *testing.T) {
				t.Helper()
				if got := stack.lookup.DefaultID(); got != altAlias {
					t.Fatalf("Host default = %q, want future activation default %q", got, altAlias)
				}
			},
		},
		{
			name: "delete model",
			run: func() error {
				return stack.deleteTestHostModel(ctx, hostRef, altAlias)
			},
			want: func(t *testing.T) {
				t.Helper()
				if stack.lookup.HasAlias(altAlias) {
					t.Fatalf("DeleteModel() retained future activation model %q", altAlias)
				}
			},
		},
		{
			name:      "set session mode",
			wantError: true,
			run: func() error {
				current, loadErr := stack.Sessions.Session(ctx, session.SessionRef)
				if loadErr != nil {
					return loadErr
				}
				revision := current.Revision
				_, err := stack.ConfigurationCommands().ConfigureSessionMode(ctx, appserver.Principal{ID: stack.UserID}, appserver.SessionModeRequest{
					WriteBase: appserver.WriteBase{OperationID: "active-turn-session-mode", SessionID: current.SessionID, ExpectedRevision: &revision, ExpectedControllerEpoch: current.Controller.EpochID},
					Mode:      "manual",
				})
				return err
			},
			want: func(t *testing.T) {
				t.Helper()
				state, err := stack.SessionRuntimeState(ctx, session.SessionRef)
				if err != nil {
					t.Fatalf("SessionRuntimeState() error = %v", err)
				}
				if state.SessionMode != "auto-review" {
					t.Fatalf("SessionMode = %q, want unchanged auto-review", state.SessionMode)
				}
			},
		},
		{
			name: "set sandbox backend",
			run: func() error {
				_, _, err := stack.setSandboxBackendAtRevision(ctx, "auto", nil)
				return err
			},
			want: func(t *testing.T) {
				t.Helper()
				if got := stack.SandboxStatus().RequestedBackend; got != "host" {
					t.Fatalf("SandboxStatus().RequestedBackend = %q, want effective future activation host override", got)
				}
			},
		},
		{
			name: "sandbox lifecycle command",
			run: func() error {
				expected, err := stack.ConfigurationRevision(ctx)
				if err != nil {
					return err
				}
				_, err = stack.ConfigurationCommands().ResetSandbox(ctx, appserver.Principal{ID: stack.UserID}, appserver.SandboxRequest{WriteBase: appserver.WriteBase{
					OperationID: "sandbox-lifecycle-active-turn", ExpectedRevision: &expected,
				}})
				return err
			},
			want: func(*testing.T) {},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if tt.wantError {
				if err == nil || (!strings.Contains(err.Error(), "active") && errorcode.CodeOf(err) != errorcode.Conflict) {
					t.Fatalf("%s error = %v, want active Session conflict", tt.name, err)
				}
			} else if err != nil {
				t.Fatalf("%s error = %v, want Host configuration commit", tt.name, err)
			}
			tt.want(t)
			if stack.currentGateway() != gw {
				t.Fatalf("%s replaced the active Host Runtime", tt.name)
			}
		})
	}
	if loaded.instance.lookup.HasAlias("ollama/blocked-model") || !loaded.instance.lookup.HasAlias(altAlias) {
		t.Fatalf("active Session model snapshot changed: blocked=%v alt=%v", loaded.instance.lookup.HasAlias("ollama/blocked-model"), loaded.instance.lookup.HasAlias(altAlias))
	}
	if got := loaded.instance.sandbox.RequestedType; got != "host" {
		t.Fatalf("active Session sandbox = %q, want frozen host", got)
	}

	close(blocking.release)
	for range handle.Handle.ACPEvents() {
	}
}

func TestBuildInitialGatewayRuntimeRejectsInitializedRuntimeBeforePlanLoad(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	pluginRoot := filepath.Join(t.TempDir(), "malformed-plugin")
	manifestDir := filepath.Join(pluginRoot, ".caelis-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(manifestDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte("invalid-json{"), 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	if err := stack.store.Save(AppConfig{
		Plugins: []PluginConfig{{ID: "malformed-plugin", Root: pluginRoot, Enabled: true}},
	}); err != nil {
		t.Fatalf("store.Save() error = %v", err)
	}

	blocking := &blockingRuntime{session: activeSession, release: make(chan struct{})}
	gw, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.Sessions,
		Runtime:  blocking,
		Resolver: blockingResolver{},
	})
	if err != nil {
		t.Fatalf("kernel.New() error = %v", err)
	}
	stack.gateway = gw

	handle, err := stack.currentGateway().BeginTurn(ctx, kernelimpl.BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hold active",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer handle.Handle.Close()
	defer func() {
		close(blocking.release)
		for range handle.Handle.ACPEvents() {
		}
	}()

	err = stack.buildInitialGatewayRuntime(ctx)
	if err == nil {
		t.Fatal("buildInitialGatewayRuntime() error = nil, want initialized Runtime rejection")
	}
	if !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("buildInitialGatewayRuntime() error = %v, want initialized Runtime rejection", err)
	}
	if strings.Contains(err.Error(), "parse enabled plugin") {
		t.Fatalf("buildInitialGatewayRuntime() error = %v, want fail-fast before plugin parsing", err)
	}
}

func TestLoadGatewayBuildPlanInvalidPluginDoesNotMutateStack(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	beforeGateway := stack.gateway
	beforeExec := stack.exec
	beforeMCP := stack.mcpMgr
	beforePlugins := clonePluginConfigs(stack.runtime.Plugins)
	beforeBaseMetadata := cloneMap(stack.runtime.BaseMetadata)

	pluginRoot := filepath.Join(t.TempDir(), "malformed-plugin")
	manifestDir := filepath.Join(pluginRoot, ".caelis-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(manifestDir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte("invalid-json{"), 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	if err := stack.store.Save(AppConfig{
		Plugins: []PluginConfig{{ID: "malformed-plugin", Root: pluginRoot, Enabled: true}},
	}); err != nil {
		t.Fatalf("store.Save() error = %v", err)
	}

	sandboxCfg := stack.sandbox
	_, err := stack.loadGatewayBuildPlan(sandboxCfg, stack.runtime)
	if err == nil {
		t.Fatal("loadGatewayBuildPlan() error = nil, want plugin parse failure")
	}
	if !strings.Contains(err.Error(), "parse enabled plugin") {
		t.Fatalf("loadGatewayBuildPlan() error = %v, want plugin parse failure", err)
	}
	if stack.gateway != beforeGateway {
		t.Fatalf("gateway changed on plan failure: before=%p after=%p", beforeGateway, stack.gateway)
	}
	if stack.exec != beforeExec {
		t.Fatalf("exec changed on plan failure: before=%p after=%p", beforeExec, stack.exec)
	}
	if stack.mcpMgr != beforeMCP {
		t.Fatalf("mcp manager changed on plan failure: before=%p after=%p", beforeMCP, stack.mcpMgr)
	}
	if !reflect.DeepEqual(stack.runtime.Plugins, beforePlugins) {
		t.Fatalf("runtime plugins = %+v, want unchanged %+v", stack.runtime.Plugins, beforePlugins)
	}
	if !reflect.DeepEqual(stack.runtime.BaseMetadata, beforeBaseMetadata) {
		t.Fatalf("runtime base metadata = %+v, want unchanged %+v", stack.runtime.BaseMetadata, beforeBaseMetadata)
	}
}

func TestBuildGatewayRuntimeMCPFailureDoesNotSwapStack(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	beforeGateway := stack.gateway
	beforeExec := stack.exec
	beforeMCP := stack.mcpMgr
	plan, err := stack.loadGatewayBuildPlan(stack.sandbox, stack.runtime)
	if err != nil {
		t.Fatalf("loadGatewayBuildPlan() error = %v", err)
	}
	plan.Plugins.MCPServerSpecs = []plugin.MCPServerSpec{{
		PluginID:  "broken",
		Name:      "server",
		Transport: plugin.MCPTransportStdio,
	}}

	bundle, err := stack.buildGatewayRuntime(plan)
	if err == nil {
		t.Fatal("buildGatewayRuntime() error = nil, want MCP init failure")
	}
	if bundle != nil {
		t.Fatalf("buildGatewayRuntime() bundle = %+v, want nil on error", bundle)
	}
	if !strings.Contains(err.Error(), "failed to initialize MCP servers") {
		t.Fatalf("buildGatewayRuntime() error = %v, want MCP init failure", err)
	}
	if stack.gateway != beforeGateway {
		t.Fatalf("gateway changed on build failure: before=%p after=%p", beforeGateway, stack.gateway)
	}
	if stack.exec != beforeExec {
		t.Fatalf("exec changed on build failure: before=%p after=%p", beforeExec, stack.exec)
	}
	if stack.mcpMgr != beforeMCP {
		t.Fatalf("mcp manager changed on build failure: before=%p after=%p", beforeMCP, stack.mcpMgr)
	}
}

func TestInstallGatewayRuntimeBundleRejectsRuntimeReplacementAndClosesBundle(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	beforeExec := stack.exec
	beforeMCP := stack.mcpMgr

	blocking := &blockingRuntime{session: activeSession, release: make(chan struct{})}
	oldGateway, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.Sessions,
		Runtime:  blocking,
		Resolver: blockingResolver{},
	})
	if err != nil {
		t.Fatalf("kernel.New() error = %v", err)
	}
	stack.gateway = oldGateway

	handle, err := stack.currentGateway().BeginTurn(ctx, kernelimpl.BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hold active",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer handle.Handle.Close()
	defer func() {
		close(blocking.release)
		for range handle.Handle.ACPEvents() {
		}
	}()

	plan, err := stack.loadGatewayBuildPlan(stack.sandbox, stack.runtime)
	if err != nil {
		t.Fatalf("loadGatewayBuildPlan() error = %v", err)
	}
	bundle, err := stack.buildGatewayRuntime(plan)
	if err != nil {
		t.Fatalf("buildGatewayRuntime() error = %v", err)
	}
	if bundle.Gateway == nil || bundle.Engine == nil || bundle.Exec == nil || bundle.MCP == nil {
		t.Fatalf("buildGatewayRuntime() incomplete bundle: %+v", bundle)
	}

	err = stack.installGatewayRuntimeBundle(oldGateway, bundle)
	if err == nil {
		t.Fatal("installGatewayRuntimeBundle() error = nil, want Runtime replacement rejection")
	}
	if !strings.Contains(err.Error(), "refusing to replace") {
		t.Fatalf("installGatewayRuntimeBundle() error = %v, want Runtime replacement rejection", err)
	}
	if stack.gateway != oldGateway {
		t.Fatalf("gateway swapped despite active turn: before=%p after=%p", oldGateway, stack.gateway)
	}
	if stack.exec != beforeExec {
		t.Fatalf("exec swapped despite active turn: before=%p after=%p", beforeExec, stack.exec)
	}
	if stack.mcpMgr != beforeMCP {
		t.Fatalf("mcp manager swapped despite active turn: before=%p after=%p", beforeMCP, stack.mcpMgr)
	}
	if bundle.Gateway != nil || bundle.Engine != nil || bundle.Exec != nil || bundle.MCP != nil {
		t.Fatalf("installGatewayRuntimeBundle() left bundle resources open: %+v", bundle)
	}
}

func TestStackConnectRollsBackOnConfigSaveFailure(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	beforeDefault := stack.lookup.DefaultID()
	stack.mu.RLock()
	beforeRuntime := stack.runtime
	stack.mu.RUnlock()
	poisonConfigStorePath(t, stack)

	_, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "save-failed-model",
	})
	if err == nil {
		t.Fatal("Connect() error = nil, want config save failure")
	}
	if stack.lookup.HasAlias("ollama/save-failed-model") {
		t.Fatal("Connect() left failed model in lookup")
	}
	if got := stack.lookup.DefaultID(); got != beforeDefault {
		t.Fatalf("DefaultModelID() = %q, want %q", got, beforeDefault)
	}
	stack.mu.RLock()
	afterRuntime := stack.runtime
	stack.mu.RUnlock()
	if afterRuntime.Model.ID != beforeRuntime.Model.ID {
		t.Fatalf("runtime model = %q, want %q", afterRuntime.Model.ID, beforeRuntime.Model.ID)
	}
}

func TestStackSetSandboxBackendRollsBackOnConfigSaveFailure(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	before := stack.SandboxStatus()
	beforeGateway := stack.currentGateway()
	poisonConfigStorePath(t, stack)

	_, _, err := stack.setSandboxBackendAtRevision(ctx, "auto", nil)
	if err == nil {
		t.Fatal("SetSandboxBackend() error = nil, want config save failure")
	}
	after := stack.SandboxStatus()
	if after.RequestedBackend != before.RequestedBackend || after.ResolvedBackend != before.ResolvedBackend {
		t.Fatalf("SandboxStatus() = %+v, want rollback to %+v", after, before)
	}
	if afterGateway := stack.currentGateway(); afterGateway != beforeGateway {
		t.Fatalf("currentGateway() changed on save failure: before=%p after=%p", beforeGateway, afterGateway)
	}
}

func TestStackSetSandboxBackendObservesNewerConcurrentSandboxWriter(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	pluginRoot := filepath.Join(t.TempDir(), "malformed-plugin")
	manifestDir := filepath.Join(pluginRoot, ".caelis-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte("invalid-json{"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc.Plugins = []PluginConfig{{ID: "malformed-plugin", Root: pluginRoot, Enabled: true}}
	if err := stack.store.Save(doc); err != nil {
		t.Fatal(err)
	}

	external := newAppConfigStore(filepath.Dir(stack.store.path))
	previousSavedHook := stack.store.savedHook
	injected := false
	stack.store.savedHook = func() {
		if previousSavedHook != nil {
			previousSavedHook()
		}
		if injected {
			return
		}
		injected = true
		concurrent, loadErr := external.Load()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		concurrent.Sandbox.RequestedType = "windows"
		if saveErr := external.Save(concurrent); saveErr != nil {
			t.Fatal(saveErr)
		}
	}

	status, actualRevision, err := stack.setSandboxBackendAtRevision(context.Background(), "host", nil)
	if err != nil {
		t.Fatalf("SetSandboxBackend() error = %v", err)
	}
	persisted, loadErr := external.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if persisted.Sandbox.RequestedType != "windows" {
		t.Fatalf("persisted sandbox = %#v, want concurrent writer value", persisted.Sandbox)
	}
	if actualRevision != persisted.ConfigurationRevision {
		t.Fatalf("SetSandboxBackend() revision = %d, want concurrent revision %d", actualRevision, persisted.ConfigurationRevision)
	}
	if status.RequestedBackend != "host" {
		t.Fatalf("SetSandboxBackend() effective status = %#v, want fixed process override", status)
	}
	if observed := stack.SandboxStatus(); observed.RequestedBackend != "host" || observed.Route != "host" {
		t.Fatalf("SandboxStatus() after command = %#v, want fixed process override", observed)
	}
	stack.mu.RLock()
	observedPolicy := cloneSandboxConfig(stack.sandboxPersisted)
	stack.mu.RUnlock()
	if observedPolicy.RequestedType != "windows" {
		t.Fatalf("observed canonical sandbox = %#v, want concurrent writer policy", observedPolicy)
	}
}

func poisonConfigStorePath(t *testing.T, stack *Stack) {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatalf("WriteFile(blocker) error = %v", err)
	}
	stack.store.path = filepath.Join(blocker, "config.json")
}

type blockingResolver struct{}

func (blockingResolver) ResolveTurn(context.Context, kernelimpl.TurnIntent) (kernelimpl.ResolvedTurn, error) {
	return kernelimpl.ResolvedTurn{RunRequest: agent.RunRequest{}}, nil
}

type blockingRuntime struct {
	session session.Session
	release chan struct{}
}

type blockingSandboxPrepareRuntime struct {
	*sandboxLifecycleTestRuntime
	started      chan struct{}
	release      chan struct{}
	prepareCalls int
}

func (r *blockingSandboxPrepareRuntime) Prepare(ctx context.Context) error {
	r.prepareCalls++
	close(r.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.release:
		return nil
	}
}

func (r *blockingRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{
		Session: r.session,
		Handle:  blockingRunner{release: r.release},
	}, nil
}

func (r *blockingRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{Status: agent.RunLifecycleStatusRunning}, nil
}

type blockingRunner struct {
	release <-chan struct{}
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
