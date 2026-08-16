package gatewayapp_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/internal/testenv"
)

// TestControlHostTwoClientsTwoSessionsInjectedTransport proves the product
// multi-client topology without binding a host kernel socket: one Host
// authority, two independent HTTP clients, and two concurrent Sessions share
// feed and operation identity through an in-memory HTTP transport.
func TestControlHostTwoClientsTwoSessionsInjectedTransport(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	workspaceA := filepath.Join(root, "workspace-a")
	workspaceB := filepath.Join(root, "workspace-b")
	for _, dir := range []string{storeDir, workspaceA, workspaceB} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName: "caelis-test", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "workspace-a", WorkspaceCWD: workspaceA,
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })

	appServer, err := local.NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	authenticator, err := controlserver.BearerTokenAuthenticator(token, appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := controlserver.Handler(controlserver.Dependencies{
		Services: appServer.Services,
	}, controlserver.Config{
		Authenticator: authenticator,
		AllowedHosts:  []string{"127.0.0.1", "localhost"},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := testenv.NewHTTPServer(t, handler)

	clientA, err := httpclient.New(httpclient.Config{
		BaseURL: httpServer.URL, BearerToken: token, EventBuffer: 64,
		HTTPClient:    httpServer.Client(),
		Compatibility: appserver.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}
	clientB, err := httpclient.New(httpclient.Config{
		BaseURL: httpServer.URL, BearerToken: token, EventBuffer: 64,
		HTTPClient:    httpServer.Client(),
		Compatibility: appserver.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}
	clientsA, err := httpclient.AppServerClients(clientA)
	if err != nil {
		t.Fatal(err)
	}
	clientsB, err := httpclient.AppServerClients(clientB)
	if err != nil {
		t.Fatal(err)
	}
	if clientsA.Tasks == nil || clientsB.Tasks == nil {
		t.Fatal("remote Task clients are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if info, err := clientsA.Sessions.Initialize(ctx); err != nil || info.APIVersion != appserver.HTTPAPIVersion {
		t.Fatalf("client A Initialize = %#v, %v", info, err)
	}
	if info, err := clientsB.Sessions.Initialize(ctx); err != nil || info.APIVersion != appserver.HTTPAPIVersion {
		t.Fatalf("client B Initialize = %#v, %v", info, err)
	}

	sessionA, err := clientsA.Sessions.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "create-session-a"},
		PreferredSessionID: "session-a", WorkspaceKey: "workspace-a", CWD: workspaceA, Title: "Session A",
	})
	if err != nil || sessionA.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("CreateSession A = %#v, %v", sessionA, err)
	}
	sessionB, err := clientsB.Sessions.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "create-session-b"},
		PreferredSessionID: "session-b", WorkspaceKey: "workspace-b", CWD: workspaceB, Title: "Session B",
	})
	if err != nil || sessionB.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("CreateSession B = %#v, %v", sessionB, err)
	}

	stateAFromB, err := clientsB.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: "session-a"})
	if err != nil || stateAFromB.SessionID != "session-a" || stateAFromB.WorkspaceKey != "workspace-a" {
		t.Fatalf("client B inspect Session A = %#v, %v", stateAFromB, err)
	}
	stateBFromA, err := clientsA.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: "session-b"})
	if err != nil || stateBFromA.SessionID != "session-b" || stateBFromA.WorkspaceKey != "workspace-b" {
		t.Fatalf("client A inspect Session B = %#v, %v", stateBFromA, err)
	}

	var wait sync.WaitGroup
	errCh := make(chan error, 4)
	wait.Add(2)
	go func() {
		defer wait.Done()
		reconnected, reconnectErr := clientsA.Sessions.Reconnect(ctx, appserver.ReconnectRequest{SessionID: "session-b"})
		if reconnectErr != nil {
			errCh <- reconnectErr
			return
		}
		defer reconnected.Subscription.Close()
		if reconnected.State.SessionID != "session-b" || reconnected.State.WorkspaceKey != "workspace-b" {
			errCh <- errors.New("client A reconnect to Session B returned the wrong Session state")
		}
		state, inspectErr := clientsA.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: "session-a"})
		if inspectErr != nil {
			errCh <- inspectErr
			return
		}
		if state.SessionID != "session-a" {
			errCh <- errors.New("client A inspect while watching Session B returned the wrong Session")
		}
	}()
	go func() {
		defer wait.Done()
		reconnected, reconnectErr := clientsB.Sessions.Reconnect(ctx, appserver.ReconnectRequest{SessionID: "session-a"})
		if reconnectErr != nil {
			errCh <- reconnectErr
			return
		}
		defer reconnected.Subscription.Close()
		if reconnected.State.SessionID != "session-a" || reconnected.State.WorkspaceKey != "workspace-a" {
			errCh <- errors.New("client B reconnect to Session A returned the wrong Session state")
		}
		state, inspectErr := clientsB.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: "session-b"})
		if inspectErr != nil {
			errCh <- inspectErr
			return
		}
		if state.SessionID != "session-b" {
			errCh <- errors.New("client B inspect while watching Session A returned the wrong Session")
		}
	}()
	wait.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	if retry, err := clientsB.Sessions.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "create-session-a"},
		PreferredSessionID: "session-a", WorkspaceKey: "workspace-a", CWD: workspaceA, Title: "Session A",
	}); err != nil {
		t.Fatalf("idempotent CreateSession retry: %v", err)
	} else if retry.SessionID != "session-a" {
		t.Fatalf("idempotent CreateSession retry Session = %q", retry.SessionID)
	}

	listed, err := clientsA.Sessions.ListSessions(ctx, appserver.ListSessionsRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) < 2 {
		t.Fatalf("ListSessions returned %d Sessions, want at least 2", len(listed.Sessions))
	}

	httpServer.Close()
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName: "caelis-test", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "workspace-a", WorkspaceCWD: workspaceA,
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatalf("restart Host: %v", err)
	}
	defer func() { _ = restarted.Close() }()
	if _, err := restarted.ControlClient().InspectSession(ctx, appserver.Principal{ID: "local-user"}, appserver.StateRequest{SessionID: "session-a"}); err != nil {
		t.Fatalf("durable Session A after Host restart: %v", err)
	}
	if _, err := restarted.ControlClient().InspectSession(ctx, appserver.Principal{ID: "local-user"}, appserver.StateRequest{SessionID: "session-b"}); err != nil {
		t.Fatalf("durable Session B after Host restart: %v", err)
	}
}
