package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/control/appserver"
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
		{Provider: "openai", Model: "gpt-5.6-sol", APIKey: "test-provider-key"},
		{Provider: "deepseek", Model: "deepseek-v4-flash", APIKey: "test-provider-key"},
		{Provider: "openai", Model: "gpt-5.6-luna"},
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
	targets := []string{"openai/gpt-5.6-sol", "deepseek/deepseek-v4-flash"}
	completed, err := adapter.DeleteModels(ctx, targets)
	if err != nil || !slices.Equal(completed, targets) {
		t.Fatalf("DeleteModels = %v, %v", completed, err)
	}
	for _, target := range targets {
		assertSlashArgCandidate(t, clients.Completion, sessionID, "disconnect-provider", target, false)
	}
	assertSlashArgCandidate(t, clients.Completion, sessionID, "disconnect-provider", "openai/gpt-5.6-luna", true)
	after, err := adapter.Status(ctx)
	if err != nil || after.ModelStatus.Alias != "openai/gpt-5.6-luna" {
		t.Fatalf("selected Session status = %#v, %v", after.ModelStatus, err)
	}
	completed, err = adapter.DeleteModels(ctx, []string{"openai/gpt-5.6-luna"})
	if err != nil || len(completed) != 1 {
		t.Fatalf("DeleteModels(final) = %v, %v", completed, err)
	}
	after, err = adapter.Status(ctx)
	if err != nil || after.ModelStatus.Display != "" {
		t.Fatalf("selected Session after final removal = %#v, %v", after.ModelStatus, err)
	}
}
