package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
)

func TestAppServerFacadeResetDoesNotCreateWorkspaceSessions(t *testing.T) {
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
	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName: "caelis-test", UserID: "local-user", StoreDir: filepath.Join(root, "store"),
		WorkspaceKey: "workspace-a", WorkspaceCWD: workspaceA,
		Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	server, err := NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	clients, err := server.Bind(appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	if clients.Tasks == nil {
		t.Fatal("AppServer task client is nil")
	}

	newFacade := func(workspaceKey, cwd string) *appserveradapter.SessionClientAdapter {
		t.Helper()
		facade, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
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

	if err := newFacade("workspace-a", workspaceA).ResetSession(ctx); err != nil {
		t.Fatal(err)
	}
	if err := newFacade("workspace-b", workspaceB).ResetSession(ctx); err != nil {
		t.Fatal(err)
	}
	for _, workspaceKey := range []string{"workspace-a", "workspace-b"} {
		listed, err := clients.Sessions.ListSessions(ctx, appserver.ListSessionsRequest{WorkspaceKey: workspaceKey, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(listed.Sessions) != 0 {
			t.Fatalf("ResetSession created Sessions in %q: %#v", workspaceKey, listed.Sessions)
		}
	}
}
