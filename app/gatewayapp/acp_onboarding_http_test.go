package gatewayapp_test

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/caelis-labs/caelis/internal/testenv"
)

func TestACPConnectHTTPOffersRuntimeSetupBeforeDiscovery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
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
	server, err := local.NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	authenticator, err := controlserver.BearerTokenAuthenticator(token, appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := controlserver.Handler(controlserver.Dependencies{Services: server.Services}, controlserver.Config{
		Authenticator: authenticator, AllowedHosts: []string{"127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := testenv.NewHTTPServer(t, handler)
	client, err := httpclient.New(httpclient.Config{
		BaseURL: httpServer.URL, BearerToken: token, HTTPClient: httpServer.Client(),
		Compatibility: appserver.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}
	clients, err := httpclient.AppServerClients(client)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		WorkspaceKey: "workspace", WorkspaceDir: workspace, Surface: "tui",
		Sessions: clients.Sessions, Participants: clients.Participants, Status: clients.Status,
		Configuration: clients.Configuration, Agents: clients.Agents, Completion: clients.Completion, Plugins: clients.Plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	choices, err := adapter.CompleteSlashArg(t.Context(), "connect-acp-launcher:antigravity", "", 10)
	if err != nil || len(choices) != 2 || choices[0].Value != "install" || choices[1].Value != "manual" || choices[0].RuntimeSetup == nil {
		t.Fatalf("runtime setup choices = %#v, %v", choices, err)
	}
	if choices[0].RuntimeSetup.ArchiveURL != "" || choices[1].Display != "Manual setup" {
		t.Fatalf("setup menu must offer manual steps before downloading: %#v", choices)
	}
	// Exercise /connect's actual facade, durable command service, and HTTP
	// error projection, including a fresh user retry of the rejected setup.
	for attempt := range 2 {
		_, err := adapter.DiscoverACPConnection(t.Context(), agents.ConnectRequest{
			AdapterID: "antigravity", Launcher: agents.LauncherChoiceInstalled,
		})
		var receiptErr *appserver.CommandReceiptError
		var remoteErr *httpclient.RemoteError
		if !errors.As(err, &receiptErr) || receiptErr.Receipt.Outcome != appserver.OutcomeRejected ||
			!errors.As(err, &remoteErr) || remoteErr.StatusCode != http.StatusBadRequest {
			t.Fatalf("attempt %d: missing runtime must be rejected before launch: %v", attempt, err)
		}
		for _, want := range []string{"agy_acp_server", "not installed", "Open /connect"} {
			if !strings.Contains(err.Error(), want) || !strings.Contains(receiptErr.Receipt.Detail, want) {
				t.Fatalf("attempt %d: /connect lost %q across HTTP: %v; receipt=%#v", attempt, want, err, receiptErr.Receipt)
			}
		}
		if errorcode.CodeOf(err) != errorcode.InvalidArgument {
			t.Fatalf("attempt %d: unavailable launcher code = %q", attempt, errorcode.CodeOf(err))
		}
	}
	revision, err := host.ControlStatus().ConfigurationRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	mismatched := revision + 1
	conflicted, err := clients.Agents.PrepareACP(t.Context(), appserver.PrepareACPRequest{
		WriteBase: appserver.WriteBase{OperationID: "acp-mismatched-revision", ExpectedRevision: &mismatched},
		Request:   agents.ACPPrepareRequest{AdapterID: "antigravity", Launcher: agents.LauncherChoiceInstalled, CWD: workspace},
	})
	var remoteErr *httpclient.RemoteError
	if conflicted.Outcome != appserver.OutcomeConflicted || conflicted.Detail != "conflict" ||
		!errors.As(err, &remoteErr) || remoteErr.StatusCode != http.StatusConflict {
		t.Fatalf("configuration revision mismatch lost its conflict contract: %#v, %v", conflicted, err)
	}
}
