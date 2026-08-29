package gatewayapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelprofile"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestDisconnectACPRemovesSiblingProfilesAndAllBindingsWhileRetainingInstallation(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	installed := writeExternalAgentExecutable(t, t.TempDir(), "shared-acp")
	connection := controlagents.Connection{
		ID: "shared", Name: "Shared", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindManaged, Command: installed},
	}
	doc, err := stack.composition.authorities.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc.ExternalAgents, doc.ModelProfiles = disconnectTestCatalog(connection, "shared", "opus", "sonnet")
	doc.AgentBindings = agentbinding.Configuration{Bindings: []agentbinding.Binding{
		{Handle: agentbinding.HandleBreeze, ProfileID: "acp:shared:opus", Effort: "none"},
		{Handle: agentbinding.HandleOrbit, ProfileID: "acp:shared:sonnet", Effort: "none"},
		{Handle: agentbinding.HandleReviewer, ProfileID: "acp:shared:opus", Effort: "none"},
	}}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}

	candidates, err := stack.Agents().DisconnectCandidates(context.Background())
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
	if result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DisconnectACP() = %#v", result)
	}
	doc, err = stack.composition.authorities.store.Load()
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
	if err != nil || result.Outcome != appserver.OutcomeCommitted || !strings.Contains(result.Detail, fault.Error()) {
		t.Fatalf("DisconnectACP() receipt/error = %#v/%v", result, err)
	}
	if writeCount() != 1 {
		t.Fatalf("DisconnectACP() result/writes = %#v/%d", result, writeCount())
	}
	doc, loadErr := stack.composition.authorities.store.Load()
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
	committedFault := stack.composition.authorities.store.saveHook
	committedPath := stack.composition.authorities.store.path + ".committed-disconnect"
	pathBlocked := false
	restore := func() {
		if !pathBlocked {
			return
		}
		if err := os.Remove(stack.composition.authorities.store.path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(committedPath, stack.composition.authorities.store.path); err != nil {
			t.Fatal(err)
		}
		pathBlocked = false
	}
	t.Cleanup(restore)
	stack.composition.authorities.store.saveHook = func(doc AppConfig) error {
		committedErr := committedFault(doc)
		if !configstore.WriteCommitted(committedErr) {
			return committedErr
		}
		if err := os.Rename(stack.composition.authorities.store.path, committedPath); err != nil {
			return errors.Join(committedErr, err)
		}
		if err := os.Mkdir(stack.composition.authorities.store.path, 0o700); err != nil {
			_ = os.Rename(committedPath, stack.composition.authorities.store.path)
			return errors.Join(committedErr, err)
		}
		pathBlocked = true
		return committedErr
	}
	expected, err := stack.ControlStatus().ConfigurationRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := appserver.DisconnectACPRequest{
		WriteBase: appserver.WriteBase{OperationID: "agent-disconnect-committed-readback", ExpectedRevision: &expected},
		AgentID:   "readback-disconnect",
	}
	result, err := stack.AgentCommands().DisconnectACP(context.Background(), appserver.Principal{ID: stack.composition.authorities.userID}, request)
	if !errors.Is(err, fault) || result.Outcome != appserver.OutcomeUnknown || result.Revision != 0 {
		t.Fatalf("DisconnectACP(committed readback failure) = %#v, %v", result, err)
	}
	restore()
	doc, err := stack.composition.authorities.store.LoadContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := controlagents.LookupAgent(doc.ExternalAgents, "readback-disconnect"); ok {
		t.Fatalf("persisted config retained disconnected Agent: %#v", doc.ExternalAgents)
	}
	replayed, replayErr := stack.AgentCommands().DisconnectACP(context.Background(), appserver.Principal{ID: stack.composition.authorities.userID}, request)
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
	first := &Stack{composition: runtimeComposition{authorities: runtimeHostAuthorities{store: makeStore()}, sessions: sessions}}
	second := &Stack{composition: runtimeComposition{authorities: runtimeHostAuthorities{store: makeStore()}, sessions: sessions}}
	testControlCommandBackend(first)
	testControlCommandBackend(second)
	type outcome struct {
		result externalAgentMutationResult
		err    error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	mutate := func(stack *Stack) {
		defer wait.Done()
		result, _, mutationErr := stack.commandBackend.disconnectACPAtRevision(ctx, "shared", expected)
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
	doc, err := stack.composition.authorities.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc.ExternalAgents, doc.ModelProfiles = disconnectTestCatalog(connection, "rollback", "opus")
	doc.AgentBindings = agentbinding.Configuration{Bindings: []agentbinding.Binding{{
		Handle: agentbinding.HandleZenith, ProfileID: "acp:rollback:opus", Effort: "none",
	}}}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}

	receipt, err := disconnectACPCommand(context.Background(), stack, "rollback")
	if err != nil || receipt.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DisconnectACP() receipt/error = %#v/%v", receipt, err)
	}
	doc, err = stack.composition.authorities.store.Load()
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

func TestDisconnectACPRevokesActivatedSessionRuntimeImmediately(t *testing.T) {
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
	if _, ok := storedACPAgentInfo(activated.instance.ListACPAgents(), "codex"); !ok {
		t.Fatalf("activated Session assembly is missing codex: %#v", activated.instance.ListACPAgents())
	}
	frozen, err := activated.instance.resolveParticipantPlacement(ctx, "acp:codex:default", "none")
	if err != nil {
		t.Fatal(err)
	}

	receipt, err := disconnectACPCommand(ctx, stack, "codex")
	if err != nil || receipt.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DisconnectACP() receipt/error = %#v/%v", receipt, err)
	}
	if _, ok := storedACPAgentInfo(activated.instance.ListACPAgents(), "codex"); ok {
		t.Fatalf("disconnect retained activated Session Agent: %#v", activated.instance.ListACPAgents())
	}
	if _, err := activated.instance.resolveParticipantPlacement(ctx, "acp:codex:default", "none"); err == nil {
		t.Fatalf("activated Session retained disconnected placement %#v", frozen)
	}
	_, err = activated.instance.acpControlPlane.Controllers.Activate(ctx, controller.HandoffRequest{
		SessionRef: active.SessionRef,
		Session:    active,
		Agent:      "codex",
	})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("disconnected Agent registry activation error = %v, want unavailable", err)
	}
	if _, err := stack.composition.resolveParticipantPlacement(ctx, "acp:codex:default", "none"); err == nil {
		t.Fatal("Host placement resolution retained disconnected Agent")
	}
	if err := stack.sessionRuntimes.release(ctx, active.SessionID); err != nil {
		t.Fatal(err)
	}
	refreshed, _, err := stack.sessionRuntimes.activateSession(ctx, active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := storedACPAgentInfo(refreshed.instance.ListACPAgents(), "codex"); ok {
		t.Fatalf("reactivated Session retained disconnected Agent: %#v", refreshed.instance.ListACPAgents())
	}
	if _, err := refreshed.instance.resolveParticipantPlacement(ctx, "acp:codex:default", "none"); err == nil {
		t.Fatal("reactivated Session retained disconnected placement")
	}
}

func TestDisconnectACPRepairsDurableSessionBindingsImmediately(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	persistDisconnectTestAgent(t, stack, "codex")
	ctx := context.Background()
	doc, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.ModelProfiles, err = modelprofile.SelectDefault(doc.ModelProfiles, "acp:codex:default", "none")
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	active, err := stack.composition.sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: stack.composition.authorities.appName, UserID: stack.composition.authorities.userID, Workspace: stack.composition.workspace,
		PreferredSessionID: "activated-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err = stack.composition.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: active.SessionRef,
		Binding:    session.ControllerBinding{Kind: session.ControllerKindACP, ControllerID: "codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = stack.composition.sessions.PutParticipant(ctx, session.PutParticipantRequest{
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
	if err != nil || receipt.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DisconnectACP() receipt/error = %#v/%v", receipt, err)
	}
	doc, err = stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := controlagents.LookupAgent(doc.ExternalAgents, "codex"); ok {
		t.Fatalf("canonical config retained disconnected Agent: %#v", doc.ExternalAgents)
	}
	repaired, err := stack.composition.sessions.Session(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Controller.Kind != session.ControllerKindKernel || repaired.Controller.ControllerID != "sdk-kernel" {
		t.Fatalf("repaired controller = %#v, want kernel fallback; receipt=%#v", repaired.Controller, receipt)
	}
	if len(repaired.Participants) != 0 {
		t.Fatalf("repaired participants = %#v, want disconnected binding removed", repaired.Participants)
	}
	state, err := stack.composition.sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	fallback, ok := modelprofile.Lookup(doc.ModelProfiles, doc.ModelProfiles.DefaultProfileID)
	if !ok || fallback.Backend.Provider == nil {
		t.Fatalf("post-disconnect default profile = %#v", doc.ModelProfiles)
	}
	if model := strings.TrimSpace(kernel.CurrentModelAlias(state)); model != fallback.Backend.Provider.ModelConfigID {
		t.Fatalf("repaired model = %q, want default %q", model, fallback.Backend.Provider.ModelConfigID)
	}
}

func TestDisconnectACPRebindsLoadedControllerToACPConnectedAfterActivation(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	ctx := context.Background()
	active, err := startGatewayAppTestSession(ctx, stack, "dynamic-acp-main")
	if err != nil {
		t.Fatal(err)
	}
	activated, _, err := stack.sessionRuntimes.activateSession(ctx, active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	releaseRuntime, err := stack.sessionRuntimes.acquireRuntimeUse(activated)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRuntime()
	if _, ok := storedACPAgentInfo(activated.instance.ListACPAgents(), "alpha"); ok {
		t.Fatal("test Runtime unexpectedly started with alpha")
	}

	alphaAgents, alphaProfiles := disconnectTestCatalog(testACPControllerConnection(t, stack, "alpha"), "alpha", "remote-model")
	betaAgents, betaProfiles := disconnectTestCatalog(testACPControllerConnection(t, stack, "beta"), "beta", "remote-model")
	doc, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.ExternalAgents = controlagents.NormalizeConfiguration(controlagents.Configuration{
		Connections: append(alphaAgents.Connections, betaAgents.Connections...),
		Agents:      append(alphaAgents.Agents, betaAgents.Agents...),
		Discoveries: append(alphaAgents.Discoveries, betaAgents.Discoveries...),
	})
	doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, append(alphaProfiles.Profiles, betaProfiles.Profiles...)...)
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}

	active = mustCurrentSession(t, stack, active.SessionID)
	selected, err := stack.ConfigurationCommands().UseSessionModel(ctx, appserver.Principal{ID: stack.composition.authorities.userID}, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "select-dynamically-connected-alpha",
			SessionID:               active.SessionID,
			ExpectedRevision:        &active.Revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Model: "acp:alpha:remote-model",
	})
	if err != nil || selected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("UseSessionModel(dynamic alpha) = %#v, %v", selected, err)
	}
	active = mustCurrentSession(t, stack, active.SessionID)
	if active.Controller.Kind != session.ControllerKindACP || active.Controller.AgentName != "alpha" {
		t.Fatalf("selected controller = %#v, want dynamically installed alpha", active.Controller)
	}
	if _, ok := storedACPAgentInfo(activated.instance.ListACPAgents(), "alpha"); !ok {
		t.Fatalf("activated Runtime roster = %#v, want alpha", activated.instance.ListACPAgents())
	}

	doc, err = stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.ModelProfiles, err = modelprofile.SelectDefault(doc.ModelProfiles, "acp:beta:remote-model", "none")
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}

	receipt, err := disconnectACPCommand(ctx, stack, "alpha")
	if err != nil || receipt.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DisconnectACP(alpha) = %#v, %v", receipt, err)
	}
	rebound := mustCurrentSession(t, stack, active.SessionID)
	if rebound.Controller.Kind != session.ControllerKindACP || rebound.Controller.AgentName != "beta" || rebound.Controller.Placement.ProfileID != "acp:beta:remote-model" {
		t.Fatalf("rebound controller = %#v, want beta fallback", rebound.Controller)
	}
	if _, ok := storedACPAgentInfo(activated.instance.ListACPAgents(), "alpha"); ok {
		t.Fatalf("activated Runtime retained alpha: %#v", activated.instance.ListACPAgents())
	}
	if _, ok := storedACPAgentInfo(activated.instance.ListACPAgents(), "beta"); !ok {
		t.Fatalf("activated Runtime did not install beta fallback: %#v", activated.instance.ListACPAgents())
	}
	if _, found, err := activated.instance.ACPControllerStatus(ctx, rebound.SessionRef); err != nil || !found {
		t.Fatalf("beta controller status found=%v err=%v", found, err)
	}
}

func TestDisconnectACPRebindsLoadedControllerToProviderConnectedAfterActivation(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	ctx := context.Background()
	active, err := startGatewayAppTestSession(ctx, stack, "dynamic-provider-fallback")
	if err != nil {
		t.Fatal(err)
	}
	activated, _, err := stack.sessionRuntimes.activateSession(ctx, active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	releaseRuntime, err := stack.sessionRuntimes.acquireRuntimeUse(activated)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRuntime()

	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	configurationRevision, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	connected, err := stack.ConfigurationCommands().ConnectModel(ctx, principal, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "connect-provider-fallback", ExpectedRevision: &configurationRevision},
		Config: appserver.ConnectConfig{
			Provider: "ollama",
			Model:    "provider-after-activation",
		},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel(provider fallback) = %#v, %v", connected, err)
	}
	fallbackConfig, err := stack.composition.lookup.ResolveConfig("ollama/provider-after-activation")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := activated.instance.lookup.Config(fallbackConfig.ID); ok {
		t.Fatal("activated Runtime unexpectedly contained provider connected after activation")
	}
	configurationRevision, err = stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defaulted, err := stack.ConfigurationCommands().UseModel(ctx, principal, appserver.UseModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "default-provider-fallback", ExpectedRevision: &configurationRevision},
		Model:     modelprofile.BuildProviderID(fallbackConfig.ID),
	})
	if err != nil || defaulted.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("UseModel(provider fallback) = %#v, %v", defaulted, err)
	}

	alphaAgents, alphaProfiles := disconnectTestCatalog(testACPControllerConnection(t, stack, "alpha"), "alpha", "remote-model")
	doc, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.ExternalAgents = alphaAgents
	doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, alphaProfiles.Profiles...)
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}

	active = mustCurrentSession(t, stack, active.SessionID)
	selected, err := stack.ConfigurationCommands().UseSessionModel(ctx, principal, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "select-alpha-before-provider-fallback",
			SessionID:               active.SessionID,
			ExpectedRevision:        &active.Revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Model: "acp:alpha:remote-model",
	})
	if err != nil || selected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("UseSessionModel(alpha) = %#v, %v", selected, err)
	}

	receipt, err := disconnectACPCommand(ctx, stack, "alpha")
	if err != nil || receipt.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DisconnectACP(alpha) = %#v, %v", receipt, err)
	}
	rebound := mustCurrentSession(t, stack, active.SessionID)
	if rebound.Controller.Kind != session.ControllerKindKernel {
		t.Fatalf("rebound controller = %#v, want kernel provider fallback", rebound.Controller)
	}
	state, err := stack.composition.sessions.SnapshotState(ctx, rebound.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if model := kernel.CurrentModelAlias(state); model != fallbackConfig.ID {
		t.Fatalf("rebound model = %q, want %q", model, fallbackConfig.ID)
	}
	if configured, err := activated.instance.lookup.ResolveConfig(fallbackConfig.ID); err != nil || configured.ID != fallbackConfig.ID {
		t.Fatalf("activated Runtime provider fallback = %#v, %v", configured, err)
	}
}

func TestDisconnectACPInterruptsActiveControllerTurnBeforeFallback(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	ctx := context.Background()
	active, err := startGatewayAppTestSession(ctx, stack, "active-acp-disconnect")
	if err != nil {
		t.Fatal(err)
	}
	activated, _, err := stack.sessionRuntimes.activateSession(ctx, active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	releaseRuntime, err := stack.sessionRuntimes.acquireRuntimeUse(activated)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRuntime()

	agents, profiles := disconnectTestCatalog(testACPControllerBlockingConnection(t, stack, "alpha"), "alpha", "remote-model")
	doc, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.ExternalAgents = agents
	doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, profiles.Profiles...)
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}

	active = mustCurrentSession(t, stack, active.SessionID)
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	selected, err := stack.ConfigurationCommands().UseSessionModel(ctx, principal, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "select-alpha-for-active-disconnect",
			SessionID:               active.SessionID,
			ExpectedRevision:        &active.Revision,
			ExpectedControllerEpoch: active.Controller.EpochID,
		},
		Model: "acp:alpha:remote-model",
	})
	if err != nil || selected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("UseSessionModel(alpha) = %#v, %v", selected, err)
	}
	prompted, err := stack.ControlClient().Prompt(ctx, principal, appserver.PromptRequest{
		WriteBase: appserver.WriteBase{OperationID: "prompt-blocking-alpha", SessionID: active.SessionID},
		Input:     "hold the ACP controller Turn",
	})
	if err != nil || prompted.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("Prompt(alpha) = %#v, %v", prompted, err)
	}
	if _, ok := activated.instance.currentGateway().ActiveTurn(active.SessionID); !ok {
		t.Fatal("ACP controller Turn was not active before disconnect")
	}

	receipt, err := disconnectACPCommand(ctx, stack, "alpha")
	if err != nil || receipt.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DisconnectACP(alpha) = %#v, %v", receipt, err)
	}
	if _, ok := activated.instance.currentGateway().ActiveTurn(active.SessionID); ok {
		t.Fatal("ACP controller Turn remained active after disconnect")
	}
	rebound := mustCurrentSession(t, stack, active.SessionID)
	if rebound.Controller.Kind != session.ControllerKindKernel {
		t.Fatalf("rebound controller = %#v, want kernel fallback", rebound.Controller)
	}
}

func TestPinnedACPSelectionRollbackPreservesNewerPlacementPublication(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	ctx := context.Background()
	active, err := startGatewayAppTestSession(ctx, stack, "acp-selection-rollback")
	if err != nil {
		t.Fatal(err)
	}
	activated, _, err := stack.sessionRuntimes.activateSession(ctx, active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agents, profiles := disconnectTestCatalog(testACPControllerConnection(t, stack, "alpha"), "alpha", "remote-model")
	doc := before
	doc.ExternalAgents = agents
	doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, profiles.Profiles...)
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	canonical := newPlacementSnapshot(doc)
	frozen, err := controlplacement.ResolveProfile(canonical.placement, "acp:alpha:remote-model", "none")
	if err != nil {
		t.Fatal(err)
	}
	finish, err := activated.instance.beginPinnedACPSelection(ctx, frozen)
	if err != nil {
		t.Fatal(err)
	}

	newer := newPlacementSnapshot(before)
	activated.instance.placementCacheMu.Lock()
	activated.instance.placementCache = newer
	activated.instance.placementCacheGeneration++
	newerGeneration := activated.instance.placementCacheGeneration
	activated.instance.placementCacheMu.Unlock()
	finish(false)

	activated.instance.placementCacheMu.RLock()
	got := activated.instance.placementCache
	gotGeneration := activated.instance.placementCacheGeneration
	activated.instance.placementCacheMu.RUnlock()
	if got != newer || gotGeneration != newerGeneration {
		t.Fatalf("rollback placement/generation = %p/%d, want newer publication %p/%d", got, gotGeneration, newer, newerGeneration)
	}
}

func TestDisconnectACPRepairsSessionToNoConfiguredWithoutFallback(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	ctx := context.Background()
	agents, profiles := disconnectTestCatalog(controlagents.Connection{
		ID: "codex", Launcher: controlagents.Launcher{Command: writeExternalAgentExecutable(t, t.TempDir(), "codex-acp")},
	}, "codex", "default")
	var err error
	profiles, err = modelprofile.SelectDefault(profiles, "acp:codex:default", "none")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc.ExternalAgents = agents
	doc.ModelProfiles = profiles
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	active, err := stack.composition.sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: stack.composition.authorities.appName, UserID: stack.composition.authorities.userID,
		Workspace: stack.composition.workspace, PreferredSessionID: "no-fallback-session",
		Controller: session.ControllerBinding{Kind: session.ControllerKindACP, ControllerID: "codex", AgentName: "codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err = stack.composition.sessions.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &active.Revision,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeConfiguration),
		Update: func(state map[string]any) (map[string]any, error) {
			next := session.CloneState(state)
			if next == nil {
				next = map[string]any{}
			}
			next[kernel.StateCurrentModelAlias] = "stale-provider-model"
			return next, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	receipt, err := disconnectACPCommand(ctx, stack, "codex")
	if err != nil || receipt.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DisconnectACP() receipt/error = %#v/%v", receipt, err)
	}
	repaired, err := stack.composition.sessions.Session(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Controller.Kind != session.ControllerKindKernel || len(repaired.Participants) != 0 {
		t.Fatalf("repaired Session = %#v", repaired)
	}
	state, err := stack.composition.sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if model := kernel.CurrentModelAlias(state); model != "" {
		t.Fatalf("repaired model = %q, want no configured model", model)
	}
	if alias := stack.composition.DefaultModelAlias(); alias != "" {
		t.Fatalf("Host default model = %q, want no configured model", alias)
	}
}

func disconnectACPCommand(ctx context.Context, stack *Stack, agentID string) (appserver.CommandResult, error) {
	snapshot, err := stack.Agents().DisconnectCandidatesSnapshot(ctx)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	revision := snapshot.Revision
	return stack.AgentCommands().DisconnectACP(ctx, appserver.Principal{ID: "test"}, appserver.DisconnectACPRequest{
		WriteBase: appserver.WriteBase{
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
	doc, err := stack.composition.authorities.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	agents, profiles := disconnectTestCatalog(connection, agentID, "default")
	doc.ExternalAgents = agents
	doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, profiles.Profiles...)
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatal(err)
	}
}

func testACPControllerConnection(t *testing.T, stack *Stack, agentID string) controlagents.Connection {
	return testACPControllerConnectionMode(t, stack, agentID, "controller")
}

func testACPControllerBlockingConnection(t *testing.T, stack *Stack, agentID string) controlagents.Connection {
	return testACPControllerConnectionMode(t, stack, agentID, "controller-blocking")
}

func testACPControllerConnectionMode(t *testing.T, stack *Stack, agentID, mode string) controlagents.Connection {
	t.Helper()
	return controlagents.Connection{
		ID:   agentID,
		Name: agentID,
		Launcher: controlagents.Launcher{
			Kind:    controlagents.LaunchKindExecutable,
			Command: os.Args[0],
			Args: []string{
				"-test.run=^TestGatewayACPOnboardingHelperProcess$",
				"--",
				"caelis-onboarding-helper",
				mode,
				filepath.Join(t.TempDir(), agentID+"-started"),
			},
			WorkDir: stack.composition.workspace.CWD,
		},
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
