package gatewayapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

func TestPluginCommandsUseSharedLedgerAndHostRevisionCAS(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(tmp, "demo")
	buildMinimalPluginDir(t, pluginDir, `{"name":"demo","version":"1.0.0"}`)

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()
	revision, ok := stack.currentPluginConfigurationRevision(ctx)
	if !ok {
		t.Fatal("missing configuration revision")
	}
	principal := controlclient.Principal{ID: "owner"}

	first, err := stack.PluginCommands().AddPluginPath(ctx, principal, controlclient.AddPluginPathRequest{
		WriteBase: controlclient.WriteBase{OperationID: "plugin-add-path-1", ExpectedRevision: &revision},
		Path:      pluginDir,
	})
	if err != nil || first.Outcome != controlclient.OutcomeCommitted || first.Revision <= revision {
		t.Fatalf("AddPluginPath() = %#v, %v", first, err)
	}

	replay, err := stack.PluginCommands().AddPluginPath(ctx, principal, controlclient.AddPluginPathRequest{
		WriteBase: controlclient.WriteBase{OperationID: "plugin-add-path-1", ExpectedRevision: &revision},
		Path:      pluginDir,
	})
	if err != nil || replay.Outcome != controlclient.OutcomeCommitted || replay.Revision != first.Revision {
		t.Fatalf("replay AddPluginPath() = %#v, %v", replay, err)
	}

	stale, err := stack.PluginCommands().EnablePlugin(ctx, principal, controlclient.EnablePluginRequest{
		WriteBase: controlclient.WriteBase{OperationID: "plugin-enable-stale", ExpectedRevision: &revision},
		ID:        "demo",
	})
	if err == nil || stale.Outcome != controlclient.OutcomeConflicted {
		t.Fatalf("EnablePlugin(stale) = %#v, %v", stale, err)
	}
}

func TestPluginCommandsRejectSessionAddressAndMissingRevision(t *testing.T) {
	tmp := t.TempDir()
	stack := buildPluginStack(t, filepath.Join(tmp, "store"), filepath.Join(tmp, "ws"))
	principal := controlclient.Principal{ID: "owner"}
	revision := uint64(1)

	if _, err := stack.PluginCommands().InstallPlugin(context.Background(), principal, controlclient.InstallPluginRequest{
		WriteBase: controlclient.WriteBase{OperationID: "session-address", SessionID: "session-1", ExpectedRevision: &revision},
		Source:    "demo@market",
	}); err == nil || !strings.Contains(err.Error(), "must not address a Session") {
		t.Fatalf("InstallPlugin(session) error = %v", err)
	}
	if _, err := stack.PluginCommands().InstallPlugin(context.Background(), principal, controlclient.InstallPluginRequest{
		WriteBase: controlclient.WriteBase{OperationID: "missing-revision"},
		Source:    "demo@market",
	}); err == nil || !strings.Contains(err.Error(), "expected_revision") {
		t.Fatalf("InstallPlugin(missing revision) error = %v", err)
	}
}

func TestPluginPureConfigMutationLeavesActiveSessionRuntimeUnchanged(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(tmp, "skillplugin")
	skillDir := filepath.Join(pluginDir, "skills", "runtime-skill")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	buildMinimalPluginDir(t, pluginDir, `{"name":"skill-plugin","version":"1.0.0"}`)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: runtime-skill\ndescription: Runtime plugin skill.\n---\n# Runtime Skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()
	revision, ok := stack.currentPluginConfigurationRevision(ctx)
	if !ok {
		t.Fatal("missing revision")
	}
	principal := controlclient.Principal{ID: "owner"}
	if _, err := stack.PluginCommands().AddPluginPath(ctx, principal, controlclient.AddPluginPathRequest{
		WriteBase: controlclient.WriteBase{OperationID: "plugin-add-for-activation", ExpectedRevision: &revision},
		Path:      pluginDir,
	}); err != nil {
		t.Fatalf("AddPluginPath() error = %v", err)
	}

	activated := activateFutureAssemblyStack(t, stack, "plugin-active")
	before, _ := activated.runtime.BaseMetadata["system_prompt"].(string)
	if !strings.Contains(before, "skillplugin:runtime-skill") {
		t.Fatalf("active assembly missing plugin skill:\n%s", before)
	}

	revision, ok = stack.currentPluginConfigurationRevision(ctx)
	if !ok {
		t.Fatal("missing revision after add")
	}
	if _, err := stack.PluginCommands().DisablePlugin(ctx, principal, controlclient.DisablePluginRequest{
		WriteBase: controlclient.WriteBase{OperationID: "plugin-disable-active", ExpectedRevision: &revision},
		ID:        "skillplugin",
	}); err != nil {
		t.Fatalf("DisablePlugin() error = %v", err)
	}
	after, _ := activated.runtime.BaseMetadata["system_prompt"].(string)
	if after != before {
		t.Fatalf("active Session Runtime changed after pure config mutation")
	}

	reactivated := activateFutureAssemblyStack(t, stack, "plugin-reactivated")
	prompt, _ := reactivated.runtime.BaseMetadata["system_prompt"].(string)
	if strings.Contains(prompt, "runtime-skill") {
		t.Fatalf("future activation still has disabled plugin skill:\n%s", prompt)
	}
}

func TestPluginExternalEffectStaleRevisionDoesNotRunFetch(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()
	principal := controlclient.Principal{ID: "owner"}
	current, ok := stack.currentPluginConfigurationRevision(ctx)
	if !ok {
		t.Fatal("missing revision")
	}
	// Bump once via pure path so a stale ExpectedRevision cannot fetch.
	pluginDir := filepath.Join(tmp, "seed")
	buildMinimalPluginDir(t, pluginDir, `{"name":"seed","version":"1.0.0"}`)
	if _, err := stack.PluginCommands().AddPluginPath(ctx, principal, controlclient.AddPluginPathRequest{
		WriteBase: controlclient.WriteBase{OperationID: "seed-path", ExpectedRevision: &current},
		Path:      pluginDir,
	}); err != nil {
		t.Fatalf("seed AddPluginPath: %v", err)
	}
	current, ok = stack.currentPluginConfigurationRevision(ctx)
	if !ok || current == 0 {
		t.Fatalf("revision after seed = %d, ok=%v", current, ok)
	}
	stale := current - 1
	marketplacesRoot := filepath.Join(storeDir, "plugins", "marketplaces")
	beforeNames := listDirNames(t, marketplacesRoot)

	result, err := stack.PluginCommands().AddMarketplace(ctx, principal, controlclient.AddMarketplaceRequest{
		WriteBase: controlclient.WriteBase{OperationID: "stale-marketplace", ExpectedRevision: &stale},
		Source:    "acme/never-fetched",
	})
	if err == nil || result.Outcome != controlclient.OutcomeConflicted {
		t.Fatalf("AddMarketplace(stale) = %#v, %v want conflicted", result, err)
	}
	if errorcode.CodeOf(err) == errorcode.UnknownOutcome {
		t.Fatalf("stale revision must not be unknown: %v", err)
	}
	afterNames := listDirNames(t, marketplacesRoot)
	if len(afterNames) != len(beforeNames) {
		t.Fatalf("stale revision still mutated marketplace cache: before=%v after=%v", beforeNames, afterNames)
	}
}

func TestPluginExternalEffectRejectedWithoutEffectForInvalidSource(t *testing.T) {
	tmp := t.TempDir()
	stack := buildPluginStack(t, filepath.Join(tmp, "store"), filepath.Join(tmp, "ws"))
	ctx := context.Background()
	revision, ok := stack.currentPluginConfigurationRevision(ctx)
	if !ok {
		t.Fatal("missing revision")
	}
	principal := controlclient.Principal{ID: "owner"}
	result, err := stack.PluginCommands().AddMarketplace(ctx, principal, controlclient.AddMarketplaceRequest{
		WriteBase: controlclient.WriteBase{OperationID: "invalid-source", ExpectedRevision: &revision},
		Source:    "",
	})
	if err == nil || result.Outcome == controlclient.OutcomeUnknown {
		t.Fatalf("empty source = %#v, %v want rejected not unknown", result, err)
	}
	if errorcode.CodeOf(err) == errorcode.UnknownOutcome {
		t.Fatalf("validation failure must not be unknown: %v", err)
	}
}

func TestPluginMarketplaceNotFoundIsRejectedWithoutUnknown(t *testing.T) {
	tmp := t.TempDir()
	stack := buildPluginStack(t, filepath.Join(tmp, "store"), filepath.Join(tmp, "ws"))
	ctx := context.Background()
	revision, ok := stack.currentPluginConfigurationRevision(ctx)
	if !ok {
		t.Fatal("missing revision")
	}
	principal := controlclient.Principal{ID: "owner"}
	result, err := stack.PluginCommands().UpdateMarketplace(ctx, principal, controlclient.UpdateMarketplaceRequest{
		WriteBase: controlclient.WriteBase{OperationID: "missing-market", ExpectedRevision: &revision},
		Name:      "does-not-exist",
	})
	if err == nil || result.Outcome == controlclient.OutcomeUnknown {
		t.Fatalf("UpdateMarketplace(missing) = %#v, %v", result, err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want not found", err)
	}
	if errorcode.CodeOf(err) == errorcode.UnknownOutcome {
		t.Fatalf("not found must not be unknown: %v", err)
	}
}

func TestPluginMarketplaceReceiptRestoresResourceKind(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	stack := buildPluginStack(t, storeDir, filepath.Join(tmp, "ws"))
	ctx := context.Background()
	revision, ok := stack.currentPluginConfigurationRevision(ctx)
	if !ok {
		t.Fatal("missing revision")
	}
	principal := controlclient.Principal{ID: "owner"}
	marketDir := filepath.Join(tmp, "market")
	writeLocalMarketplace(t, marketDir, "receipt-market", "demo-plugin")

	result, err := stack.PluginCommands().AddMarketplace(ctx, principal, controlclient.AddMarketplaceRequest{
		WriteBase: controlclient.WriteBase{OperationID: "market-receipt-1", ExpectedRevision: &revision},
		Source:    marketDir,
	})
	if err != nil || result.Outcome != controlclient.OutcomeCommitted {
		t.Fatalf("AddMarketplace() = %#v, %v", result, err)
	}
	if result.Resource == nil || result.Resource.Kind != controlclient.CommandResourceMarketplace {
		t.Fatalf("Resource = %#v, want marketplace kind", result.Resource)
	}

	// Intent-only restart recovery must restore the marketplace resource kind,
	// not default to plugin.
	if err := stack.writePluginOperationReceipt(ctx, pluginOperationReceipt{
		PrincipalID:  principal.ID,
		OperationID:  "market-receipt-recover",
		Digest:       "digest-market",
		Action:       controlclient.ActionPluginMarketplaceAdd,
		Outcome:      controlclient.OutcomeCommitted,
		Revision:     result.Revision,
		ResourceKind: controlclient.CommandResourceMarketplace,
		Target:       result.Resource.Ref,
	}); err != nil {
		t.Fatal(err)
	}
	recovered, found, err := stack.loadPluginOperationReceipt(ctx, principal.ID, "market-receipt-recover", "digest-market")
	if err != nil || !found {
		t.Fatalf("load receipt: found=%v err=%v", found, err)
	}
	command := pluginCommandResultFromReceipt(recovered)
	if command.Resource == nil || command.Resource.Kind != controlclient.CommandResourceMarketplace || command.Resource.Ref != result.Resource.Ref {
		t.Fatalf("recovered Resource = %#v, want marketplace %q", command.Resource, result.Resource.Ref)
	}
	// Action-only derivation must also be deterministic when ResourceKind is absent.
	recovered.ResourceKind = ""
	derived := pluginCommandResultFromReceipt(recovered)
	if derived.Resource == nil || derived.Resource.Kind != controlclient.CommandResourceMarketplace {
		t.Fatalf("derived Resource = %#v, want marketplace kind from action", derived.Resource)
	}
}

func TestPluginMarketplaceIntentOnlyRestartRecoversMarketplaceKind(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	stack := buildPluginStack(t, storeDir, filepath.Join(tmp, "ws"))
	ctx := context.Background()
	revision, ok := stack.currentPluginConfigurationRevision(ctx)
	if !ok {
		t.Fatal("missing revision")
	}
	principal := controlclient.Principal{ID: stack.UserID}
	marketDir := filepath.Join(tmp, "market")
	writeLocalMarketplace(t, marketDir, "restart-market", "demo-plugin")
	request := controlclient.AddMarketplaceRequest{
		WriteBase: controlclient.WriteBase{OperationID: "market-intent-restart", ExpectedRevision: &revision},
		Source:    marketDir,
	}

	failing := &completeFailingOperationStore{OperationStore: stack.operations}
	firstCommands, err := controlclient.NewCommandService(controlclient.CommandServiceConfig{
		Authorizer: controlclient.ProductCommandAuthorizer{}, Operations: failing, Backend: stack,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, firstErr := firstCommands.AddMarketplace(ctx, principal, request)
	if firstErr == nil || first.Outcome != controlclient.OutcomeUnknown {
		t.Fatalf("AddMarketplace(first) = %#v, %v; want intent-only unknown after completion fault", first, firstErr)
	}

	restartedOperations := controlclient.NewFileOperationStore(filepath.Join(stack.storeDir, "control-operations"))
	if err := restartedOperations.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	restartedCommands, err := controlclient.NewCommandService(controlclient.CommandServiceConfig{
		Authorizer: controlclient.ProductCommandAuthorizer{}, Operations: restartedOperations, Backend: stack,
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, recoveryErr := restartedCommands.AddMarketplace(ctx, principal, request)
	if recoveryErr != nil || recovered.Outcome != controlclient.OutcomeCommitted {
		t.Fatalf("AddMarketplace(recovered) = %#v, %v", recovered, recoveryErr)
	}
	if recovered.Resource == nil || recovered.Resource.Kind != controlclient.CommandResourceMarketplace {
		t.Fatalf("recovered Resource = %#v, want marketplace kind", recovered.Resource)
	}
	if recovered.Resource.Ref != "restart-market" {
		t.Fatalf("recovered Resource.Ref = %q, want restart-market", recovered.Resource.Ref)
	}
	if !stack.CanRecoverControlCommand(controlclient.ActionPluginMarketplaceAdd) {
		t.Fatal("marketplace add must be recoverable")
	}
}

func TestClassifyPluginMutationErrorPostEffectCASIsUnknown(t *testing.T) {
	err := classifyPluginMutationError(pluginMutationResult{EffectStarted: true}, &configstore.ConfigurationRevisionConflict{
		Expected: 1, Actual: 2,
	})
	var outcomeErr *controlclient.OutcomeError
	if !errors.As(err, &outcomeErr) || outcomeErr.Outcome != controlclient.OutcomeUnknown {
		t.Fatalf("classify post-effect CAS = %v, want unknown", err)
	}
	if errorcode.CodeOf(err) != errorcode.UnknownOutcome {
		t.Fatalf("code = %s, want unknown_outcome", errorcode.CodeOf(err))
	}
}

func TestPluginExternalEffectReplayDoesNotRepeatInstall(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(tmp, "installable")
	buildMinimalPluginDir(t, pluginDir, `{"name":"installable","version":"1.0.0"}`)

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()
	revision, ok := stack.currentPluginConfigurationRevision(ctx)
	if !ok {
		t.Fatal("missing revision")
	}
	principal := controlclient.Principal{ID: "owner"}
	req := controlclient.InstallPluginRequest{
		WriteBase: controlclient.WriteBase{OperationID: "plugin-install-once", ExpectedRevision: &revision},
		Source:    pluginDir,
	}
	first, err := stack.PluginCommands().InstallPlugin(ctx, principal, req)
	if err != nil || first.Outcome != controlclient.OutcomeCommitted {
		t.Fatalf("InstallPlugin() = %#v, %v", first, err)
	}
	second, err := stack.PluginCommands().InstallPlugin(ctx, principal, req)
	if err != nil || second.Outcome != controlclient.OutcomeCommitted || second.Revision != first.Revision {
		t.Fatalf("InstallPlugin(replay) = %#v, %v", second, err)
	}
	if !stack.CanRecoverControlCommand(controlclient.ActionPluginInstall) {
		t.Fatal("install must be recoverable")
	}
	if stack.CanRecoverControlCommand(controlclient.ActionPluginEnable) {
		t.Fatal("pure config enable must not claim external-effect recovery")
	}
}

func listDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func writeLocalMarketplace(t *testing.T, dir, name, pluginName string) {
	t.Helper()
	pluginDir := filepath.Join(dir, "plugins", pluginName)
	marketManifestDir := filepath.Join(dir, ".claude-plugin")
	for _, d := range []string{pluginDir, marketManifestDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{
  "name": "` + name + `",
  "owner": {"name": "tester"},
  "plugins": [{"name": "` + pluginName + `", "source": "./plugins/` + pluginName + `"}]
}`
	if err := os.WriteFile(filepath.Join(marketManifestDir, "marketplace.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	buildMinimalPluginDir(t, pluginDir, `{"name":"`+pluginName+`","version":"1.0.0"}`)
}
