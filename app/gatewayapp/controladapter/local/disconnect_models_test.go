package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/modelprofile/builder"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
)

func TestDisconnectModelsRefreshesHostCatalogAndSelectedSession(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName: "caelis-test", UserID: "local-user", StoreDir: filepath.Join(root, "store"),
		WorkspaceKey: "workspace", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	clients := bindAppServerTestClients(t, host)
	status, err := clients.Status.SessionStatus(ctx, appserver.StatusRequest{Surface: "test"})
	if err != nil {
		t.Fatal(err)
	}
	revision := status.Configuration.Revision
	for i, cfg := range []appserver.ConnectConfig{
		{Provider: "xiaomi", Model: "mimo-v2.5", APIKey: "test-provider-key"},
		{Provider: "deepseek", Model: "deepseek-v4-flash", APIKey: "test-provider-key"},
		{Provider: "xiaomi", Model: "mimo-v2.6-flash"},
	} {
		result, err := clients.Configuration.ConnectModel(ctx, appserver.ConnectModelRequest{
			WriteBase: appserver.WriteBase{OperationID: fmt.Sprintf("connect-%d", i), ExpectedRevision: &revision}, Config: cfg,
		})
		if err != nil || result.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("connect = %#v, %v", result, err)
		}
		revision = result.Revision
	}
	sessionID := createAppServerTestSession(t, clients, "create-disconnect", "disconnect", workspace)
	adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		SessionID: sessionID, WorkspaceKey: "workspace", WorkspaceDir: workspace, Surface: "test",
		Sessions: clients.Sessions, Participants: clients.Participants, Status: clients.Status,
		Configuration: clients.Configuration, Agents: clients.Agents, Completion: clients.Completion, Plugins: clients.Plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	targets := []string{"xiaomi/mimo-v2.5", "deepseek/deepseek-v4-flash"}
	completed, err := adapter.DeleteModels(ctx, targets)
	if err != nil || !slices.Equal(completed, targets) {
		t.Fatalf("DeleteModels = %v, %v", completed, err)
	}
	for _, target := range targets {
		assertSlashArgCandidate(t, clients.Completion, sessionID, "disconnect-provider", target, false)
	}
	assertSlashArgCandidate(t, clients.Completion, sessionID, "disconnect-provider", "xiaomi/mimo-v2.6-flash", true)
	after, err := adapter.Status(ctx)
	if err != nil || after.ModelStatus.Alias != "xiaomi/mimo-v2.6-flash" {
		t.Fatalf("selected Session status = %#v, %v", after.ModelStatus, err)
	}
	completed, err = adapter.DeleteModels(ctx, []string{"xiaomi/mimo-v2.6-flash"})
	if err != nil || len(completed) != 1 {
		t.Fatalf("DeleteModels(final) = %v, %v", completed, err)
	}
	after, err = adapter.Status(ctx)
	if err != nil || after.ModelStatus.Display != "" {
		t.Fatalf("selected Session after final removal = %#v, %v", after.ModelStatus, err)
	}
}

func TestSavedRetiredConnectionCanBeSelectedAndDeletedOverHTTP(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	cfg := modelconfig.Config{Provider: "gemini", Model: "gemini-pro-latest", ContextWindowTokens: 1000000, AuthType: "none"}
	profile, err := builder.FromProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	doc := configstore.AppConfig{SchemaVersion: configstore.SchemaVersionV2, Models: configstore.PersistedModelConfig{Configs: []modelconfig.Config{cfg}}, ModelProfiles: modelprofile.Configuration{Profiles: []modelprofile.ModelProfile{profile}}}
	if err := configstore.New(storeDir).Save(doc); err != nil {
		t.Fatal(err)
	}
	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{AppName: "caelis-test", UserID: "local-user", StoreDir: storeDir, WorkspaceCWD: root, SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	services, err := NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	remote := bindAppServerHTTPTestClient(t, services, "local-user")
	assertSlashArgCandidate(t, remote, "", "disconnect-provider", "gemini/gemini-pro-latest", true)
	status, err := remote.SessionStatus(t.Context(), appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	revision := status.Configuration.Revision
	rejected, err := remote.ConnectModel(t.Context(), appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "retired-new", ExpectedRevision: &revision}, Config: appserver.ConnectConfig{Provider: "gemini", Model: "new-model", APIKey: "fixture"}})
	if err == nil || rejected.Outcome != appserver.OutcomeRejected {
		t.Fatalf("retired onboarding was accepted: %+v %v", rejected, err)
	}
	used, err := remote.UseModel(t.Context(), appserver.UseModelRequest{WriteBase: appserver.WriteBase{OperationID: "retired-use", ExpectedRevision: &revision}, Model: "gemini/gemini-pro-latest"})
	if err != nil || used.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("saved connection unavailable after rejected connect: %+v %v", used, err)
	}
	deleted, err := remote.DeleteModel(t.Context(), appserver.DeleteModelRequest{WriteBase: appserver.WriteBase{OperationID: "retired-delete", ExpectedRevision: &used.Revision}, Model: "gemini/gemini-pro-latest"})
	if err != nil || deleted.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("saved connection could not be removed: %+v %v", deleted, err)
	}
	assertSlashArgCandidate(t, remote, "", "disconnect-provider", "gemini/gemini-pro-latest", false)
	saved, err := configstore.New(storeDir).Load()
	if err != nil || len(saved.Models.Configs) != 0 || len(saved.Models.ProviderEndpoints) != 0 || len(saved.ModelProfiles.Profiles) != 0 {
		t.Fatalf("saved connection survived removal: %+v %v", saved.Models, err)
	}
}
