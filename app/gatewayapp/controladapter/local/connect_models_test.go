package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt/connectwizard"
)

func TestConnectModelPickerTracksHostConfiguration(t *testing.T) {
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
	appServer, err := NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	clients, err := appServer.Bind(appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := clients.Status.SessionStatus(ctx, appserver.StatusRequest{Surface: "test"})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := clients.Configuration.ConnectModel(ctx, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "connect-initial", ExpectedRevision: &status.Configuration.Revision},
		Config: appserver.ConnectConfig{
			Provider: "openai", Model: "gpt-5.6-sol", APIKey: "test-provider-key",
		},
	})
	if err != nil || initial.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel(initial) = %#v, %v", initial, err)
	}
	sessionID := createAppServerTestSession(t, clients, "create-connect-picker", "connect-picker", workspace)
	remote := bindAppServerHTTPTestClient(t, appServer, "local-user")
	state := connectwizard.ConnectWizardState{Provider: "openai"}
	command := "connect-model:" + state.EncodeCompletionState()
	assertSlashArgCandidate(t, clients.Completion, "", command, "gpt-5.6-sol", false)
	assertSlashArgCandidate(t, remote, sessionID, command, "gpt-6-astra", true)

	connected, err := clients.Configuration.ConnectModel(ctx, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "connect-astra", ExpectedRevision: &initial.Revision},
		Config:    appserver.ConnectConfig{Provider: "openai", Model: "gpt-6-astra"},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel(astra) = %#v, %v", connected, err)
	}
	assertSlashArgCandidate(t, clients.Completion, "", command, "gpt-6-astra", false)
	assertSlashArgCandidate(t, remote, sessionID, command, "gpt-6-astra", false)
	assertSlashArgCandidate(t, remote, sessionID, "disconnect-provider", "openai/gpt-6-astra", true)

	deleted, err := clients.Configuration.DeleteModel(ctx, appserver.DeleteModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "disconnect-astra", ExpectedRevision: &connected.Revision},
		Model:     "openai/gpt-6-astra",
	})
	if err != nil || deleted.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DeleteModel(astra) = %#v, %v", deleted, err)
	}
	assertSlashArgCandidate(t, remote, sessionID, command, "gpt-6-astra", true)
	assertSlashArgCandidate(t, remote, sessionID, command, "gpt-5.6-sol", false)
}
