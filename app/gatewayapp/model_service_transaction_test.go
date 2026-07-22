package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/ports/gateway"
)

func TestConnectStoresProviderAPIKeyBehindOpaqueReference(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	secret := "sk-connect-secret"
	profile, err := stack.Connect(ModelConfig{
		Provider: "openai", API: providers.APIOpenAI, Model: "gpt-test", BaseURL: "https://api.example/v1", Token: secret, PersistToken: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	modelID := profile.Backend.Provider.ModelConfigID
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	var configured ModelConfig
	for _, candidate := range doc.Models.Configs {
		if candidate.ID == modelID {
			configured = candidate
			break
		}
	}
	var endpointProfile ProviderEndpointConfig
	for _, candidate := range doc.Models.ProviderEndpoints {
		if candidate.ID == configured.ProviderEndpointID {
			endpointProfile = candidate
			break
		}
	}
	if endpointProfile.CredentialRef == "" || !strings.HasPrefix(endpointProfile.CredentialRef, "apikey:") || endpointProfile.Token != "" || endpointProfile.TokenEnv != "" || endpointProfile.PersistToken {
		t.Fatalf("persisted provider credential = %#v", endpointProfile)
	}
	raw, err := os.ReadFile(stack.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("config contains plaintext key: %s", raw)
	}
	got, err := stack.apiKeyCredentials.Get(context.Background(), endpointProfile.CredentialRef)
	if err != nil || got != secret {
		t.Fatalf("credential Get() = %q, %v", got, err)
	}
	hydrated, err := stack.lookup.ResolveConfig(modelID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.lookup.ResolveModelConfig(context.Background(), hydrated, 0); err != nil {
		t.Fatalf("ResolveModelConfig() error = %v", err)
	}
}

func TestConnectRollsBackNewProviderCredentialWhenConfigSaveFails(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	configured := modelconfig.NormalizeConfig(ModelConfig{
		Provider: "openai", API: providers.APIOpenAI, Model: "gpt-rollback", BaseURL: "https://rollback.example/v1", Token: "secret",
	})
	ref := credentialstore.BuildReference(configured.Provider, configured.ProviderEndpointID)
	stack.store.saveHook = func(AppConfig) error { return errors.New("save failed") }
	if _, err := stack.Connect(configured); err == nil || !strings.Contains(err.Error(), "save failed") {
		t.Fatalf("Connect() error = %v", err)
	}
	if _, err := stack.apiKeyCredentials.Get(context.Background(), ref); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back credential Get() error = %v", err)
	}
}

func TestConnectRollsForwardCredentialAfterCommittedConfigWriteFault(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	configured := modelconfig.NormalizeConfig(ModelConfig{
		Provider: "openai", API: providers.APIOpenAI, Model: "gpt-committed", BaseURL: "https://committed.example/v1", Token: "committed-secret",
	})
	ref := credentialstore.BuildReference(configured.Provider, configured.ProviderEndpointID)
	fault := errors.New("directory fsync failed")
	invalidations := 0
	stack.store.savedHook = func() { invalidations++ }
	stack.store.saveHook = func(doc AppConfig) error {
		doc = configstore.Normalize(doc)
		if err := configstore.Validate(doc); err != nil {
			return err
		}
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		return atomicWriteFile(stack.store.path, data, 0o600, atomicWriteOps{
			fsyncDir: func(string) error { return fault },
		})
	}

	profiles, err := stack.ConnectModels([]ModelConfig{configured})
	if !errors.Is(err, fault) || !configstore.WriteCommitted(err) {
		t.Fatalf("ConnectModels() error = %v, want committed %v", err, fault)
	}
	if len(profiles) != 1 || invalidations != 1 {
		t.Fatalf("ConnectModels() profiles/invalidations = %d/%d, want 1/1", len(profiles), invalidations)
	}
	if got, err := stack.apiKeyCredentials.Get(context.Background(), ref); err != nil || got != "committed-secret" {
		t.Fatalf("credential after committed config write = %q, %v", got, err)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, endpoint := range doc.Models.ProviderEndpoints {
		found = found || endpoint.CredentialRef == ref
	}
	if !found {
		t.Fatalf("committed config does not reference credential %q: %#v", ref, doc.Models.ProviderEndpoints)
	}
	if !stack.lookup.HasAlias(profiles[0].Backend.Provider.ModelConfigID) {
		t.Fatalf("committed model %q missing from live lookup", profiles[0].Backend.Provider.ModelConfigID)
	}
}

func TestUseModelRollsForwardAfterCommittedConfigWriteFault(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	profile, err := stack.Connect(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "use-committed"})
	if err != nil {
		t.Fatal(err)
	}
	modelID := profile.Backend.Provider.ModelConfigID
	fault := errors.New("directory fsync after rename failed")
	writeCount := installCommittedConfigSaveFault(t, stack, "fsync", fault)

	err = stack.UseModel(ctx, activeSession.SessionRef, modelID)
	requireCommittedConfigWriteError(t, err, fault)
	if writeCount() != 1 {
		t.Fatalf("config writes = %d, want one committed roll-forward write", writeCount())
	}
	doc, loadErr := stack.store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if doc.Models.DefaultID != modelID || stack.lookup.DefaultID() != modelID || stack.runtime.Model.ID != modelID {
		t.Fatalf("model defaults diverged after committed write: disk=%q lookup=%q runtime=%q", doc.Models.DefaultID, stack.lookup.DefaultID(), stack.runtime.Model.ID)
	}
	state, stateErr := stack.Sessions.SnapshotState(ctx, activeSession.SessionRef)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if got := gateway.CurrentModelAlias(state); got != modelID {
		t.Fatalf("Session model alias = %q, want %q", got, modelID)
	}
}

func TestConnectRollsBackLookupProfilesAndRuntimeWhenRefreshFails(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	originalID := stack.lookup.DefaultID()
	originalRuntimeModel := stack.runtime.Model.ID
	wantErr := errors.New("refresh failed")
	stack.refreshConfiguredAgentsHook = func() error { return wantErr }
	if _, err := stack.Connect(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "new-model"}); !errors.Is(err, wantErr) {
		t.Fatalf("Connect() error = %v", err)
	}
	if stack.lookup.DefaultID() != originalID || stack.runtime.Model.ID != originalRuntimeModel {
		t.Fatalf("lookup/runtime were not rolled back: %q/%q", stack.lookup.DefaultID(), stack.runtime.Model.ID)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range doc.ModelProfiles.Profiles {
		if strings.Contains(profile.DisplayName, "new-model") {
			t.Fatalf("failed profile persisted: %#v", profile)
		}
	}
}

func TestConnectModelsPersistsStandardProfilesAtomicallyAndKeepsExistingDefault(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	originalDefaultID := stack.lookup.DefaultID()
	profiles, err := stack.ConnectModels([]ModelConfig{
		{Provider: "ollama", API: providers.APIOllama, Model: "batch-first"},
		{Provider: "ollama", API: providers.APIOllama, Model: "batch-second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || stack.lookup.DefaultID() != originalDefaultID || stack.runtime.Model.ID != originalDefaultID {
		t.Fatalf("batch profiles/default/runtime = %#v/%q/%q", profiles, stack.lookup.DefaultID(), stack.runtime.Model.ID)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if _, ok := modelprofile.Lookup(doc.ModelProfiles, profile.ID); !ok {
			t.Fatalf("profile %q was not persisted", profile.ID)
		}
	}
	if len(doc.ExternalAgents.Agents) != 0 {
		t.Fatalf("provider connect created synthetic Agents: %#v", doc.ExternalAgents.Agents)
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

func TestConnectModelsSelectsFirstProfileWhenNoModelExists(t *testing.T) {
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceKey: t.TempDir(), WorkspaceCWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	profiles, err := stack.ConnectModels([]ModelConfig{
		{Provider: "ollama", API: providers.APIOllama, Model: "first"},
		{Provider: "ollama", API: providers.APIOllama, Model: "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstModelID := profiles[0].Backend.Provider.ModelConfigID
	if len(profiles) != 2 || stack.lookup.DefaultID() != firstModelID || stack.runtime.Model.ID != firstModelID {
		t.Fatalf("profiles/default/runtime = %#v/%q/%q", profiles, stack.lookup.DefaultID(), stack.runtime.Model.ID)
	}
}

func TestDeleteModelRemovesProviderProfileAndOrdinaryBindings(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	profile, err := stack.Connect(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "delete-profile-model",
		ReasoningMode: "effort", ReasoningLevels: []string{"high"}, ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.AgentBindings().BindAgentBinding(ctx, agentbinding.Binding{
		Handle: agentbinding.HandleZenith, ProfileID: profile.ID, Effort: "high",
	}); err != nil {
		t.Fatal(err)
	}
	if err := stack.DeleteModel(ctx, activeSession.SessionRef, profile.Backend.Provider.ModelConfigID); err != nil {
		t.Fatal(err)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := modelprofile.Lookup(doc.ModelProfiles, profile.ID); ok {
		t.Fatalf("deleted ModelProfile remains: %#v", doc.ModelProfiles)
	}
	if _, ok := agentbinding.Lookup(doc.AgentBindings, agentbinding.HandleZenith); ok {
		t.Fatalf("deleted profile binding remains: %#v", doc.AgentBindings)
	}
}

func TestDeleteModelRollsForwardAfterCommittedConfigWriteFault(t *testing.T) {
	for _, stage := range []string{"chmod", "fsync"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			stack, activeSession := newLocalStateTestStack(t)
			profile, err := stack.Connect(ModelConfig{
				Provider: "ollama", API: providers.APIOllama, Model: "delete-committed-" + stage,
			})
			if err != nil {
				t.Fatal(err)
			}
			modelID := profile.Backend.Provider.ModelConfigID
			if err := stack.UseModel(ctx, activeSession.SessionRef, modelID); err != nil {
				t.Fatal(err)
			}
			if _, err := stack.AgentBindings().BindAgentBinding(ctx, agentbinding.Binding{
				Handle: agentbinding.HandleZenith, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort,
			}); err != nil {
				t.Fatal(err)
			}
			if _, ok := agentConfigForToolTest(stack.runtime.Assembly.Agents, string(agentbinding.HandleZenith)); !ok {
				t.Fatalf("runtime assembly is missing bound %q before deletion", agentbinding.HandleZenith)
			}

			fault := errors.New(stage + " after rename failed")
			writeCount := installCommittedConfigSaveFault(t, stack, stage, fault)
			err = stack.DeleteModel(ctx, activeSession.SessionRef, modelID)
			requireCommittedConfigWriteError(t, err, fault)
			if got := writeCount(); got != 1 {
				t.Fatalf("config writes = %d, want one committed roll-forward write", got)
			}

			doc, loadErr := stack.store.Load()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if _, ok := modelprofile.Lookup(doc.ModelProfiles, profile.ID); ok {
				t.Fatalf("committed deletion retained profile %q", profile.ID)
			}
			if _, ok := agentbinding.Lookup(doc.AgentBindings, agentbinding.HandleZenith); ok {
				t.Fatalf("committed deletion retained binding %q", agentbinding.HandleZenith)
			}
			if stack.lookup.HasAlias(modelID) || stack.runtime.Model.ID != stack.lookup.DefaultID() {
				t.Fatalf("lookup/runtime diverged after committed deletion: has=%v runtime=%q default=%q", stack.lookup.HasAlias(modelID), stack.runtime.Model.ID, stack.lookup.DefaultID())
			}
			if _, ok := agentConfigForToolTest(stack.runtime.Assembly.Agents, string(agentbinding.HandleZenith)); ok {
				t.Fatalf("runtime assembly retained deleted binding %q", agentbinding.HandleZenith)
			}
			state, stateErr := stack.Sessions.SnapshotState(ctx, activeSession.SessionRef)
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			if got := gateway.CurrentModelAlias(state); got != "" {
				t.Fatalf("Session retained deleted model alias %q", got)
			}
		})
	}
}

func TestDeleteModelRollsBackAfterPreCommitConfigWriteFault(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	profile, err := stack.Connect(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "delete-precommit"})
	if err != nil {
		t.Fatal(err)
	}
	modelID := profile.Backend.Provider.ModelConfigID
	previousRuntimeID := stack.runtime.Model.ID
	fault := errors.New("rename failed")
	stack.store.saveHook = func(AppConfig) error { return fault }

	err = stack.DeleteModel(ctx, activeSession.SessionRef, modelID)
	if !errors.Is(err, fault) || configstore.WriteCommitted(err) {
		t.Fatalf("DeleteModel() error = %v, want uncommitted %v", err, fault)
	}
	doc, loadErr := stack.store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := modelprofile.Lookup(doc.ModelProfiles, profile.ID); !ok || !stack.lookup.HasAlias(modelID) || stack.runtime.Model.ID != previousRuntimeID {
		t.Fatalf("pre-commit rollback diverged: profile=%v lookup=%v runtime=%q want=%q", ok, stack.lookup.HasAlias(modelID), stack.runtime.Model.ID, previousRuntimeID)
	}
}

func TestDeleteModelRejectsSystemBoundProfile(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	profile, err := stack.Connect(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "system-bound-model",
		ReasoningMode: "effort", ReasoningLevels: []string{"high"}, ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.AgentBindings().BindAgentBinding(ctx, agentbinding.Binding{
		Handle: agentbinding.HandleReviewer, ProfileID: profile.ID, Effort: "high",
	}); err != nil {
		t.Fatal(err)
	}
	err = stack.DeleteModel(ctx, activeSession.SessionRef, profile.Backend.Provider.ModelConfigID)
	if err == nil || !strings.Contains(err.Error(), "rebind or reset") {
		t.Fatalf("DeleteModel(system-bound) error = %v", err)
	}
	if !stack.lookup.HasAlias(profile.Backend.Provider.ModelConfigID) {
		t.Fatal("DeleteModel removed a system-bound model")
	}
}

func TestDeleteModelRollsBackProfileAndBindingWhenRefreshFails(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	profile, err := stack.Connect(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "delete-rollback-model"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.AgentBindings().BindAgentBinding(ctx, agentbinding.Binding{
		Handle: agentbinding.HandleZenith, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort,
	}); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("refresh failed")
	stack.refreshConfiguredAgentsHook = func() error { return wantErr }
	if err := stack.DeleteModel(ctx, activeSession.SessionRef, profile.Backend.Provider.ModelConfigID); !errors.Is(err, wantErr) {
		t.Fatalf("DeleteModel() error = %v", err)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := modelprofile.Lookup(doc.ModelProfiles, profile.ID); !ok {
		t.Fatalf("rollback did not restore profile: %#v", doc.ModelProfiles)
	}
	if binding, ok := agentbinding.Lookup(doc.AgentBindings, agentbinding.HandleZenith); !ok || binding.ProfileID != profile.ID {
		t.Fatalf("rollback did not restore binding: %#v", binding)
	}
}

func TestUseModelRollsBackConfigWhenSessionStateUpdateFails(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	originalID := stack.lookup.DefaultID()
	next, err := stack.Connect(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "next-model"})
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.UseModel(ctx, activeSession.SessionRef, originalID); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("state update failed")
	stack.Sessions = &failingUpdateSessionService{Service: stack.Sessions, err: wantErr}
	err = stack.UseModel(ctx, activeSession.SessionRef, next.Backend.Provider.ModelConfigID)
	if !errors.Is(err, wantErr) || stack.lookup.DefaultID() != originalID {
		t.Fatalf("UseModel() error/default = %v/%q", err, stack.lookup.DefaultID())
	}
	state, stateErr := stack.Sessions.SnapshotState(ctx, activeSession.SessionRef)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if got, _ := state[gateway.StateCurrentModelAlias].(string); got != originalID {
		t.Fatalf("session model state = %q, want %q", got, originalID)
	}
}

func TestDeleteModelRestoresProfileWhenSessionStateUpdateFails(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	profile, err := stack.Connect(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "delete-state-rollback-model"})
	if err != nil {
		t.Fatal(err)
	}
	var refreshProfilePresence []bool
	stack.refreshConfiguredAgentsHook = func() error {
		doc, loadErr := stack.store.Load()
		if loadErr != nil {
			return loadErr
		}
		_, found := modelprofile.Lookup(doc.ModelProfiles, profile.ID)
		refreshProfilePresence = append(refreshProfilePresence, found)
		return nil
	}
	wantErr := errors.New("state update failed")
	stack.Sessions = &failOnceUpdateSessionService{Service: stack.Sessions, err: wantErr}
	err = stack.DeleteModel(ctx, activeSession.SessionRef, profile.Backend.Provider.ModelConfigID)
	if !errors.Is(err, wantErr) {
		t.Fatalf("DeleteModel() error = %v", err)
	}
	if want := []bool{false, true}; !reflect.DeepEqual(refreshProfilePresence, want) {
		t.Fatalf("profile presence during refreshes = %#v, want %#v", refreshProfilePresence, want)
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

func installCommittedConfigSaveFault(t *testing.T, stack *Stack, stage string, fault error) func() int {
	t.Helper()
	writes := 0
	stack.store.saveHook = func(doc AppConfig) error {
		writes++
		doc = configstore.Normalize(doc)
		if err := configstore.Validate(doc); err != nil {
			return err
		}
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		ops := atomicWriteOps{}
		switch stage {
		case "chmod":
			ops.chmod = func(path string, mode os.FileMode) error {
				if path == stack.store.path {
					return fault
				}
				return os.Chmod(path, mode)
			}
		case "fsync":
			ops.fsyncDir = func(string) error { return fault }
		default:
			t.Fatalf("unknown committed fault stage %q", stage)
		}
		return atomicWriteFile(stack.store.path, data, 0o600, ops)
	}
	return func() int { return writes }
}

func requireCommittedConfigWriteError(t *testing.T, err error, fault error) {
	t.Helper()
	if !errors.Is(err, fault) || !configstore.WriteCommitted(err) {
		t.Fatalf("operation error = %v, want committed %v", err, fault)
	}
}
