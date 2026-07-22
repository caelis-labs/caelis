package gatewayapp

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/ports/plugin"
)

func TestStackRejectsReconfigureWhileActiveTurn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, session := newLocalStateTestStack(t)
	altProfile, err := stack.Connect(ModelConfig{
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
	stack.gateway = gw

	handle, err := stack.currentGateway().BeginTurn(ctx, gateway.BeginTurnRequest{
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
	acpCommand := writeExternalAgentExecutable(t, t.TempDir(), "active-turn-acp")
	acpRequest := controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand, CommandLine: acpCommand,
		ModelID: "opus", CWD: stack.Workspace.CWD,
	}
	acpConnection, err := stack.resolveACPConnectionLauncher(ctx, acpRequest)
	if err != nil {
		t.Fatalf("resolveACPConnectionLauncher() error = %v", err)
	}
	acpRequest.Discovery = &controlagents.DiscoverySnapshot{
		ConnectionID: acpConnection.ID, LaunchFingerprint: controlagents.LaunchFingerprint(acpConnection.Launcher),
		CWD: stack.Workspace.CWD, SelectedModelID: "opus", Models: []controlagents.RemoteModel{{ID: "opus", Name: "Opus"}},
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
		name string
		run  func() error
		want func(*testing.T)
	}{
		{
			name: "connect",
			run: func() error {
				_, err := stack.Connect(ModelConfig{
					Provider: "ollama",
					API:      providers.APIOllama,
					Model:    "blocked-model",
				})
				return err
			},
			want: func(t *testing.T) {
				t.Helper()
				if stack.lookup.HasAlias("ollama/blocked-model") {
					t.Fatal("Connect() mutated lookup while active turn was running")
				}
			},
		},
		{
			name: "connect ACP Agent",
			run: func() error {
				_, err := stack.ConnectACP(ctx, acpRequest)
				return err
			},
			want: func(t *testing.T) {
				t.Helper()
				doc, err := stack.store.Load()
				if err != nil {
					t.Fatalf("Load() error = %v", err)
				}
				if _, ok := controlagents.LookupConnection(doc.ExternalAgents, acpConnection.ID); ok {
					t.Fatal("ConnectACP() persisted a connection while an active turn was running")
				}
			},
		},
		{
			name: "disconnect ACP Agent",
			run: func() error {
				_, err := stack.DisconnectACP(ctx, "disconnect-agent")
				return err
			},
			want: func(t *testing.T) {
				t.Helper()
				doc, err := stack.store.Load()
				if err != nil {
					t.Fatalf("Load() error = %v", err)
				}
				if _, ok := controlagents.LookupAgent(doc.ExternalAgents, "disconnect-agent"); !ok {
					t.Fatal("DisconnectACP() removed an Agent while an active turn was running")
				}
				if _, ok := controlagents.LookupConnection(doc.ExternalAgents, disconnectConnection.ID); !ok {
					t.Fatal("DisconnectACP() removed a Connection while an active turn was running")
				}
			},
		},
		{
			name: "use model",
			run: func() error {
				return stack.UseModel(ctx, session.SessionRef, altAlias)
			},
			want: func(t *testing.T) {
				t.Helper()
				state, err := stack.SessionRuntimeState(ctx, session.SessionRef)
				if err != nil {
					t.Fatalf("SessionRuntimeState() error = %v", err)
				}
				if state.ModelAlias != "" {
					t.Fatalf("ModelAlias = %q, want unchanged empty state", state.ModelAlias)
				}
			},
		},
		{
			name: "delete model",
			run: func() error {
				return stack.DeleteModel(ctx, session.SessionRef, altAlias)
			},
			want: func(t *testing.T) {
				t.Helper()
				if !stack.lookup.HasAlias(altAlias) {
					t.Fatalf("DeleteModel() removed %q while active turn was running", altAlias)
				}
			},
		},
		{
			name: "set session mode",
			run: func() error {
				_, err := stack.SetSessionMode(ctx, session.SessionRef, "manual")
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
				_, err := stack.SetSandboxBackend(ctx, "auto")
				return err
			},
			want: func(t *testing.T) {
				t.Helper()
				if got := stack.SandboxStatus().RequestedBackend; got != "host" {
					t.Fatalf("SandboxStatus().RequestedBackend = %q, want unchanged host", got)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil {
				t.Fatalf("%s error = nil, want active-turn rejection", tt.name)
			}
			if !strings.Contains(err.Error(), "active") {
				t.Fatalf("%s error = %v, want readable active-turn rejection", tt.name, err)
			}
			tt.want(t)
		})
	}

	close(blocking.release)
	for range handle.Handle.ACPEvents() {
	}
}

func TestRebuildGatewayRejectsActiveTurnBeforePlanLoad(t *testing.T) {
	t.Parallel()

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

	handle, err := stack.currentGateway().BeginTurn(ctx, gateway.BeginTurnRequest{
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

	err = stack.rebuildGateway()
	if err == nil {
		t.Fatal("rebuildGateway() error = nil, want active-turn rejection")
	}
	if !strings.Contains(err.Error(), "active") {
		t.Fatalf("rebuildGateway() error = %v, want active-turn rejection", err)
	}
	if strings.Contains(err.Error(), "parse enabled plugin") {
		t.Fatalf("rebuildGateway() error = %v, want fail-fast before plugin parsing", err)
	}
}

func TestLoadGatewayBuildPlanInvalidPluginDoesNotMutateStack(t *testing.T) {
	t.Parallel()

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

	sandboxCfg := effectiveSandboxConfig(stack.sandbox, stack.Workspace.CWD)
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
	t.Parallel()

	stack, _ := newLocalStateTestStack(t)
	beforeGateway := stack.gateway
	beforeExec := stack.exec
	beforeMCP := stack.mcpMgr
	plan, err := stack.loadGatewayBuildPlan(effectiveSandboxConfig(stack.sandbox, stack.Workspace.CWD), stack.runtime)
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

func TestInstallGatewayRuntimeBundleRejectsLateActiveTurnAndClosesBundle(t *testing.T) {
	t.Parallel()

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

	handle, err := stack.currentGateway().BeginTurn(ctx, gateway.BeginTurnRequest{
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

	plan, err := stack.loadGatewayBuildPlan(effectiveSandboxConfig(stack.sandbox, stack.Workspace.CWD), stack.runtime)
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
		t.Fatal("installGatewayRuntimeBundle() error = nil, want active-turn rejection")
	}
	if !strings.Contains(err.Error(), "active") {
		t.Fatalf("installGatewayRuntimeBundle() error = %v, want active-turn rejection", err)
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
	t.Parallel()

	stack, _ := newLocalStateTestStack(t)
	beforeDefault := stack.DefaultModelID()
	stack.mu.RLock()
	beforeRuntime := stack.runtime
	stack.mu.RUnlock()
	poisonConfigStorePath(t, stack)

	_, err := stack.Connect(ModelConfig{
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
	if got := stack.DefaultModelID(); got != beforeDefault {
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
	t.Parallel()

	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	before := stack.SandboxStatus()
	beforeGateway := stack.currentGateway()
	poisonConfigStorePath(t, stack)

	_, err := stack.SetSandboxBackend(ctx, "auto")
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

func poisonConfigStorePath(t *testing.T, stack *Stack) {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatalf("WriteFile(blocker) error = %v", err)
	}
	stack.store.path = filepath.Join(blocker, "config.json")
}

type blockingResolver struct{}

func (blockingResolver) ResolveTurn(context.Context, gateway.TurnIntent) (gateway.ResolvedTurn, error) {
	return gateway.ResolvedTurn{RunRequest: agent.RunRequest{}}, nil
}

type blockingRuntime struct {
	session session.Session
	release chan struct{}
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
