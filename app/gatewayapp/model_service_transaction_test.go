package gatewayapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestConnectStoresProviderAPIKeyBehindOpaqueReference(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	secret := "sk-connect-secret"
	profile, err := stack.connectTestModel(ModelConfig{
		Provider: "openai", API: providers.APIOpenAI, Model: "gpt-test", BaseURL: "https://api.example/v1", Token: secret, PersistToken: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	modelID := profile.Backend.Provider.ModelConfigID
	doc, err := stack.composition.store.Load()
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
	if endpointProfile.CredentialRef == "" || !strings.HasPrefix(endpointProfile.CredentialRef, "apikey:") || endpointProfile.Token != "" || endpointProfile.PersistToken {
		t.Fatalf("persisted provider credential = %#v", endpointProfile)
	}
	raw, err := os.ReadFile(stack.composition.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("config contains plaintext key: %s", raw)
	}
	got, err := stack.composition.apiKeyCredentials.Get(context.Background(), endpointProfile.CredentialRef)
	if err != nil || got != secret {
		t.Fatalf("credential Get() = %q, %v", got, err)
	}
	hydrated, err := stack.composition.lookup.ResolveConfig(modelID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.composition.lookup.ResolveModelConfig(context.Background(), hydrated, 0); err != nil {
		t.Fatalf("ResolveModelConfig() error = %v", err)
	}
}

func TestConnectReplacesLegacyEnvironmentCredential(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	configured := modelconfig.NormalizeConfig(ModelConfig{
		Provider: "deepseek",
		API:      providers.APIDeepSeek,
		Model:    "deepseek-v4-pro",
		Token:    "replacement-secret",
	})
	ref := credentialstore.BuildReference(configured.Provider, configured.ProviderEndpointID)
	writeLegacyEnvironmentCredentialForTest(t, stack.composition.storeDir, ref, "DEEPSEEK_API_KEY")

	profile, err := stack.connectTestModel(configured)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if strings.TrimSpace(profile.ID) == "" {
		t.Fatal("Connect() returned an empty ModelProfile")
	}
	doc, err := stack.composition.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	foundEndpoint := false
	for _, endpoint := range doc.Models.ProviderEndpoints {
		foundEndpoint = foundEndpoint || (endpoint.Provider == "deepseek" && endpoint.CredentialRef == ref)
	}
	if !foundEndpoint {
		t.Fatalf("DeepSeek endpoint does not reference replacement credential %q: %#v", ref, doc.Models.ProviderEndpoints)
	}
	if got, err := stack.composition.apiKeyCredentials.Get(context.Background(), ref); err != nil || got != "replacement-secret" {
		t.Fatalf("replacement credential = %q, %v", got, err)
	}
}

func TestConnectLegacyEnvironmentCredentialRollbackAllowsRetry(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	configured := modelconfig.NormalizeConfig(ModelConfig{
		Provider: "deepseek",
		API:      providers.APIDeepSeek,
		Model:    "deepseek-v4-pro",
		Token:    "replacement-secret",
	})
	ref := credentialstore.BuildReference(configured.Provider, configured.ProviderEndpointID)
	writeLegacyEnvironmentCredentialForTest(t, stack.composition.storeDir, ref, "DEEPSEEK_API_KEY")
	stack.composition.store.saveHook = func(AppConfig) error { return errors.New("save failed") }

	if _, err := stack.connectTestModel(configured); err == nil || !strings.Contains(err.Error(), "save failed") {
		t.Fatalf("Connect() error = %v, want save failure", err)
	}
	if _, err := stack.composition.apiKeyCredentials.Get(context.Background(), ref); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential after rollback error = %v, want removed replacement", err)
	}

	stack.composition.store.saveHook = nil
	if _, err := stack.connectTestModel(configured); err != nil {
		t.Fatalf("Connect() retry error = %v", err)
	}
	if got, err := stack.composition.apiKeyCredentials.Get(context.Background(), ref); err != nil || got != "replacement-secret" {
		t.Fatalf("credential after retry = %q, %v", got, err)
	}
}

func TestResolveModelConfigHidesLegacyCredentialStorageDetails(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	configured := modelconfig.NormalizeConfig(ModelConfig{
		Provider: "deepseek",
		API:      providers.APIDeepSeek,
		Model:    "deepseek-v4-pro",
	})
	ref := credentialstore.BuildReference(configured.Provider, configured.ProviderEndpointID)
	configured.CredentialRef = ref
	writeLegacyEnvironmentCredentialForTest(t, stack.composition.storeDir, ref, "DEEPSEEK_API_KEY")

	_, err := stack.composition.lookup.ResolveModelConfig(context.Background(), configured, 0)
	if err == nil || err.Error() != "model credential is invalid; reconnect with /connect" {
		t.Fatalf("ResolveModelConfig() error = %v, want concise reconnect guidance", err)
	}
	for _, internalDetail := range []string{"credentialstore", "environment-backed", ref, "DEEPSEEK_API_KEY"} {
		if strings.Contains(err.Error(), internalDetail) {
			t.Fatalf("ResolveModelConfig() error = %q, want no internal detail %q", err, internalDetail)
		}
	}
}

func TestHasReusableProviderAuthRejectsLegacyCredential(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	baseURL := "https://api.deepseek.com/anthropic"
	configured := modelconfig.NormalizeConfig(ModelConfig{
		Provider: "deepseek",
		API:      providers.APIDeepSeek,
		Model:    "deepseek-v4-pro",
		BaseURL:  baseURL,
		Token:    "current-secret",
	})
	ref := credentialstore.BuildReference(configured.Provider, configured.ProviderEndpointID)
	if _, err := stack.connectTestModel(configured); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if !stack.HasReusableProviderAuth(context.Background(), "deepseek", baseURL) {
		t.Fatal("HasReusableProviderAuth() = false for valid stored credential")
	}

	writeLegacyEnvironmentCredentialForTest(t, stack.composition.storeDir, ref, "DEEPSEEK_API_KEY")
	if stack.HasReusableProviderAuth(context.Background(), "deepseek", baseURL) {
		t.Fatal("HasReusableProviderAuth() = true for invalid legacy credential")
	}
}

func TestConnectRollsBackNewProviderCredentialWhenConfigSaveFails(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	configured := modelconfig.NormalizeConfig(ModelConfig{
		Provider: "openai", API: providers.APIOpenAI, Model: "gpt-rollback", BaseURL: "https://rollback.example/v1", Token: "secret",
	})
	ref := credentialstore.BuildReference(configured.Provider, configured.ProviderEndpointID)
	stack.composition.store.saveHook = func(AppConfig) error { return errors.New("save failed") }
	if _, err := stack.connectTestModel(configured); err == nil || !strings.Contains(err.Error(), "save failed") {
		t.Fatalf("Connect() error = %v", err)
	}
	if _, err := stack.composition.apiKeyCredentials.Get(context.Background(), ref); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back credential Get() error = %v", err)
	}
}

func TestProviderCredentialCASLoserRestoresCommittedWinner(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	stale, err := stack.composition.store.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	winner := modelconfig.NormalizeConfig(ModelConfig{
		Provider: "openai", API: providers.APIOpenAI, Model: "gpt-winner", BaseURL: "https://shared.example/v1", Token: "winner-secret",
	})
	if _, err := stack.connectTestModel(winner); err != nil {
		t.Fatalf("Connect(winner) error = %v", err)
	}
	ref := credentialstore.BuildReference(winner.Provider, winner.ProviderEndpointID)
	if got, err := stack.composition.apiKeyCredentials.Get(ctx, ref); err != nil || got != "winner-secret" {
		t.Fatalf("winner credential = %q, %v", got, err)
	}

	loser := winner
	loser.Model = "gpt-loser"
	loser.Token = "loser-secret"
	_, credentialTxn, err := stack.prepareProviderCredentials(ctx, []ModelConfig{loser})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.composition.store.CompareAndSave(ctx, stale.ConfigurationRevision, stale); err == nil {
		t.Fatal("stale AppConfig CAS unexpectedly committed")
	} else {
		var conflict *configstore.ConfigurationRevisionConflict
		if !errors.As(err, &conflict) {
			t.Fatalf("stale AppConfig CAS error = %v, want revision conflict", err)
		}
	}
	if err := credentialTxn.rollback(); err != nil {
		t.Fatal(err)
	}
	if got, err := stack.composition.apiKeyCredentials.Get(ctx, ref); err != nil || got != "winner-secret" {
		t.Fatalf("credential after CAS loser rollback = %q, %v; want winner", got, err)
	}
}

func writeLegacyEnvironmentCredentialForTest(t *testing.T, root string, ref string, environment string) {
	t.Helper()
	dir := filepath.Join(root, "providers", "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(ref))))
	path := filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
	raw, err := json.Marshal(map[string]any{
		"version":     1,
		"ref":         ref,
		"environment": environment,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
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
	stack.composition.store.savedHook = func() { invalidations++ }
	stack.composition.store.saveHook = func(doc AppConfig) error {
		doc = configstore.Normalize(doc)
		if err := configstore.Validate(doc); err != nil {
			return err
		}
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		return atomicWriteFile(stack.composition.store.path, data, 0o600, atomicWriteOps{
			fsyncDir: func(string) error { return fault },
		})
	}

	profiles, err := stack.connectTestModels([]ModelConfig{configured})
	if !errors.Is(err, fault) || !configstore.WriteCommitted(err) {
		t.Fatalf("ConnectModels() error = %v, want committed %v", err, fault)
	}
	if len(profiles) != 1 || invalidations != 1 {
		t.Fatalf("ConnectModels() profiles/invalidations = %d/%d, want 1/1", len(profiles), invalidations)
	}
	if got, err := stack.composition.apiKeyCredentials.Get(context.Background(), ref); err != nil || got != "committed-secret" {
		t.Fatalf("credential after committed config write = %q, %v", got, err)
	}
	doc, err := stack.composition.store.Load()
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
	if !stack.composition.lookup.HasAlias(profiles[0].Backend.Provider.ModelConfigID) {
		t.Fatalf("committed model %q missing from live lookup", profiles[0].Backend.Provider.ModelConfigID)
	}
}

func TestUseModelChangesDefaultWithoutOverwritingModelProfile(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	profile, err := stack.connectTestModel(ModelConfig{
		Provider:               "ollama",
		API:                    providers.APIOllama,
		Model:                  "use-committed",
		ReasoningLevels:        []string{"none", "high"},
		DefaultReasoningEffort: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	modelID := profile.Backend.Provider.ModelConfigID
	originalDoc, err := stack.composition.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	fault := errors.New("directory fsync after rename failed")
	writeCount := installCommittedConfigSaveFault(t, stack, "fsync", fault)

	err = stack.useTestHostModel(ctx, session.SessionRef{}, modelID, "high")
	requireCommittedConfigWriteError(t, err, fault)
	if writeCount() != 1 {
		t.Fatalf("config writes = %d, want one committed default-selection write", writeCount())
	}
	doc, loadErr := stack.composition.store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if doc.Models.DefaultID != "" ||
		doc.Models.DefaultAlias != "" ||
		doc.ModelProfiles.DefaultProfileID != profile.ID ||
		doc.ModelProfiles.DefaultEffort != "high" ||
		stack.composition.lookup.DefaultID() != modelID ||
		stack.composition.lookup.DefaultEffort() != "high" ||
		stack.composition.runtime.Model.ID != modelID ||
		stack.composition.runtime.ModelProfileEffort != "high" {
		t.Fatalf(
			"global defaults diverged: legacy=%q profile=%q effort=%q lookup=%q/%q runtime=%q/%q",
			doc.Models.DefaultID,
			doc.ModelProfiles.DefaultProfileID,
			doc.ModelProfiles.DefaultEffort,
			stack.composition.lookup.DefaultID(),
			stack.composition.lookup.DefaultEffort(),
			stack.composition.runtime.Model.ID,
			stack.composition.runtime.ModelProfileEffort,
		)
	}
	if !reflect.DeepEqual(doc.Models.Configs, originalDoc.Models.Configs) ||
		!reflect.DeepEqual(doc.Models.ProviderEndpoints, originalDoc.Models.ProviderEndpoints) ||
		!reflect.DeepEqual(doc.ModelProfiles.Profiles, originalDoc.ModelProfiles.Profiles) {
		t.Fatalf("UseModel overwrote model/profile definitions:\nbefore: %#v\nafter:  %#v", originalDoc, doc)
	}
	rawConfig, readErr := os.ReadFile(stack.composition.store.path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(rawConfig), `"default_model_id"`) || strings.Contains(string(rawConfig), `"default_alias"`) {
		t.Fatalf("config persisted redundant model default identities:\n%s", rawConfig)
	}
	if !strings.Contains(string(rawConfig), `"default_effort": "high"`) {
		t.Fatalf("config did not persist global default effort:\n%s", rawConfig)
	}
	state, stateErr := stack.composition.sessions.SnapshotState(ctx, activeSession.SessionRef)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if got := kernel.CurrentModelAlias(state); got != "" {
		t.Fatalf("Host UseModel mutated Session model alias = %q", got)
	}
	storeDir := stack.composition.storeDir
	workspaceKey := stack.composition.workspace.Key
	workspaceCWD := stack.composition.workspace.CWD
	if closeErr := stack.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	reloaded, reloadErr := NewLocalStack(Config{
		StoreDir:     storeDir,
		WorkspaceKey: workspaceKey,
		WorkspaceCWD: workspaceCWD,
		Sandbox:      SandboxConfig{RequestedType: "host"},
	})
	if reloadErr != nil {
		t.Fatal(reloadErr)
	}
	defer reloaded.Close()
	if reloaded.composition.runtime.ModelProfileID != profile.ID || reloaded.composition.runtime.ModelProfileEffort != "high" {
		t.Fatalf("reloaded global default = %q/%q, want %q/high", reloaded.composition.runtime.ModelProfileID, reloaded.composition.runtime.ModelProfileEffort, profile.ID)
	}
	self, ok := agentConfigForToolTest(reloaded.composition.runtime.Assembly.Agents, "self")
	if !ok {
		t.Fatalf("runtime-derived self missing from assembly: %#v", reloaded.composition.runtime.Assembly.Agents)
	}
	if got := self.SessionOptions.ModelID; got != profile.Backend.Provider.ModelConfigID {
		t.Fatalf("runtime-derived self model session option = %q, want %q", got, profile.Backend.Provider.ModelConfigID)
	}
	if got := self.SessionOptions.ConfigValues[acpConfigReasoningID]; got != "high" || self.SessionOptions.ReasoningEffortConfigID != acpConfigReasoningID {
		t.Fatalf("runtime-derived self reasoning session options = %#v, want high", self.SessionOptions)
	}
	if got := self.SessionOptions.ConfigValues[acpConfigModeID]; got != "manual" {
		t.Fatalf("runtime-derived self mode session option = %q, want manual", got)
	}
}

func TestHostModelSelectionDoesNotRequireOrMutateSession(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	profile, err := stack.connectTestModel(ModelConfig{
		Provider:               "ollama",
		API:                    providers.APIOllama,
		Model:                  "host-only-selection",
		ReasoningLevels:        []string{"none", "high"},
		DefaultReasoningEffort: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	modelID := profile.Backend.Provider.ModelConfigID
	if err := stack.useTestHostModel(ctx, session.SessionRef{}, modelID, "high"); err != nil {
		t.Fatalf("Host UseModel() error = %v", err)
	}
	state, err := stack.composition.sessions.SnapshotState(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := kernel.CurrentModelAlias(state); got != "" {
		t.Fatalf("Host model selection mutated Session alias = %q", got)
	}
}

func TestConnectPersistsCanonicalProfileForFutureActivation(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	originalID := stack.composition.lookup.DefaultID()
	originalRuntimeModel := stack.composition.runtime.Model.ID
	_, err := stack.connectTestModel(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "new-model"})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if stack.composition.lookup.DefaultID() != originalID || stack.composition.runtime.Model.ID != originalRuntimeModel {
		t.Fatalf("existing default changed after committed connect: %q/%q", stack.composition.lookup.DefaultID(), stack.composition.runtime.Model.ID)
	}
	doc, err := stack.composition.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	modelID := ""
	for _, profile := range doc.ModelProfiles.Profiles {
		if strings.Contains(profile.DisplayName, "new-model") {
			found = true
			if profile.Backend.Provider != nil {
				modelID = profile.Backend.Provider.ModelConfigID
			}
		}
	}
	if !found || modelID == "" || !stack.composition.lookup.HasAlias(modelID) {
		t.Fatalf("committed profile did not roll forward to durable/live state: %#v", doc.ModelProfiles)
	}
}

func TestConnectModelsPersistsStandardProfilesAtomicallyAndKeepsExistingDefault(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	originalDefaultID := stack.composition.lookup.DefaultID()
	profiles, err := stack.connectTestModels([]ModelConfig{
		{Provider: "ollama", API: providers.APIOllama, Model: "batch-first"},
		{Provider: "ollama", API: providers.APIOllama, Model: "batch-second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || stack.composition.lookup.DefaultID() != originalDefaultID || stack.composition.runtime.Model.ID != originalDefaultID {
		t.Fatalf("batch profiles/default/runtime = %#v/%q/%q", profiles, stack.composition.lookup.DefaultID(), stack.composition.runtime.Model.ID)
	}
	doc, err := stack.composition.store.Load()
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

	before := stack.composition.lookup.Snapshot()
	_, err = stack.connectTestModels([]ModelConfig{
		{Provider: "ollama", API: providers.APIOllama, Model: "should-rollback"},
		{Model: "invalid-without-provider"},
	})
	if err == nil {
		t.Fatal("ConnectModels(invalid batch) error = nil")
	}
	after := stack.composition.lookup.Snapshot()
	if !reflect.DeepEqual(after, before) || stack.composition.lookup.HasAlias("ollama/should-rollback") {
		t.Fatalf("invalid batch leaked into lookup: before=%#v after=%#v", before, after)
	}
}

func TestConnectModelsSelectsFirstProfileWhenNoModelExists(t *testing.T) {
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceKey: t.TempDir(), WorkspaceCWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	profiles, err := stack.connectTestModels([]ModelConfig{
		{Provider: "ollama", API: providers.APIOllama, Model: "first"},
		{Provider: "ollama", API: providers.APIOllama, Model: "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstModelID := profiles[0].Backend.Provider.ModelConfigID
	if len(profiles) != 2 || stack.composition.lookup.DefaultID() != firstModelID || stack.composition.runtime.Model.ID != firstModelID {
		t.Fatalf("profiles/default/runtime = %#v/%q/%q", profiles, stack.composition.lookup.DefaultID(), stack.composition.runtime.Model.ID)
	}
}

func TestDeleteModelRemovesProviderProfileAndOrdinaryBindings(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	profile, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "delete-profile-model",
		ReasoningMode: "effort", ReasoningLevels: []string{"high"}, ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.testAgentBindings().BindAgentBinding(ctx, agentbinding.Binding{
		Handle: agentbinding.HandleZenith, ProfileID: profile.ID, Effort: "high",
	}); err != nil {
		t.Fatal(err)
	}
	if err := stack.deleteTestHostModel(ctx, session.SessionRef{}, profile.Backend.Provider.ModelConfigID); err != nil {
		t.Fatal(err)
	}
	doc, err := stack.composition.store.Load()
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

func TestDeleteNonDefaultModelPreservesGlobalEffort(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	selected, err := stack.connectTestModel(ModelConfig{
		Provider:               "ollama",
		API:                    providers.APIOllama,
		Model:                  "selected-before-delete",
		ReasoningLevels:        []string{"none", "high"},
		DefaultReasoningEffort: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.useTestHostModel(ctx, session.SessionRef{}, selected.Backend.Provider.ModelConfigID, "high"); err != nil {
		t.Fatal(err)
	}
	unrelated, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "unrelated-delete",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.deleteTestHostModel(ctx, session.SessionRef{}, unrelated.Backend.Provider.ModelConfigID); err != nil {
		t.Fatal(err)
	}
	doc, err := stack.composition.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if doc.ModelProfiles.DefaultProfileID != selected.ID ||
		doc.ModelProfiles.DefaultEffort != "high" ||
		stack.composition.lookup.DefaultID() != selected.Backend.Provider.ModelConfigID ||
		stack.composition.lookup.DefaultEffort() != "high" ||
		stack.composition.runtime.ModelProfileEffort != "high" {
		t.Fatalf(
			"default changed after deleting unrelated model: profile=%q effort=%q lookup=%q/%q runtime=%q",
			doc.ModelProfiles.DefaultProfileID,
			doc.ModelProfiles.DefaultEffort,
			stack.composition.lookup.DefaultID(),
			stack.composition.lookup.DefaultEffort(),
			stack.composition.runtime.ModelProfileEffort,
		)
	}
}

func TestDeleteModelRollsForwardAfterCommittedConfigWriteFault(t *testing.T) {
	for _, stage := range []string{"chmod", "fsync"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			stack, activeSession := newLocalStateTestStack(t)
			profile, err := stack.connectTestModel(ModelConfig{
				Provider: "ollama", API: providers.APIOllama, Model: "delete-committed-" + stage,
			})
			if err != nil {
				t.Fatal(err)
			}
			modelID := profile.Backend.Provider.ModelConfigID
			if err := stack.useTestHostModel(ctx, session.SessionRef{}, modelID); err != nil {
				t.Fatal(err)
			}
			if _, err := stack.testAgentBindings().BindAgentBinding(ctx, agentbinding.Binding{
				Handle: agentbinding.HandleZenith, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort,
			}); err != nil {
				t.Fatal(err)
			}

			fault := errors.New(stage + " after rename failed")
			writeCount := installCommittedConfigSaveFault(t, stack, stage, fault)
			err = stack.deleteTestHostModel(ctx, session.SessionRef{}, modelID)
			requireCommittedConfigWriteError(t, err, fault)
			if got := writeCount(); got != 1 {
				t.Fatalf("config writes = %d, want one committed roll-forward write", got)
			}

			doc, loadErr := stack.composition.store.Load()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if _, ok := modelprofile.Lookup(doc.ModelProfiles, profile.ID); ok {
				t.Fatalf("committed deletion retained profile %q", profile.ID)
			}
			if _, ok := agentbinding.Lookup(doc.AgentBindings, agentbinding.HandleZenith); ok {
				t.Fatalf("committed deletion retained binding %q", agentbinding.HandleZenith)
			}
			if stack.composition.lookup.HasAlias(modelID) || stack.composition.runtime.Model.ID != stack.composition.lookup.DefaultID() {
				t.Fatalf("lookup/runtime diverged after committed deletion: has=%v runtime=%q default=%q", stack.composition.lookup.HasAlias(modelID), stack.composition.runtime.Model.ID, stack.composition.lookup.DefaultID())
			}
			state, stateErr := stack.composition.sessions.SnapshotState(ctx, activeSession.SessionRef)
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			if got := kernel.CurrentModelAlias(state); got != "" {
				t.Fatalf("Session retained deleted model alias %q", got)
			}
		})
	}
}

func TestDeleteModelRollsBackAfterPreCommitConfigWriteFault(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	profile, err := stack.connectTestModel(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "delete-precommit"})
	if err != nil {
		t.Fatal(err)
	}
	modelID := profile.Backend.Provider.ModelConfigID
	previousRuntimeID := stack.composition.runtime.Model.ID
	fault := errors.New("rename failed")
	stack.composition.store.saveHook = func(AppConfig) error { return fault }

	err = stack.deleteTestHostModel(ctx, session.SessionRef{}, modelID)
	if !errors.Is(err, fault) || configstore.WriteCommitted(err) {
		t.Fatalf("DeleteModel() error = %v, want uncommitted %v", err, fault)
	}
	doc, loadErr := stack.composition.store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := modelprofile.Lookup(doc.ModelProfiles, profile.ID); !ok || !stack.composition.lookup.HasAlias(modelID) || stack.composition.runtime.Model.ID != previousRuntimeID {
		t.Fatalf("pre-commit rollback diverged: profile=%v lookup=%v runtime=%q want=%q", ok, stack.composition.lookup.HasAlias(modelID), stack.composition.runtime.Model.ID, previousRuntimeID)
	}
}

func TestDeleteModelRejectsSystemBoundProfile(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	profile, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "system-bound-model",
		ReasoningMode: "effort", ReasoningLevels: []string{"high"}, ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.testAgentBindings().BindAgentBinding(ctx, agentbinding.Binding{
		Handle: agentbinding.HandleReviewer, ProfileID: profile.ID, Effort: "high",
	}); err != nil {
		t.Fatal(err)
	}
	err = stack.deleteTestHostModel(ctx, session.SessionRef{}, profile.Backend.Provider.ModelConfigID)
	if err == nil || !strings.Contains(err.Error(), "rebind or reset") {
		t.Fatalf("DeleteModel(system-bound) error = %v", err)
	}
	if !stack.composition.lookup.HasAlias(profile.Backend.Provider.ModelConfigID) {
		t.Fatal("DeleteModel removed a system-bound model")
	}
}

func TestDeleteModelPersistsCanonicalDeletionForFutureActivation(t *testing.T) {
	ctx := context.Background()
	stack, _ := newLocalStateTestStack(t)
	profile, err := stack.connectTestModel(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "delete-rollback-model"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.testAgentBindings().BindAgentBinding(ctx, agentbinding.Binding{
		Handle: agentbinding.HandleZenith, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort,
	}); err != nil {
		t.Fatal(err)
	}
	if err := stack.deleteTestHostModel(ctx, session.SessionRef{}, profile.Backend.Provider.ModelConfigID); err != nil {
		t.Fatalf("DeleteModel() error = %v", err)
	}
	doc, err := stack.composition.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := modelprofile.Lookup(doc.ModelProfiles, profile.ID); ok {
		t.Fatalf("committed deletion restored profile: %#v", doc.ModelProfiles)
	}
	if binding, ok := agentbinding.Lookup(doc.AgentBindings, agentbinding.HandleZenith); ok {
		t.Fatalf("committed deletion restored binding: %#v", binding)
	}
	if stack.composition.lookup.HasAlias(profile.Backend.Provider.ModelConfigID) {
		t.Fatal("committed deletion did not roll forward to live lookup")
	}
}

func installCommittedConfigSaveFault(t *testing.T, stack *Stack, stage string, fault error) func() int {
	t.Helper()
	writes := 0
	stack.composition.store.saveHook = func(doc AppConfig) error {
		writes++
		doc = configstore.Normalize(doc)
		doc.ConfigurationRevision++
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
				if path == stack.composition.store.path {
					return fault
				}
				return os.Chmod(path, mode)
			}
		case "fsync":
			ops.fsyncDir = func(string) error { return fault }
		default:
			t.Fatalf("unknown committed fault stage %q", stage)
		}
		return atomicWriteFile(stack.composition.store.path, data, 0o600, ops)
	}
	return func() int { return writes }
}

func requireCommittedConfigWriteError(t *testing.T, err error, fault error) {
	t.Helper()
	if !errors.Is(err, fault) || !configstore.WriteCommitted(err) {
		t.Fatalf("operation error = %v, want committed %v", err, fault)
	}
}
