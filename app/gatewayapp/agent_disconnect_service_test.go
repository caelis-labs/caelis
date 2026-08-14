package gatewayapp

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/modelprofile"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

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
	result, err := disconnectACPCommand(context.Background(), stack, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != controlclient.OutcomeCommitted {
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
	fault := errors.New("directory fsync after rename failed")
	writeCount := installCommittedConfigSaveFault(t, stack, "fsync", fault)

	result, err := disconnectACPCommand(context.Background(), stack, "committed-disconnect")
	if err != nil || result.Outcome != controlclient.OutcomeCommitted || !strings.Contains(result.Detail, fault.Error()) {
		t.Fatalf("DisconnectACP() receipt/error = %#v/%v", result, err)
	}
	if writeCount() != 1 {
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
}

func TestDisconnectACPCommandCachesUnknownWhenCommittedRevisionCannotBeObserved(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	persistDisconnectTestAgent(t, stack, "readback-disconnect")
	fault := errors.New("directory fsync after disconnect CAS failed")
	writeCount := installCommittedConfigSaveFault(t, stack, "fsync", fault)
	committedFault := stack.store.saveHook
	committedPath := stack.store.path + ".committed-disconnect"
	pathBlocked := false
	restore := func() {
		if !pathBlocked {
			return
		}
		if err := os.Remove(stack.store.path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(committedPath, stack.store.path); err != nil {
			t.Fatal(err)
		}
		pathBlocked = false
	}
	t.Cleanup(restore)
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
	request := controlclient.DisconnectACPRequest{
		WriteBase: controlclient.WriteBase{OperationID: "agent-disconnect-committed-readback", ExpectedRevision: &expected},
		AgentID:   "readback-disconnect",
	}
	result, err := stack.AgentCommands().DisconnectACP(context.Background(), controlclient.Principal{ID: stack.UserID}, request)
	if !errors.Is(err, fault) || result.Outcome != controlclient.OutcomeUnknown || result.Revision != 0 {
		t.Fatalf("DisconnectACP(committed readback failure) = %#v, %v", result, err)
	}
	restore()
	doc, err := stack.store.LoadContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := controlagents.LookupAgent(doc.ExternalAgents, "readback-disconnect"); ok {
		t.Fatalf("persisted config retained disconnected Agent: %#v", doc.ExternalAgents)
	}
	replayed, replayErr := stack.AgentCommands().DisconnectACP(context.Background(), controlclient.Principal{ID: stack.UserID}, request)
	if replayErr != nil || replayed != result || writeCount() != 1 {
		t.Fatalf("DisconnectACP(replay) = %#v, %v writes=%d; want %#v and one write", replayed, replayErr, writeCount(), result)
	}
}

func TestDisconnectACPCASAllowsOnlyOneConcurrentHostWriter(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	seed := newAppConfigStore(root)
	doc, err := seed.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	connection := controlagents.Connection{ID: "shared", Launcher: controlagents.Launcher{Command: writeExternalAgentExecutable(t, t.TempDir(), "shared-acp")}}
	doc.ExternalAgents, doc.ModelProfiles = disconnectTestCatalog(connection, "shared", "default")
	if err := seed.Save(doc); err != nil {
		t.Fatal(err)
	}
	doc, err = seed.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expected := doc.ConfigurationRevision

	ready := make(chan struct{}, 1)
	release := make(chan struct{})
	var saveCalls atomic.Int32
	makeStore := func() *appConfigStore {
		store := newAppConfigStore(root)
		store.saveHook = func(AppConfig) error {
			saveCalls.Add(1)
			ready <- struct{}{}
			<-release
			return nil
		}
		return store
	}
	sessions := sessionmemory.NewStore(sessionmemory.Config{})
	first := &Stack{store: makeStore(), Sessions: sessions}
	second := &Stack{store: makeStore(), Sessions: sessions}
	type outcome struct {
		result externalAgentMutationResult
		err    error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	mutate := func(stack *Stack) {
		defer wait.Done()
		result, _, mutationErr := stack.disconnectACPAtRevision(ctx, "shared", expected)
		results <- outcome{result: result, err: mutationErr}
	}
	go mutate(first)
	go mutate(second)
	<-ready
	close(release)
	wait.Wait()
	close(results)

	committed := 0
	conflicts := 0
	for item := range results {
		switch {
		case item.err == nil:
			committed++
			if item.result.Revision != expected+1 {
				t.Fatalf("committed revision = %d, want %d", item.result.Revision, expected+1)
			}
		case errors.Is(item.err, configstore.ErrConfigurationRevisionConflict):
			conflicts++
			if item.result.Revision != expected+1 {
				t.Fatalf("conflict actual revision = %d, want %d", item.result.Revision, expected+1)
			}
		default:
			t.Fatalf("disconnect mutation error = %v", item.err)
		}
	}
	attempts := saveCalls.Load()
	if committed != 1 || conflicts != 1 || attempts < 1 || attempts > 2 {
		t.Fatalf("committed=%d conflicts=%d CAS attempts=%d, want 1/1/(1 or 2)", committed, conflicts, attempts)
	}
	loaded, err := seed.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := controlagents.LookupAgent(loaded.ExternalAgents, "shared"); ok || loaded.ConfigurationRevision != expected+1 {
		t.Fatalf("canonical external Agent config = %#v at revision %d", loaded.ExternalAgents, loaded.ConfigurationRevision)
	}
}

func TestDisconnectACPPersistsProfilesAndBindingsWithoutAssemblyRefresh(t *testing.T) {
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

	receipt, err := disconnectACPCommand(context.Background(), stack, "rollback")
	if err != nil || receipt.Outcome != controlclient.OutcomeCommitted {
		t.Fatalf("DisconnectACP() receipt/error = %#v/%v", receipt, err)
	}
	doc, err = stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := controlagents.LookupAgent(doc.ExternalAgents, "rollback"); ok {
		t.Fatalf("committed disconnect restored external Agent: %#v", doc.ExternalAgents)
	}
	if _, ok := modelprofile.Lookup(doc.ModelProfiles, "acp:rollback:opus"); ok {
		t.Fatalf("committed disconnect restored profile: %#v", doc.ModelProfiles)
	}
	if binding, ok := agentbinding.Lookup(doc.AgentBindings, agentbinding.HandleZenith); ok {
		t.Fatalf("committed disconnect restored binding: %#v", binding)
	}
}

func TestDisconnectACPDoesNotRewriteActivatedSessionRuntime(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	persistDisconnectTestAgent(t, stack, "codex")
	ctx := context.Background()
	active, err := startGatewayAppTestSession(ctx, stack, "activated-before-disconnect")
	if err != nil {
		t.Fatal(err)
	}
	activated, _, err := stack.sessionRuntimes.activateSession(ctx, active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := storedACPAgentInfo(activated.stack.ListACPAgents(), "codex"); !ok {
		t.Fatalf("activated Session assembly is missing codex: %#v", activated.stack.ListACPAgents())
	}
	frozen, err := activated.stack.resolveParticipantPlacement(ctx, "acp:codex:default", "none")
	if err != nil {
		t.Fatal(err)
	}

	receipt, err := disconnectACPCommand(ctx, stack, "codex")
	if err != nil || receipt.Outcome != controlclient.OutcomeCommitted {
		t.Fatalf("DisconnectACP() receipt/error = %#v/%v", receipt, err)
	}
	if _, ok := storedACPAgentInfo(activated.stack.ListACPAgents(), "codex"); !ok {
		t.Fatalf("disconnect rewrote activated Session assembly: %#v", activated.stack.ListACPAgents())
	}
	retained, err := activated.stack.resolveParticipantPlacement(ctx, "acp:codex:default", "none")
	if err != nil {
		t.Fatalf("activated Session placement changed after disconnect: %v", err)
	}
	if retained.Fingerprint != frozen.Fingerprint || retained.ConfigFingerprint != frozen.ConfigFingerprint {
		t.Fatalf("activated placement changed: before=%#v after=%#v", frozen, retained)
	}
	if _, err := stack.resolveParticipantPlacement(ctx, "acp:codex:default", "none"); err == nil {
		t.Fatal("Host placement resolution retained disconnected Agent")
	}
	if err := stack.sessionRuntimes.release(ctx, active.SessionID); err != nil {
		t.Fatal(err)
	}
	refreshed, _, err := stack.sessionRuntimes.activateSession(ctx, active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := storedACPAgentInfo(refreshed.stack.ListACPAgents(), "codex"); ok {
		t.Fatalf("reactivated Session retained disconnected Agent: %#v", refreshed.stack.ListACPAgents())
	}
	if _, err := refreshed.stack.resolveParticipantPlacement(ctx, "acp:codex:default", "none"); err == nil {
		t.Fatal("reactivated Session retained disconnected placement")
	}
}

func TestDisconnectACPDoesNotScanDurableSessionBindings(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	persistDisconnectTestAgent(t, stack, "codex")
	ctx := context.Background()
	active, err := stack.Sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: stack.AppName, UserID: stack.UserID, Workspace: stack.Workspace,
		PreferredSessionID: "activated-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err = stack.Sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: active.SessionRef,
		Binding:    session.ControllerBinding{Kind: session.ControllerKindACP, ControllerID: "codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = stack.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef:       active.SessionRef,
		ExpectedRevision: &active.Revision,
		Binding: session.ParticipantBinding{
			ID: "participant-codex", Kind: session.ParticipantKindACP, AgentName: "codex",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	receipt, err := disconnectACPCommand(ctx, stack, "codex")
	if err != nil || receipt.Outcome != controlclient.OutcomeCommitted {
		t.Fatalf("DisconnectACP() receipt/error = %#v/%v", receipt, err)
	}
	doc, err := stack.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := controlagents.LookupAgent(doc.ExternalAgents, "codex"); ok {
		t.Fatalf("canonical config retained disconnected Agent: %#v", doc.ExternalAgents)
	}
}

func disconnectACPCommand(ctx context.Context, stack *Stack, agentID string) (controlclient.CommandResult, error) {
	snapshot, err := stack.DisconnectCandidatesSnapshot(ctx)
	if err != nil {
		return controlclient.CommandResult{}, err
	}
	revision := snapshot.Revision
	return stack.AgentCommands().DisconnectACP(ctx, controlclient.Principal{ID: "test"}, controlclient.DisconnectACPRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:      "test-agent-disconnect-" + uuid.NewString(),
			ExpectedRevision: &revision,
		},
		AgentID: agentID,
	})
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
	agents, profiles := disconnectTestCatalog(connection, agentID, "default")
	doc.ExternalAgents = agents
	doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, profiles.Profiles...)
	if err != nil {
		t.Fatal(err)
	}
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
