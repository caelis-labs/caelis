package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

func TestAppServerFacadeCreatesIndependentWorkspaceSessions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workspaceA := filepath.Join(root, "workspace-a")
	workspaceB := filepath.Join(root, "workspace-b")
	if err := os.MkdirAll(workspaceA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceB, 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalA, err := filepath.EvalSymlinks(workspaceA)
	if err != nil {
		t.Fatal(err)
	}
	canonicalB, err := filepath.EvalSymlinks(workspaceB)
	if err != nil {
		t.Fatal(err)
	}
	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName: "caelis-test", UserID: "local-user", StoreDir: filepath.Join(root, "store"),
		WorkspaceKey: "workspace-a", WorkspaceCWD: workspaceA,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	server, err := NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	clients, tasks, err := server.Bind(controlclient.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	if tasks == nil {
		t.Fatal("AppServer task client is nil")
	}

	newFacade := func(workspaceKey, cwd string) *controladapter.SessionClientAdapter {
		t.Helper()
		facade, err := controladapter.NewAppServerAdapter(controladapter.AppServerAdapterConfig{
			WorkspaceKey: workspaceKey, WorkspaceDir: cwd, Surface: "test",
			Sessions: clients.Sessions, Participants: clients.Participants, Status: clients.Status,
			Configuration: clients.Configuration, Agents: clients.Agents,
			Completion: clients.Completion, Plugins: clients.Plugins,
		})
		if err != nil {
			t.Fatal(err)
		}
		return facade
	}

	createdA, err := newFacade("workspace-a", workspaceA).NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	createdB, err := newFacade("workspace-b", workspaceB).NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if createdA.SessionID == "" || createdB.SessionID == "" || createdA.SessionID == createdB.SessionID {
		t.Fatalf("created Sessions = %#v / %#v", createdA, createdB)
	}
	stateA, err := clients.Sessions.InspectSession(ctx, controlclient.StateRequest{SessionID: createdA.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	stateB, err := clients.Sessions.InspectSession(ctx, controlclient.StateRequest{SessionID: createdB.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if stateA.WorkspaceKey != "workspace-a" || stateA.CWD != canonicalA {
		t.Fatalf("workspace A state = %#v", stateA)
	}
	if stateB.WorkspaceKey != "workspace-b" || stateB.CWD != canonicalB {
		t.Fatalf("workspace B state = %#v", stateB)
	}
}
