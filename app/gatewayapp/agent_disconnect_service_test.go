package gatewayapp

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelprofile"
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

func TestDisconnectACPRemovesSiblingProfilesAndRetainsInstallation(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	installed := writeExternalAgentExecutable(t, t.TempDir(), "shared-acp")
	connection := controlagents.Connection{
		ID: "shared", Name: "Shared", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindManaged, Command: installed},
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc.ExternalAgents, doc.ModelProfiles = disconnectTestCatalog(connection, "shared", "opus", "sonnet")
	doc.AgentBindings = agentbinding.Configuration{Bindings: []agentbinding.Binding{
		{Handle: agentbinding.HandleBreeze, ProfileID: "acp:shared:opus", Effort: "none"},
		{Handle: agentbinding.HandleOrbit, ProfileID: "acp:shared:sonnet", Effort: "none"},
	}}
	if err := stack.store.Save(doc); err != nil {
		t.Fatal(err)
	}

	candidates, err := stack.DisconnectCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].AgentID != "shared" || !candidates[0].LastOnConnection {
		t.Fatalf("DisconnectCandidates() = %#v", candidates)
	}
	result, err := stack.DisconnectACP(context.Background(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	if !result.ConnectionRemoved {
		t.Fatalf("DisconnectACP() = %#v", result)
	}
	doc, err = stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.ExternalAgents.Agents) != 0 || len(doc.ExternalAgents.Connections) != 0 || len(doc.ModelProfiles.Profiles) != 0 || len(doc.AgentBindings.Bindings) != 0 {
		t.Fatalf("post-disconnect config = %#v", doc)
	}
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("disconnect removed managed adapter installation %q: %v", installed, err)
	}
}

func TestDisconnectACPRollsForwardAfterCommittedConfigWriteFault(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	persistDisconnectTestAgent(t, stack, "committed-disconnect")
	if err := stack.refreshConfiguredAgentsFromStore(); err != nil {
		t.Fatal(err)
	}
	if _, ok := storedACPAgentInfo(stack.ListACPAgents(), "committed-disconnect"); !ok {
		t.Fatalf("runtime assembly is missing seeded ACP Agent: %#v", stack.ListACPAgents())
	}
	fault := errors.New("directory fsync after rename failed")
	writeCount := installCommittedConfigSaveFault(t, stack, "fsync", fault)

	result, err := stack.DisconnectACP(context.Background(), "committed-disconnect")
	requireCommittedConfigWriteError(t, err, fault)
	if result.Agent.ID != "committed-disconnect" || writeCount() != 1 {
		t.Fatalf("DisconnectACP() result/writes = %#v/%d", result, writeCount())
	}
	doc, loadErr := stack.store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := controlagents.LookupAgent(doc.ExternalAgents, "committed-disconnect"); ok {
		t.Fatalf("committed config retained disconnected Agent: %#v", doc.ExternalAgents)
	}
	if _, ok := modelprofile.Lookup(doc.ModelProfiles, "acp:committed-disconnect:default"); ok {
		t.Fatalf("committed config retained disconnected ModelProfile: %#v", doc.ModelProfiles)
	}
	if _, ok := storedACPAgentInfo(stack.ListACPAgents(), "committed-disconnect"); ok {
		t.Fatalf("runtime assembly retained disconnected ACP Agent: %#v", stack.ListACPAgents())
	}
}

func TestDisconnectACPRollsBackProfilesAndBindingsWhenAssemblyRefreshFails(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	connection := controlagents.Connection{ID: "rollback", Launcher: controlagents.Launcher{Command: writeExternalAgentExecutable(t, t.TempDir(), "rollback-acp")}}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc.ExternalAgents, doc.ModelProfiles = disconnectTestCatalog(connection, "rollback", "opus")
	doc.AgentBindings = agentbinding.Configuration{Bindings: []agentbinding.Binding{{
		Handle: agentbinding.HandleZenith, ProfileID: "acp:rollback:opus", Effort: "none",
	}}}
	if err := stack.store.Save(doc); err != nil {
		t.Fatal(err)
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
	if _, err := stack.DisconnectACP(context.Background(), "rollback"); !errors.Is(err, wantErr) {
		t.Fatalf("DisconnectACP() error = %v", err)
	}
	doc, err = stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := controlagents.LookupAgent(doc.ExternalAgents, "rollback"); !ok {
		t.Fatalf("rollback did not restore external Agent: %#v", doc.ExternalAgents)
	}
	if _, ok := modelprofile.Lookup(doc.ModelProfiles, "acp:rollback:opus"); !ok {
		t.Fatalf("rollback did not restore profile: %#v", doc.ModelProfiles)
	}
	if binding, ok := agentbinding.Lookup(doc.AgentBindings, agentbinding.HandleZenith); !ok || binding.ProfileID != "acp:rollback:opus" {
		t.Fatalf("rollback did not restore binding: %#v", binding)
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
		t.Fatal(err)
	}
	if _, err := stack.Sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: bound.SessionRef,
		Binding:    session.ControllerBinding{Kind: session.ControllerKindACP, ControllerID: "codex"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := stack.Sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: stack.AppName, UserID: stack.UserID, Workspace: stack.Workspace, PreferredSessionID: "newer-unbound-task",
	}); err != nil {
		t.Fatal(err)
	}
	stack.Sessions = oneSessionPerPageService{Service: stack.Sessions}

	_, err = stack.DisconnectACP(ctx, "codex")
	var inUse *controlagents.AgentInUseError
	if !errors.As(err, &inUse) || inUse.SessionID != bound.SessionID {
		t.Fatalf("DisconnectACP() error = %v, want bound Session %q", err, bound.SessionID)
	}
	doc, loadErr := stack.store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := controlagents.LookupAgent(doc.ExternalAgents, "codex"); !ok {
		t.Fatalf("DisconnectACP() removed bound Agent: %#v", doc.ExternalAgents)
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
		t.Fatal(err)
	}
	bound, err = stack.Sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: bound.SessionRef,
		Binding:    session.ControllerBinding{Kind: session.ControllerKindACP, ControllerID: "codex", AgentName: "codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := internalcontrolclient.CloseSession(ctx, stack.Sessions, bound, "test completed"); err != nil {
		t.Fatal(err)
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
			stack.assemblyMutationMu.RLock()
			defer stack.assemblyMutationMu.RUnlock()
			close(acquired)
		}()
		<-attempted
		select {
		case <-acquired:
			t.Fatal("controller binding gate acquired during external Agent refresh")
		default:
		}
		return nil
	}
	if _, err := stack.DisconnectACP(context.Background(), "codex"); err != nil {
		t.Fatal(err)
	}
	<-acquired
}

func persistDisconnectTestAgent(t *testing.T, stack *Stack, agentID string) {
	t.Helper()
	connection := controlagents.Connection{
		ID:       agentID,
		Launcher: controlagents.Launcher{Command: writeExternalAgentExecutable(t, t.TempDir(), agentID+"-acp")},
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc.ExternalAgents, doc.ModelProfiles = disconnectTestCatalog(connection, agentID, "default")
	if err := stack.store.Save(doc); err != nil {
		t.Fatal(err)
	}
}

func disconnectTestCatalog(connection controlagents.Connection, agentID string, modelIDs ...string) (controlagents.Configuration, modelprofile.Configuration) {
	agents := controlagents.Configuration{
		Connections: []controlagents.Connection{connection},
		Agents:      []controlagents.Agent{{ID: agentID, ConnectionID: connection.ID}},
	}
	profiles := modelprofile.Configuration{}
	for _, modelID := range modelIDs {
		agents.Discoveries = append(agents.Discoveries, controlagents.DiscoverySnapshot{
			ConnectionID: connection.ID, LaunchFingerprint: controlagents.LaunchFingerprint(connection.Launcher), SelectedModelID: modelID,
		})
		profiles.Profiles = append(profiles.Profiles, modelprofile.ModelProfile{
			ID: "acp:" + agentID + ":" + modelID, DisplayName: agentID + " " + modelID,
			Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{AgentID: agentID, RemoteModelID: modelID}},
			Effort:  modelprofile.EffortCapability{DefaultEffort: "none", Choices: []modelprofile.EffortChoice{{Canonical: "none"}}},
		})
	}
	return agents, profiles
}
