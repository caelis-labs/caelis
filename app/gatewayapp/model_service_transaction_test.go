package gatewayapp

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controldelegation "github.com/caelis-labs/caelis/control/delegation"
	"github.com/caelis-labs/caelis/ports/gateway"
)

func TestConnectRollsBackLookupRuntimeAndStoreWhenAgentRefreshFails(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	originalID := stack.lookup.DefaultID()
	originalRuntimeModel := stack.runtime.Model.ID
	wantErr := errors.New("refresh failed")
	stack.refreshConfiguredAgentsHook = func() error { return wantErr }

	_, err := stack.Connect(ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "new-model",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Connect() error = %v, want %v", err, wantErr)
	}
	if got := stack.lookup.DefaultID(); got != originalID {
		t.Fatalf("lookup default = %q, want rollback to %q", got, originalID)
	}
	if got := stack.runtime.Model.ID; got != originalRuntimeModel {
		t.Fatalf("runtime model = %q, want rollback to %q", got, originalRuntimeModel)
	}
	doc, loadErr := stack.store.Load()
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if doc.Models.DefaultID != "" && doc.Models.DefaultID != originalID {
		t.Fatalf("stored default = %q, want rollback to %q", doc.Models.DefaultID, originalID)
	}
	for _, cfg := range doc.Models.Configs {
		if strings.Contains(cfg.ID, "new-model") {
			t.Fatalf("stored configs contain failed model %#v", cfg)
		}
	}
	for _, agent := range controlagents.ListAgents(doc.AgentRoster) {
		if strings.Contains(agent.Backing.ModelAlias, "new-model") {
			t.Fatalf("stored roster contains failed model Agent %#v", agent)
		}
	}
}

func TestConnectModelsPersistsBatchAtomicallyAndKeepsExistingDefault(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	originalDefaultID := stack.lookup.DefaultID()
	modelIDs, err := stack.ConnectModels([]ModelConfig{
		{Provider: "ollama", API: providers.APIOllama, Model: "batch-first"},
		{Provider: "ollama", API: providers.APIOllama, Model: "batch-second"},
	})
	if err != nil {
		t.Fatalf("ConnectModels() error = %v", err)
	}
	if len(modelIDs) != 2 || stack.lookup.DefaultID() != originalDefaultID || stack.runtime.Model.ID != originalDefaultID {
		t.Fatalf("batch ids/default/runtime = %#v/%q/%q", modelIDs, stack.lookup.DefaultID(), stack.runtime.Model.ID)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	found := map[string]bool{}
	for _, cfg := range doc.Models.Configs {
		found[cfg.Model] = true
	}
	if !found["batch-first"] || !found["batch-second"] || doc.Models.DefaultID != originalDefaultID {
		t.Fatalf("persisted batch = models:%#v default:%q", found, doc.Models.DefaultID)
	}
	rosterModels := map[string]bool{}
	for _, agent := range controlagents.ListAgents(doc.AgentRoster) {
		rosterModels[agent.Backing.ModelAlias] = true
	}
	if !rosterModels[modelIDs[0]] || !rosterModels[modelIDs[1]] {
		t.Fatalf("persisted model-backed Agents = %#v, want %#v", rosterModels, modelIDs)
	}

	before := stack.lookup.Snapshot()
	_, err = stack.ConnectModels([]ModelConfig{
		{Provider: "ollama", API: providers.APIOllama, Model: "should-rollback"},
		{Model: "invalid-without-provider"},
	})
	if err == nil {
		t.Fatal("ConnectModels(invalid batch) error = nil")
	}
	after := stack.lookup.Snapshot()
	if !reflect.DeepEqual(after, before) || stack.lookup.HasAlias("ollama/should-rollback") {
		t.Fatalf("invalid batch leaked into lookup: before=%#v after=%#v", before, after)
	}
}

func TestConnectModelsSelectsFirstModelWhenNoModelExists(t *testing.T) {
	stack, err := newGatewayAppTestStack(t, Config{
		StoreDir:     t.TempDir(),
		WorkspaceKey: t.TempDir(),
		WorkspaceCWD: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	t.Cleanup(func() { _ = stack.Close() })

	modelIDs, err := stack.ConnectModels([]ModelConfig{
		{Provider: "ollama", API: providers.APIOllama, Model: "first"},
		{Provider: "ollama", API: providers.APIOllama, Model: "second"},
	})
	if err != nil {
		t.Fatalf("ConnectModels() error = %v", err)
	}
	if len(modelIDs) != 2 || stack.lookup.DefaultID() != modelIDs[0] || stack.runtime.Model.ID != modelIDs[0] {
		t.Fatalf("batch ids/default/runtime = %#v/%q/%q, want first model active", modelIDs, stack.lookup.DefaultID(), stack.runtime.Model.ID)
	}
}

func TestDeleteModelAtomicallyRemovesModelBackedAgent(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	modelID, err := stack.Connect(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "delete-agent-model"})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	agentID := modelBackedAgentIDForTest(t, stack, modelID)
	if _, err := stack.Delegation().BindDelegation(ctx, controldelegation.BindRequest{
		Profile: controldelegation.ProfileZenith, AgentID: agentID, ReasoningEffort: "high",
	}); err != nil {
		t.Fatalf("BindDelegation() error = %v", err)
	}
	if err := stack.DeleteModel(ctx, activeSession.SessionRef, modelID); err != nil {
		t.Fatalf("DeleteModel() error = %v", err)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, agent := range controlagents.ListAgents(doc.AgentRoster) {
		if agent.Backing.ModelAlias == modelID {
			t.Fatalf("deleted model still has roster Agent %#v", agent)
		}
	}
	if stack.lookup.HasAlias(modelID) {
		t.Fatalf("deleted model %q remains in lookup", modelID)
	}
	if binding, ok := controldelegation.LookupBinding(doc.Delegation, controldelegation.ProfileZenith); !ok || binding.Target != controldelegation.TargetSelf {
		t.Fatalf("Zenith binding after model deletion = %#v, ok=%v, want self", binding, ok)
	}
}

func TestDeleteModelRejectsRecoverableControllerBinding(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	modelID, err := stack.Connect(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "bound-model"})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	agentID := ""
	for _, rosterAgent := range controlagents.ListAgents(doc.AgentRoster) {
		if rosterAgent.Backing.ModelAlias == modelID {
			agentID = rosterAgent.ID
			break
		}
	}
	if agentID == "" {
		t.Fatalf("model-backed Agent missing for %q", modelID)
	}
	if _, err := stack.Sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: agentID, AgentName: agentID,
		},
	}); err != nil {
		t.Fatalf("BindController() error = %v", err)
	}

	err = stack.DeleteModel(ctx, activeSession.SessionRef, modelID)
	var inUse *controlagents.AgentInUseError
	if !errors.As(err, &inUse) || inUse.AgentID != agentID || inUse.SessionID != activeSession.SessionID {
		t.Fatalf("DeleteModel() error = %v, want AgentInUseError for %q/%q", err, agentID, activeSession.SessionID)
	}
	if !stack.lookup.HasAlias(modelID) {
		t.Fatalf("DeleteModel() removed bound model %q", modelID)
	}
}

func TestDeleteModelRestoresModelBackedAgentWhenRefreshFails(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	modelID, err := stack.Connect(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "delete-rollback-model"})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	agentID := modelBackedAgentIDForTest(t, stack, modelID)
	if _, err := stack.Delegation().BindDelegation(ctx, controldelegation.BindRequest{
		Profile: controldelegation.ProfileZenith, AgentID: agentID,
	}); err != nil {
		t.Fatalf("BindDelegation() error = %v", err)
	}
	wantErr := errors.New("refresh failed")
	stack.refreshConfiguredAgentsHook = func() error { return wantErr }
	if err := stack.DeleteModel(ctx, activeSession.SessionRef, modelID); !errors.Is(err, wantErr) {
		t.Fatalf("DeleteModel() error = %v, want %v", err, wantErr)
	}
	if !stack.lookup.HasAlias(modelID) {
		t.Fatalf("rollback did not restore model %q", modelID)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	found := false
	for _, agent := range controlagents.ListAgents(doc.AgentRoster) {
		found = found || agent.Backing.ModelAlias == modelID
	}
	if !found {
		t.Fatalf("rollback did not restore roster Agent for %q: %#v", modelID, doc.AgentRoster.Agents)
	}
	if binding, ok := controldelegation.LookupBinding(doc.Delegation, controldelegation.ProfileZenith); !ok || binding.Target != controldelegation.TargetAgent || binding.AgentID != agentID {
		t.Fatalf("rollback did not restore Zenith binding = %#v, ok=%v", binding, ok)
	}
}

func modelBackedAgentIDForTest(t *testing.T, stack *Stack, modelID string) string {
	t.Helper()
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, agent := range controlagents.ListAgents(doc.AgentRoster) {
		if agent.Backing.ModelAlias == modelID {
			return agent.ID
		}
	}
	t.Fatalf("model-backed Agent missing for %q", modelID)
	return ""
}

func TestUseModelRollsBackConfigWhenSessionStateUpdateFails(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	originalID := stack.lookup.DefaultID()
	nextID, err := stack.Connect(ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "next-model",
	})
	if err != nil {
		t.Fatalf("Connect(next) error = %v", err)
	}
	if err := stack.UseModel(ctx, activeSession.SessionRef, originalID); err != nil {
		t.Fatalf("UseModel(original) error = %v", err)
	}
	wantErr := errors.New("state update failed")
	stack.Sessions = &failingUpdateSessionService{Service: stack.Sessions, err: wantErr}

	err = stack.UseModel(ctx, activeSession.SessionRef, nextID)
	if !errors.Is(err, wantErr) {
		t.Fatalf("UseModel() error = %v, want %v", err, wantErr)
	}
	if got := stack.lookup.DefaultID(); got != originalID {
		t.Fatalf("lookup default = %q, want rollback to %q", got, originalID)
	}
	state, stateErr := stack.Sessions.SnapshotState(ctx, activeSession.SessionRef)
	if stateErr != nil {
		t.Fatalf("SnapshotState() error = %v", stateErr)
	}
	if got, _ := state[gateway.StateCurrentModelAlias].(string); got != originalID {
		t.Fatalf("session model state = %q, want %q", got, originalID)
	}
}

func TestDeleteModelRestoresLiveAgentAssemblyWhenSessionStateUpdateFails(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	modelID, err := stack.Connect(ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "delete-state-rollback-model",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	var refreshAgentPresence []bool
	stack.refreshConfiguredAgentsHook = func() error {
		doc, loadErr := stack.store.Load()
		if loadErr != nil {
			return loadErr
		}
		found := false
		for _, agent := range controlagents.ListAgents(doc.AgentRoster) {
			found = found || agent.Backing.ModelAlias == modelID
		}
		refreshAgentPresence = append(refreshAgentPresence, found)
		return nil
	}
	wantErr := errors.New("state update failed")
	stack.Sessions = &failOnceUpdateSessionService{Service: stack.Sessions, err: wantErr}

	err = stack.DeleteModel(ctx, activeSession.SessionRef, modelID)
	if !errors.Is(err, wantErr) {
		t.Fatalf("DeleteModel() error = %v, want %v", err, wantErr)
	}
	if want := []bool{false, true}; !reflect.DeepEqual(refreshAgentPresence, want) {
		t.Fatalf("Agent presence during refreshes = %#v, want %#v", refreshAgentPresence, want)
	}
	if !stack.lookup.HasAlias(modelID) {
		t.Fatalf("rollback did not restore model %q", modelID)
	}
}

type failingUpdateSessionService struct {
	session.Service
	err error
}

func (s *failingUpdateSessionService) UpdateState(context.Context, session.UpdateStateRequest) (session.Session, error) {
	return session.Session{}, s.err
}

type failOnceUpdateSessionService struct {
	session.Service
	err    error
	failed bool
}

func (s *failOnceUpdateSessionService) UpdateState(ctx context.Context, req session.UpdateStateRequest) (session.Session, error) {
	if !s.failed {
		s.failed = true
		return session.Session{}, s.err
	}
	return s.Service.UpdateState(ctx, req)
}
