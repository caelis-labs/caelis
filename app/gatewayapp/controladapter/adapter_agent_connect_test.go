package controladapter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlagents "github.com/caelis-labs/caelis/control/agents"
)

func TestAdapterACPConnectDiscoveryIsReusedForModelsConfigAndPersist(t *testing.T) {
	discoveryCalls := 0
	connectCalls := 0
	driver := &Adapter{stack: &RuntimeStack{
		Session: SessionRuntimeDeps{Workspace: session.WorkspaceRef{CWD: t.TempDir()}},
		Agent: AgentRuntimeDeps{
			DiscoverConnectionFn: func(_ context.Context, req controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error) {
				discoveryCalls++
				return controlagents.DiscoverySnapshot{
					ConnectionID: "claude", LaunchFingerprint: "fingerprint", CWD: req.CWD,
					SelectedModelID: req.ModelID,
					Models:          []controlagents.RemoteModel{{ID: "opus", Name: "Opus"}},
					ConfigOptions: []controlagents.ConfigOption{{
						ID: "reasoning_effort", Name: "Reasoning", Options: []controlagents.ConfigChoice{{Value: "max", Name: "Max"}},
					}},
				}, nil
			},
			ConnectFn: func(_ context.Context, req controlagents.ConnectRequest) (controlagents.ConnectResult, error) {
				connectCalls++
				if req.Discovery == nil || req.Discovery.SelectedModelID != "opus" || len(req.Discovery.Models) != 1 {
					t.Fatalf("ConnectACP discovery = %#v, want cached snapshot", req.Discovery)
				}
				return controlagents.ConnectResult{Agents: []controlagents.Agent{{ID: "opus"}}, Discovery: *req.Discovery}, nil
			},
		},
	}}
	payload := controlagents.EncodeConnectState(controlagents.ConnectState{Agent: "claude", Launcher: controlagents.LauncherChoiceNPX})
	models, err := driver.CompleteSlashArg(context.Background(), "connect-acp-model:"+payload, "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(models) error = %v", err)
	}
	if len(models) != 1 || models[0].Value != "opus" {
		t.Fatalf("model candidates = %#v, want opus", models)
	}
	selectedPayload := controlagents.EncodeConnectState(controlagents.ConnectState{Agent: "claude", Launcher: controlagents.LauncherChoiceNPX, Model: "opus"})
	configs, err := driver.CompleteSlashArg(context.Background(), "connect-acp-config:"+selectedPayload, "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(config) error = %v", err)
	}
	if !slashCandidatesHaveValue(configs, "default") || !slashCandidatesHaveValue(configs, "reasoning_effort=max") {
		t.Fatalf("config candidates = %#v, want default and reasoning", configs)
	}
	if discoveryCalls != 2 {
		t.Fatalf("discovery calls = %d, want catalog and selected-model temporary Sessions", discoveryCalls)
	}
	if _, err := driver.ConnectACP(context.Background(), controlagents.ConnectRequest{
		AdapterID: "claude", Launcher: controlagents.LauncherChoiceNPX, ModelID: "opus",
	}); err != nil {
		t.Fatalf("ConnectACP() error = %v", err)
	}
	if connectCalls != 1 {
		t.Fatalf("connect calls = %d, want one", connectCalls)
	}
	if len(driver.acpDiscoveries) != 0 {
		t.Fatalf("discovery cache after connect = %#v, want endpoint entries cleared", driver.acpDiscoveries)
	}
}

func TestAdapterACPDiscoveryCacheExpires(t *testing.T) {
	calls := 0
	driver := &Adapter{stack: &RuntimeStack{
		Session: SessionRuntimeDeps{Workspace: session.WorkspaceRef{CWD: t.TempDir()}},
		Agent: AgentRuntimeDeps{DiscoverConnectionFn: func(_ context.Context, req controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error) {
			calls++
			return controlagents.DiscoverySnapshot{ConnectionID: "claude", CWD: req.CWD, Models: []controlagents.RemoteModel{{ID: "opus"}}}, nil
		}},
	}}
	req := controlagents.ConnectRequest{AdapterID: "claude", Launcher: controlagents.LauncherChoiceNPX}
	if _, err := driver.DiscoverACPConnection(context.Background(), req); err != nil {
		t.Fatalf("DiscoverACPConnection(first) error = %v", err)
	}
	key := acpDiscoveryRequestKey(controlagents.ConnectRequest{
		AdapterID: "claude", Launcher: controlagents.LauncherChoiceNPX, CWD: driver.WorkspaceDir(),
	})
	entry := driver.acpDiscoveries[key]
	entry.cachedAt = time.Now().Add(-acpDiscoveryCacheTTL - time.Second)
	driver.acpDiscoveries[key] = entry
	if _, err := driver.DiscoverACPConnection(context.Background(), req); err != nil {
		t.Fatalf("DiscoverACPConnection(after expiry) error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("discovery calls = %d, want expired cache rediscovered", calls)
	}
}

func TestAdapterACPConnectDoesNotUseExpiredDiscovery(t *testing.T) {
	var connectedDiscovery *controlagents.DiscoverySnapshot
	driver := &Adapter{stack: &RuntimeStack{
		Session: SessionRuntimeDeps{Workspace: session.WorkspaceRef{CWD: t.TempDir()}},
		Agent: AgentRuntimeDeps{
			DiscoverConnectionFn: func(_ context.Context, req controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error) {
				return controlagents.DiscoverySnapshot{
					ConnectionID: "claude", CWD: req.CWD, SelectedModelID: req.ModelID,
					Models: []controlagents.RemoteModel{{ID: "opus"}},
				}, nil
			},
			ConnectFn: func(_ context.Context, req controlagents.ConnectRequest) (controlagents.ConnectResult, error) {
				connectedDiscovery = req.Discovery
				return controlagents.ConnectResult{Agents: []controlagents.Agent{{ID: "opus"}}}, nil
			},
		},
	}}
	req := controlagents.ConnectRequest{AdapterID: "claude", Launcher: controlagents.LauncherChoiceNPX, ModelID: "opus"}
	if _, err := driver.DiscoverACPConnection(context.Background(), req); err != nil {
		t.Fatalf("DiscoverACPConnection() error = %v", err)
	}
	key := acpDiscoveryRequestKey(controlagents.ConnectRequest{
		AdapterID: "claude", Launcher: controlagents.LauncherChoiceNPX, ModelID: "opus", CWD: driver.WorkspaceDir(),
	})
	entry := driver.acpDiscoveries[key]
	entry.cachedAt = time.Now().Add(-acpDiscoveryCacheTTL - time.Second)
	driver.acpDiscoveries[key] = entry
	if _, err := driver.ConnectACP(context.Background(), req); err != nil {
		t.Fatalf("ConnectACP() error = %v", err)
	}
	if connectedDiscovery != nil {
		t.Fatalf("ConnectACP() discovery = %#v, want expired snapshot omitted", connectedDiscovery)
	}
}

func TestCompleteConnectACPLaunchersDistinguishesGlobalAndManaged(t *testing.T) {
	candidates := completeConnectACPLaunchers("claude", "", 10)
	if len(candidates) == 0 || candidates[0].Value != "managed" || !strings.Contains(candidates[0].Display, "Recommended") || !strings.Contains(candidates[0].Detail, "safe to cancel or retry") {
		t.Fatalf("launcher candidates = %#v, want productized managed recommendation first", candidates)
	}
	for _, want := range []string{"npx", "global", "managed"} {
		if !slashCandidatesHaveValue(candidates, want) {
			t.Fatalf("launcher candidates = %#v, want %q", candidates, want)
		}
	}
	if slashCandidatesHaveValue(candidates, "path") {
		t.Fatalf("launcher candidates = %#v, global install must not be mislabeled as PATH", candidates)
	}
}

func TestCompleteConnectACPRestoresNativeAgentCatalog(t *testing.T) {
	candidates := completeConnectACPAgents("", 20)
	for _, want := range []string{"codex", "claude", "grok", "codefree-o", "opencode", "custom"} {
		if !slashCandidatesHaveValue(candidates, want) {
			t.Fatalf("ACP Agent candidates = %#v, want %q", candidates, want)
		}
	}
	for _, agent := range []string{"grok", "codefree-o", "opencode"} {
		launchers := completeConnectACPLaunchers(agent, "", 10)
		if len(launchers) != 1 || launchers[0].Value != "installed" {
			t.Fatalf("%s launchers = %#v, want installed only", agent, launchers)
		}
	}
}

func TestCompleteConnectACPPreservesCaseSensitiveCustomCommand(t *testing.T) {
	driver := &Adapter{stack: &RuntimeStack{
		Session: SessionRuntimeDeps{Workspace: session.WorkspaceRef{CWD: t.TempDir()}},
		Agent: AgentRuntimeDeps{DiscoverConnectionFn: func(_ context.Context, req controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error) {
			if req.CommandLine != "/Tmp/MyACP --stdio" {
				t.Fatalf("discovery command line = %q, want case preserved", req.CommandLine)
			}
			return controlagents.DiscoverySnapshot{Models: []controlagents.RemoteModel{{ID: "model"}}}, nil
		}},
	}}
	payload := controlagents.EncodeConnectState(controlagents.ConnectState{
		Agent: "custom", Launcher: controlagents.LauncherChoiceCommand, CommandLine: "/Tmp/MyACP --stdio",
	})
	if _, err := driver.CompleteSlashArg(context.Background(), "connect-acp-model:"+payload, "", 10); err != nil {
		t.Fatalf("CompleteSlashArg() error = %v", err)
	}
}

func TestAdapterDisconnectACPUsesControlOwnedRosterCapability(t *testing.T) {
	t.Parallel()

	disconnectCalls := 0
	driver := &Adapter{stack: &RuntimeStack{Agent: AgentRuntimeDeps{
		DisconnectCandidatesFn: func(context.Context) ([]controlagents.DisconnectCandidate, error) {
			return []controlagents.DisconnectCandidate{{AgentID: "codex", Name: "Codex", ConnectionID: "codex", LastOnConnection: true}}, nil
		},
		DisconnectFn: func(_ context.Context, agentID string) (controlagents.DisconnectResult, error) {
			disconnectCalls++
			if agentID != "codex" {
				t.Fatalf("agentID = %q, want codex", agentID)
			}
			return controlagents.DisconnectResult{Agent: controlagents.Agent{ID: "codex"}, ConnectionID: "codex", ConnectionRemoved: true}, nil
		},
	}}}
	driver.acpDiscoveries = map[string]acpDiscoveryCacheEntry{
		"codex":  {snapshot: controlagents.DiscoverySnapshot{ConnectionID: "codex"}, cachedAt: time.Now()},
		"claude": {snapshot: controlagents.DiscoverySnapshot{ConnectionID: "claude"}, cachedAt: time.Now()},
	}

	candidates, err := driver.DisconnectCandidates(context.Background())
	if err != nil {
		t.Fatalf("DisconnectCandidates() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].AgentID != "codex" {
		t.Fatalf("DisconnectCandidates() = %#v", candidates)
	}
	sources, err := driver.CompleteSlashArg(context.Background(), "connect", "", 10)
	if err != nil || !slashCandidatesHaveValue(sources, "disconnect") {
		t.Fatalf("CompleteSlashArg(connect) = %#v, err=%v, want disconnect entry", sources, err)
	}
	agents, err := driver.CompleteSlashArg(context.Background(), "connect-disconnect-agent", "", 10)
	if err != nil || len(agents) != 1 || agents[0].Value != "codex" || agents[0].Display != "/codex" {
		t.Fatalf("CompleteSlashArg(disconnect Agent) = %#v, err=%v", agents, err)
	}
	confirm, err := driver.CompleteSlashArg(context.Background(), "connect-disconnect-confirm:codex", "", 10)
	if err != nil || len(confirm) != 1 || confirm[0].Value != "confirm" || !strings.Contains(confirm[0].Detail, "installed adapter") {
		t.Fatalf("CompleteSlashArg(disconnect confirm) = %#v, err=%v", confirm, err)
	}
	result, err := driver.DisconnectACP(context.Background(), "codex")
	if err != nil {
		t.Fatalf("DisconnectACP() error = %v", err)
	}
	if disconnectCalls != 1 || !result.ConnectionRemoved {
		t.Fatalf("DisconnectACP() result = %#v calls=%d", result, disconnectCalls)
	}
	if _, ok := driver.acpDiscoveries["codex"]; ok {
		t.Fatal("DisconnectACP() retained discovery cache for released Connection")
	}
	if _, ok := driver.acpDiscoveries["claude"]; !ok {
		t.Fatal("DisconnectACP() removed unrelated discovery cache")
	}
}

func TestAdapterDisconnectACPMapsControlOwnedCurrentControllerError(t *testing.T) {
	t.Parallel()

	disconnectCalls := 0
	driver := &Adapter{
		stack: &RuntimeStack{Agent: AgentRuntimeDeps{
			DisconnectFn: func(context.Context, string) (controlagents.DisconnectResult, error) {
				disconnectCalls++
				return controlagents.DisconnectResult{}, &controlagents.AgentInUseError{AgentID: "codex", SessionID: "parent"}
			},
		}},
		session: session.Session{
			SessionRef: session.SessionRef{SessionID: "parent"},
			Controller: session.ControllerBinding{Kind: session.ControllerKindACP, AgentName: "codex"},
		},
		hasSession: true,
	}

	if _, err := driver.DisconnectACP(context.Background(), "codex"); err == nil || !strings.Contains(err.Error(), "this task") {
		t.Fatal("DisconnectACP() error = nil for current controller")
	}
	if disconnectCalls != 1 {
		t.Fatalf("DisconnectACP() calls = %d, want Control-owned safety check", disconnectCalls)
	}
}
