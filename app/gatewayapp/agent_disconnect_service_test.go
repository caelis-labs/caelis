package gatewayapp

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	internalcontrolclient "github.com/caelis-labs/caelis/internal/controlclient"
)

type oneSessionPerPageService struct {
	session.Service
}

func (s oneSessionPerPageService) ListSessions(ctx context.Context, req session.ListSessionsRequest) (session.SessionList, error) {
	req.Limit = 1
	return s.Service.ListSessions(ctx, req)
}

func TestDisconnectACPPreservesSharedConnectionAndRetainsInstallation(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	installed := writeAgentRosterExecutable(t, t.TempDir(), "shared-acp")
	connection := controlagents.Connection{
		ID: "shared", Name: "Shared", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindManaged, Command: installed},
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	doc.AgentRoster = controlagents.Configuration{
		Connections: []controlagents.Connection{connection},
		Agents: []controlagents.Agent{
			{ID: "opus", Backing: controlagents.AgentBacking{ConnectionID: connection.ID}, Defaults: controlagents.SessionOptions{ModelID: "opus"}},
			{ID: "sonnet", Backing: controlagents.AgentBacking{ConnectionID: connection.ID}, Defaults: controlagents.SessionOptions{ModelID: "sonnet"}},
		},
		Discoveries: []controlagents.DiscoverySnapshot{
			{ConnectionID: connection.ID, SelectedModelID: "opus"},
			{ConnectionID: connection.ID, SelectedModelID: "sonnet"},
		},
	}
	if err := stack.store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	candidates, err := stack.DisconnectCandidates(context.Background())
	if err != nil {
		t.Fatalf("DisconnectCandidates() error = %v", err)
	}
	if len(candidates) != 2 || candidates[0].AgentID != "opus" || candidates[0].SiblingCount != 1 || candidates[0].LastOnConnection {
		t.Fatalf("DisconnectCandidates() = %#v", candidates)
	}
	first, err := stack.DisconnectACP(context.Background(), "opus")
	if err != nil {
		t.Fatalf("DisconnectACP(opus) error = %v", err)
	}
	if first.ConnectionRemoved {
		t.Fatalf("DisconnectACP(opus) = %#v, want shared Connection retained", first)
	}
	doc, err = stack.store.Load()
	if err != nil {
		t.Fatalf("Load(after opus) error = %v", err)
	}
	if _, ok := controlagents.LookupConnection(doc.AgentRoster, connection.ID); !ok || len(doc.AgentRoster.Discoveries) != 1 || doc.AgentRoster.Discoveries[0].SelectedModelID != "sonnet" {
		t.Fatalf("roster after first disconnect = %#v", doc.AgentRoster)
	}

	last, err := stack.DisconnectACP(context.Background(), "sonnet")
	if err != nil {
		t.Fatalf("DisconnectACP(sonnet) error = %v", err)
	}
	if !last.ConnectionRemoved {
		t.Fatalf("DisconnectACP(sonnet) = %#v, want final Connection released", last)
	}
	doc, err = stack.store.Load()
	if err != nil {
		t.Fatalf("Load(after sonnet) error = %v", err)
	}
	if len(doc.AgentRoster.Agents) != 0 || len(doc.AgentRoster.Connections) != 0 || len(doc.AgentRoster.Discoveries) != 0 {
		t.Fatalf("final roster = %#v, want connection-owned state removed", doc.AgentRoster)
	}
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("disconnect removed managed adapter installation %q: %v", installed, err)
	}
}

func TestDisconnectACPRollsBackPersistedRosterWhenAssemblyRefreshFails(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	connection := controlagents.Connection{ID: "rollback", Launcher: controlagents.Launcher{Command: writeAgentRosterExecutable(t, t.TempDir(), "rollback-acp")}}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	doc.AgentRoster = controlagents.Configuration{
		Connections: []controlagents.Connection{connection},
		Agents: []controlagents.Agent{{
			ID: "opus", Backing: controlagents.AgentBacking{ConnectionID: connection.ID}, Defaults: controlagents.SessionOptions{ModelID: "opus"},
		}},
		Discoveries: []controlagents.DiscoverySnapshot{{ConnectionID: connection.ID, SelectedModelID: "opus"}},
	}
	if err := stack.store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	wantErr := errors.New("refresh failed")
	refreshCalls := 0
	stack.refreshConfiguredAgentsHook = func() error {
		refreshCalls++
		if refreshCalls == 1 {
			return wantErr
		}
		return nil
	}
	if _, err := stack.DisconnectACP(context.Background(), "opus"); !errors.Is(err, wantErr) {
		t.Fatalf("DisconnectACP() error = %v, want %v", err, wantErr)
	}
	if refreshCalls != 2 {
		t.Fatalf("refresh calls = %d, want failed apply plus rollback refresh", refreshCalls)
	}
	doc, err = stack.store.Load()
	if err != nil {
		t.Fatalf("Load(after rollback) error = %v", err)
	}
	if _, ok := controlagents.LookupAgent(doc.AgentRoster, "opus"); !ok {
		t.Fatalf("rollback did not restore Agent: %#v", doc.AgentRoster)
	}
	if _, ok := controlagents.LookupConnection(doc.AgentRoster, connection.ID); !ok || len(doc.AgentRoster.Discoveries) != 1 {
		t.Fatalf("rollback did not restore connection state: %#v", doc.AgentRoster)
	}
}

func TestDisconnectACPRejectsRecoverableControllerBindingAcrossAllSessionPages(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	persistDisconnectTestAgent(t, stack, "codex")
	ctx := context.Background()
	bound, err := stack.Sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: stack.AppName, UserID: stack.UserID, Workspace: stack.Workspace, PreferredSessionID: "bound-task",
	})
	if err != nil {
		t.Fatalf("StartSession(bound) error = %v", err)
	}
	if _, err := stack.Sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: bound.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "codex",
		},
	}); err != nil {
		t.Fatalf("BindController() error = %v", err)
	}
	if _, err := stack.Sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: stack.AppName, UserID: stack.UserID, Workspace: stack.Workspace, PreferredSessionID: "newer-unbound-task",
	}); err != nil {
		t.Fatalf("StartSession(unbound) error = %v", err)
	}
	stack.Sessions = oneSessionPerPageService{Service: stack.Sessions}

	_, err = stack.DisconnectACP(ctx, "codex")
	var inUse *controlagents.AgentInUseError
	if !errors.As(err, &inUse) || inUse.SessionID != bound.SessionID {
		t.Fatalf("DisconnectACP() error = %v, want bound Session %q", err, bound.SessionID)
	}
	doc, loadErr := stack.store.Load()
	if loadErr != nil {
		t.Fatalf("Load(after rejection) error = %v", loadErr)
	}
	if _, ok := controlagents.LookupAgent(doc.AgentRoster, "codex"); !ok {
		t.Fatalf("DisconnectACP() removed bound Agent: %#v", doc.AgentRoster)
	}
}

func TestDisconnectACPIgnoresClosedHistoricalControllerBinding(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	persistDisconnectTestAgent(t, stack, "codex")
	ctx := context.Background()
	bound, err := stack.Sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: stack.AppName, UserID: stack.UserID, Workspace: stack.Workspace, PreferredSessionID: "closed-task",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	bound, err = stack.Sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: bound.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "codex", AgentName: "codex",
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}
	if _, err := internalcontrolclient.CloseSession(ctx, stack.Sessions, bound, "test completed"); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}

	if _, err := stack.DisconnectACP(ctx, "codex"); err != nil {
		t.Fatalf("DisconnectACP() error = %v for closed historical binding", err)
	}
}

func TestDisconnectACPHoldsRosterMutationGateThroughRuntimeRefresh(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	persistDisconnectTestAgent(t, stack, "codex")
	attempted := make(chan struct{})
	acquired := make(chan struct{})
	stack.refreshConfiguredAgentsHook = func() error {
		go func() {
			close(attempted)
			stack.agentRosterMu.RLock()
			defer stack.agentRosterMu.RUnlock()
			close(acquired)
		}()
		<-attempted
		select {
		case <-acquired:
			t.Fatal("controller binding gate acquired during roster refresh")
		default:
		}
		return nil
	}

	if _, err := stack.DisconnectACP(context.Background(), "codex"); err != nil {
		t.Fatalf("DisconnectACP() error = %v", err)
	}
	<-acquired
}

func persistDisconnectTestAgent(t *testing.T, stack *Stack, agentID string) {
	t.Helper()
	connection := controlagents.Connection{
		ID: agentID,
		Launcher: controlagents.Launcher{
			Command: writeAgentRosterExecutable(t, t.TempDir(), agentID+"-acp"),
		},
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	doc.AgentRoster = controlagents.Configuration{
		Connections: []controlagents.Connection{connection},
		Agents: []controlagents.Agent{{
			ID: agentID, Backing: controlagents.AgentBacking{ConnectionID: connection.ID}, Defaults: controlagents.SessionOptions{ModelID: "default"},
		}},
		Discoveries: []controlagents.DiscoverySnapshot{{ConnectionID: connection.ID, SelectedModelID: "default"}},
	}
	if err := stack.store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
}
