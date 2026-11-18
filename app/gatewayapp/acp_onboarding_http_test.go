package gatewayapp_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/adapterhost"
	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/caelis-labs/caelis/internal/testenv"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func TestACPConnectHTTPCommitsSelectedModelsInOneRevision(t *testing.T) {
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
	connectOperation := make(chan string, 1)
	httpServer := testenv.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/control/v1/agents/connect-acp" {
			select {
			case connectOperation <- r.Header.Get("Idempotency-Key"):
			default:
			}
		}
		handler.ServeHTTP(w, r)
	}))
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
	request := agents.ConnectRequest{
		AdapterID: "custom", Launcher: agents.LauncherChoiceCommand,
		CommandLine: strings.Join([]string{strconv.Quote(os.Args[0]), "-test.run=^TestGatewayACPOnboardingHelperProcess$", "--", "caelis-onboarding-helper", "multimodel", strconv.Quote(filepath.Join(root, "starts"))}, " "),
	}
	roster, err := adapter.DiscoverACPConnection(t.Context(), request)
	if err != nil || len(roster.Models) != 3 || roster.SelectedModelID != "" {
		t.Fatalf("discover roster = %#v, %v", roster, err)
	}
	revision, err := host.ControlStatus().ConfigurationRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	invalid := request
	invalid.ModelIDs = []string{"vendor/alpha", "vendor/unavailable"}
	if _, err := adapter.ConnectACP(t.Context(), invalid); err == nil || !strings.Contains(err.Error(), `prepare ACP model "vendor/unavailable"`) {
		t.Fatalf("unavailable model error = %v", err)
	}
	if after, err := host.ControlStatus().ConfigurationRevision(t.Context()); err != nil || after != revision {
		t.Fatalf("revision after failed model preparation = %d, %v", after, err)
	}
	request.ModelIDs = []string{"vendor/beta", "vendor/alpha"}
	connected, err := adapter.ConnectACP(t.Context(), request)
	if err != nil || len(connected.Profiles) != 2 || connected.Connection.ID == "" {
		t.Fatalf("connect = %#v, %v", connected, err)
	}
	doc, err := configstore.New(filepath.Join(root, "store")).LoadContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if doc.ConfigurationRevision != revision+1 || len(doc.ExternalAgents.Connections) != 1 || len(doc.ExternalAgents.Agents) != 1 || len(doc.ExternalAgents.Discoveries) != 2 {
		t.Fatalf("expected one revision, one endpoint, and two discoveries, got revision %d and %#v", doc.ConfigurationRevision, doc.ExternalAgents)
	}
	for i, expectedEffort := range []string{"high", "low"} {
		committed := connected.Profiles[i]
		stored, ok := modelprofile.Lookup(doc.ModelProfiles, committed.ID)
		if !ok || stored.DisplayName != committed.DisplayName || stored.Backend.ACP.RemoteModelID != request.ModelIDs[i] ||
			stored.Backend.ACP.AgentID != doc.ExternalAgents.Agents[0].ID || stored.Effort.DefaultEffort != expectedEffort {
			t.Fatalf("profile %d = %#v, committed %#v", i, stored, committed)
		}
	}
	select {
	case operationID := <-connectOperation:
		observed, err := client.Operation(t.Context(), operationID)
		if err != nil || observed.Outcome != appserver.OutcomeCommitted || observed.Revision != doc.ConfigurationRevision ||
			observed.ACPConnect == nil || !reflect.DeepEqual(observed.ACPConnect.Profiles, connected.Profiles) {
			t.Fatalf("stored batch receipt = %#v, %v", observed, err)
		}
	default:
		t.Fatal("batch connect did not use the ACP connect route")
	}

	retired := agents.ACPPrepareRequest{AdapterID: "custom", Launcher: agents.LauncherChoiceCommand, CommandLine: request.CommandLine, CWD: workspace,
		Install: &agents.RuntimeInstallation{Directory: filepath.Join(root, "retired"), ArchiveURL: "https://example.invalid/retired.zip"}}
	rejected, err := clients.Agents.PrepareACP(t.Context(), appserver.PrepareACPRequest{
		WriteBase: appserver.WriteBase{OperationID: "acp-retired-install", ExpectedRevision: &doc.ConfigurationRevision}, Request: retired,
	})
	var remoteErr *httpclient.RemoteError
	if rejected.Outcome != appserver.OutcomeRejected || errorcode.CodeOf(err) != errorcode.InvalidArgument ||
		!errors.As(err, &remoteErr) || remoteErr.StatusCode != http.StatusBadRequest || !strings.Contains(rejected.Detail, "ACP runtime installation is no longer supported") {
		t.Fatalf("retired installation = %#v, %v", rejected, err)
	}
	observed, err := client.Operation(t.Context(), rejected.OperationID)
	if err != nil || !reflect.DeepEqual(observed, rejected) {
		t.Fatalf("retired installation receipt = %#v, %v; want %#v", observed, err, rejected)
	}

	t.Run("prompt through selected ACP profile", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		// A managed Host supplies the collaboration MCP endpoint when the
		// external controller opens its Session for the first prompt.
		host.SetBuiltInChildControl(httpServer.URL, filepath.Join(root, "child.token"))
		if _, err := adapter.UseModel(ctx, connected.Profiles[0].ID, "", false); err != nil {
			t.Fatal(err)
		}
		created, err := clients.Sessions.CreateSession(ctx, appserver.CreateSessionRequest{
			WriteBase: appserver.WriteBase{OperationID: "create-acp-prompt-session"}, WorkspaceKey: "workspace", CWD: workspace,
		})
		if err != nil || created.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("create ACP Session = %#v, %v", created, err)
		}
		state, err := clients.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
		if err != nil || state.Controller.Placement.ProfileID != connected.Profiles[0].ID {
			t.Fatalf("ACP Session controller = %#v, %v", state.Controller, err)
		}
		turns, err := appserver.NewSessionTurnClient(clients.Sessions)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := headless.RunSessionOnce(ctx, turns, appserver.SessionTurnStartRequest{
			SessionID: created.SessionID, Input: "Hello from the ACP regression test",
		}, headless.Options{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Codex prerequisites", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		const diagnostic = `install Codex so "codex" is available on PATH`
		service := host.AdapterHost()
		descriptor, err := service.Inspect(t.Context(), adapterhost.CodexAdapterID)
		if err != nil || descriptor.Available || descriptor.Diagnostic != diagnostic {
			t.Fatalf("missing Codex inspection = %#v, %v", descriptor, err)
		}
		request := agents.ConnectRequest{AdapterID: adapterhost.CodexAdapterID, Launcher: agents.LauncherChoiceHosted}
		_, err = adapter.DiscoverACPConnection(t.Context(), request)
		const message = `Install Codex so "codex" is available on the Host's PATH.`
		var remote *httpclient.RemoteError
		if errorcode.CodeOf(err) != errorcode.InvalidArgument || !errors.As(err, &remote) || remote.StatusCode != http.StatusBadRequest || remote.Detail != message {
			t.Fatalf("missing Codex discovery error = %v", err)
		}

		if err := service.(interface{ Close() error }).Close(); err != nil {
			t.Fatal(err)
		}
		_, err = adapter.DiscoverACPConnection(t.Context(), request)
		if errorcode.CodeOf(err) != errorcode.FailedPrecondition || !errors.As(err, &remote) || remote.StatusCode != http.StatusBadRequest || remote.Detail != "conflict" {
			t.Fatalf("shut down adapter discovery error = %v", err)
		}
	})
}
