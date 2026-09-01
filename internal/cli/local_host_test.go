package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/caelis-labs/caelis/internal/productpaths"
	"github.com/caelis-labs/caelis/internal/servicelifecycle"
	"github.com/caelis-labs/caelis/internal/testenv"
	"github.com/caelis-labs/caelis/internal/version"
)

func TestManagedLocalHostStartsOnceAndSharesSessionsAcrossWorkspaces(t *testing.T) {
	ctx := t.Context()
	testenv.SetHome(t, t.TempDir())
	storeDir := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceA, "only-a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceB, "only-b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(workspaceB, ".agents", "skills", "workspace-b-only")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: workspace-b-only\ndescription: visible only in workspace B\n---\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	instanceID := uuid.NewString()
	build := version.BuildInfo()
	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName: "caelis-test", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "workspace-a", WorkspaceCWD: workspaceA, SkillDirs: []string{},
		Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	appServer, err := local.NewAppServer(host)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := controlserver.DefaultTokenFile(storeDir)
	token, err := controlserver.LoadOrCreateBearerToken(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := controlserver.BearerTokenAuthenticator(token, appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	info := appserver.ServerInfo{
		ProtocolVersion:     acpsdk.ProtocolVersionNumber,
		EnvelopeVersion:     appserver.EnvelopeVersion,
		APIVersion:          appserver.HTTPAPIVersion,
		DistributionVersion: build.Version, BuildID: build.BuildID, BuildKind: build.BuildKind,
		ServerID: appserver.ServerIdentity, InstanceID: instanceID,
		Capabilities: appserver.RequiredManagedHostCapabilities(), Transports: []string{"http"},
	}
	handler, err := controlserver.Handler(controlserver.Dependencies{
		Services: appServer.Services, Lifecycle: host,
	}, controlserver.Config{
		Authenticator: authenticator, AllowedHosts: []string{"127.0.0.1"}, ServerInfo: info,
		Ready: func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := testenv.NewHTTPServer(t, handler)
	var starts atomic.Int32
	start := func(request localHostStartRequest) (servicelifecycle.LaunchedProcess, error) {
		starts.Add(1)
		if request.Listen != defaultLocalHostListen || request.StoreDir != storeDir {
			t.Fatalf("local Host start request = %#v", request)
		}
		err := controlserver.PublishDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir), controlserver.DiscoveryRecord{
			SchemaVersion: controlserver.DiscoverySchemaVersion,
			ServerID:      info.ServerID, InstanceID: info.InstanceID,
			AppName: "caelis-test", PrincipalID: "local-user", PID: 1, Endpoint: server.URL,
			ProtocolVersion: info.ProtocolVersion, EnvelopeVersion: info.EnvelopeVersion, APIVersion: info.APIVersion,
			DistributionVersion: info.DistributionVersion, BuildID: info.BuildID, BuildKind: info.BuildKind,
			Capabilities: info.Capabilities, Transports: info.Transports, StartedAt: time.Now().UTC(),
		})
		return testLaunchedService(1), err
	}
	open := func(workspaceKey string, workspaceCWD string) *productClients {
		t.Helper()
		product, err := openProductClients(ctx, gatewayapp.Config{
			AppName: "caelis-test", UserID: "local-user", StoreDir: storeDir,
			WorkspaceKey: workspaceKey, WorkspaceCWD: workspaceCWD,
		}, productClientOptions{
			HTTPClient: server.Client(), LaunchLocalService: start, ServiceInstallDir: t.TempDir(),
			StartupTimeout: time.Second, PollInterval: time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = product.Close() })
		return product
	}
	clientA := open("workspace-a", workspaceA)
	clientB := open("workspace-b", workspaceB)
	if starts.Load() != 1 {
		t.Fatalf("local Host starts = %d, want 1", starts.Load())
	}
	if clientA.Mode != productClientModeManaged || clientB.Mode != productClientModeManaged || clientA.BaseURL != clientB.BaseURL {
		t.Fatalf("managed clients = %#v %#v", clientA, clientB)
	}
	for _, one := range []struct {
		client       *productClients
		sessionID    string
		workspaceKey string
		cwd          string
	}{
		{clientA, "session-a", "workspace-a", workspaceA},
		{clientB, "session-b", "workspace-b", workspaceB},
	} {
		result, err := one.client.Clients.Sessions.CreateSession(ctx, appserver.CreateSessionRequest{
			WriteBase:          appserver.WriteBase{OperationID: "create-" + one.sessionID},
			PreferredSessionID: one.sessionID, WorkspaceKey: one.workspaceKey, CWD: one.cwd,
		})
		if err != nil || result.Outcome != appserver.OutcomeCommitted {
			t.Fatalf("CreateSession(%s) = %#v, %v", one.sessionID, result, err)
		}
	}
	driverB, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		WorkspaceKey: "workspace-b", WorkspaceDir: workspaceB, Surface: "cli-tui",
		Sessions: clientB.Clients.Sessions, Participants: clientB.Clients.Participants,
		Status: clientB.Clients.Status, Configuration: clientB.Clients.Configuration,
		Agents: clientB.Clients.Agents, Completion: clientB.Clients.Completion, Plugins: clientB.Clients.Plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driverB.Close() })

	statusB, err := driverB.LightweightStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	canonicalWorkspaceB, err := filepath.EvalSymlinks(workspaceB)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusB.Session.Workspace, canonicalWorkspaceB) {
		t.Fatalf("workspace B status = %q, want %q", statusB.Session.Workspace, canonicalWorkspaceB)
	}
	resumeB, err := driverB.CompleteResume(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !resumeCandidatesContainSession(resumeB, "session-b") || resumeCandidatesContainSession(resumeB, "session-a") {
		t.Fatalf("workspace B resume candidates = %#v, want session-b only", resumeB)
	}
	filesB, err := driverB.CompleteFile(ctx, "only-", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !completionCandidatesContainValue(filesB, "only-b.txt") || completionCandidatesContainValue(filesB, "only-a.txt") {
		t.Fatalf("workspace B file candidates = %#v, want only-b.txt", filesB)
	}
	skillsB, err := driverB.CompleteSkill(ctx, "workspace-b-only", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !completionCandidatesContainValue(skillsB, "workspace-b-only") {
		t.Fatalf("workspace B skill candidates = %#v, want workspace-b-only", skillsB)
	}

	listed, err := clientA.Clients.Sessions.ListSessions(ctx, appserver.ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 2 {
		t.Fatalf("shared Host Sessions = %#v", listed.Sessions)
	}
	stateB, err := clientA.Clients.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: "session-b"})
	if err != nil {
		t.Fatal(err)
	}
	if stateB.WorkspaceKey != "workspace-b" || stateB.CWD != canonicalWorkspaceB {
		t.Fatalf("cross-client Session B = %#v", stateB)
	}
	if err := clientA.Close(); err != nil {
		t.Fatal(err)
	}
	stateA, err := clientB.Clients.Sessions.InspectSession(ctx, appserver.StateRequest{SessionID: "session-a"})
	if err != nil || stateA.WorkspaceKey != "workspace-a" {
		t.Fatalf("Session A after first client exit = %#v, %v", stateA, err)
	}
}

func TestMissingManagedHostFallsBackToEmbedded(t *testing.T) {
	storeDir := t.TempDir()
	product, err := openProductClients(t.Context(), gatewayapp.Config{
		AppName: "caelis", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "fallback", WorkspaceCWD: t.TempDir(),
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	}, productClientOptions{
		Mode: productClientModeManaged, ServiceInstallDir: t.TempDir(),
		LaunchLocalService: func(localHostStartRequest) (servicelifecycle.LaunchedProcess, error) {
			return servicelifecycle.LaunchedProcess{}, errors.New("loopback service unavailable")
		},
		EmbeddedControlEndpoint: roundTripEmbeddedControlFactory(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = product.Close() })
	if product.Mode != productClientModeEmbedded || !product.ManagedFallback {
		t.Fatalf("fallback product = mode %d fallback %v", product.Mode, product.ManagedFallback)
	}
	if err := product.Clients.Validate(); err != nil || product.Clients.Tasks == nil {
		t.Fatalf("fallback clients = %v, tasks=%T", err, product.Clients.Tasks)
	}
}

func TestAutomaticWorkspaceAddressListsAndResumesPersistedLegacyAliases(t *testing.T) {
	ctx := t.Context()
	storeDir := t.TempDir()
	workspace := t.TempDir()
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	legacySessions := map[string]string{
		"legacy-key-a": "legacy-workspace-session-a",
		"legacy-key-b": "legacy-workspace-session-b",
	}
	for legacyKey, sessionID := range legacySessions {
		legacy, err := gatewayapp.NewLocalStack(gatewayapp.Config{
			AppName: "caelis", UserID: "local-user", StoreDir: storeDir,
			WorkspaceKey: legacyKey, WorkspaceCWD: workspace,
			SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := legacy.Sessions().StartSession(ctx, session.StartSessionRequest{
			AppName: legacy.AppName(), UserID: legacy.UserID(),
			Workspace: legacy.Workspace(), PreferredSessionID: sessionID,
		}); err != nil {
			_ = legacy.Close()
			t.Fatal(err)
		}
		if err := legacy.Close(); err != nil {
			t.Fatal(err)
		}
	}

	product, err := openProductClients(ctx, gatewayapp.Config{
		AppName: "caelis", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: canonicalWorkspace, WorkspaceCWD: canonicalWorkspace,
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	}, productClientOptions{
		Mode: productClientModeEmbedded, EmbeddedControlEndpoint: roundTripEmbeddedControlFactory(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = product.Close() })
	if product.Workspace.WorkspaceKey != canonicalWorkspace || product.Workspace.WorkspaceCWD != canonicalWorkspace {
		t.Fatalf("automatic workspace = %#v, want canonical address %q", product.Workspace, canonicalWorkspace)
	}
	listed, err := product.Clients.Sessions.ListSessions(ctx, appserver.ListSessionsRequest{
		CWD: product.Workspace.WorkspaceCWD,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != len(legacySessions) {
		t.Fatalf("legacy workspace Sessions = %#v", listed.Sessions)
	}
	for _, summary := range listed.Sessions {
		if want := legacySessions[summary.WorkspaceKey]; want != summary.SessionID {
			t.Fatalf("legacy workspace Session = %#v, want %q", summary, want)
		}
		created, err := product.Clients.Sessions.CreateSession(ctx, appserver.CreateSessionRequest{
			WriteBase:          appserver.WriteBase{OperationID: "resume-" + summary.SessionID},
			PreferredSessionID: summary.SessionID,
			WorkspaceKey:       product.Workspace.WorkspaceKey,
			CWD:                product.Workspace.WorkspaceCWD,
		})
		if err != nil || created.SessionID != summary.SessionID {
			t.Fatalf("resume legacy Session %q = %#v, %v", summary.SessionID, created, err)
		}
	}
}

func TestExplicitRemoteHostRequiresWorkspaceCapabilities(t *testing.T) {
	tests := []struct {
		name                   string
		capabilities           []string
		additionalCapabilities []string
		missing                string
	}{
		{
			name:         "cwd session listing",
			capabilities: []string{appserver.CapabilityWorkspaceTrust},
			missing:      appserver.CapabilityWorkspaceCWDList,
		},
		{
			name:                   "interactive workspace trust",
			capabilities:           []string{appserver.CapabilityWorkspaceCWDList},
			additionalCapabilities: []string{appserver.CapabilityWorkspaceTrust},
			missing:                appserver.CapabilityWorkspaceTrust,
		},
		{
			name: "interactive workspace trust preflight",
			capabilities: []string{
				appserver.CapabilityWorkspaceCWDList,
				appserver.CapabilityWorkspaceTrust,
			},
			additionalCapabilities: []string{
				appserver.CapabilityWorkspaceTrust,
				appserver.CapabilityWorkspaceTrustPreflight,
			},
			missing: appserver.CapabilityWorkspaceTrustPreflight,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testenv.NewHTTPServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/api/control/v1/initialize" {
					http.NotFound(writer, request)
					return
				}
				_ = json.NewEncoder(writer).Encode(appserver.ServerInfo{
					ProtocolVersion: acpsdk.ProtocolVersionNumber,
					EnvelopeVersion: appserver.EnvelopeVersion,
					APIVersion:      appserver.HTTPAPIVersion,
					ServerID:        appserver.ServerIdentity,
					Capabilities:    test.capabilities,
				})
			}))
			workspace := t.TempDir()
			_, err := openProductClients(t.Context(), gatewayapp.Config{
				WorkspaceKey: workspace, WorkspaceCWD: workspace,
			}, productClientOptions{
				Mode: productClientModeRemote, ControlURL: server.URL, Token: "test-token", HTTPClient: server.Client(),
				WorkspaceKey: workspace, WorkspaceCWD: workspace, AdditionalRemoteCapabilities: test.additionalCapabilities,
			})
			if err == nil || !strings.Contains(err.Error(), test.missing) {
				t.Fatalf("remote attach error = %v, want missing %q capability", err, test.missing)
			}
		})
	}
}

func TestExplicitRemoteHostAllowsMissingWorkspaceTrustOutsideInteractiveFlow(t *testing.T) {
	server := testenv.NewHTTPServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/control/v1/initialize" {
			http.NotFound(writer, request)
			return
		}
		_ = json.NewEncoder(writer).Encode(appserver.ServerInfo{
			ProtocolVersion: acpsdk.ProtocolVersionNumber,
			EnvelopeVersion: appserver.EnvelopeVersion,
			APIVersion:      appserver.HTTPAPIVersion,
			ServerID:        appserver.ServerIdentity,
			Capabilities:    []string{appserver.CapabilityWorkspaceCWDList},
		})
	}))
	workspace := t.TempDir()
	product, err := openProductClients(t.Context(), gatewayapp.Config{
		WorkspaceKey: workspace, WorkspaceCWD: workspace,
	}, productClientOptions{
		Mode: productClientModeRemote, ControlURL: server.URL, Token: "test-token", HTTPClient: server.Client(),
		WorkspaceKey: workspace, WorkspaceCWD: workspace,
	})
	if err != nil {
		t.Fatalf("non-interactive remote attach: %v", err)
	}
	t.Cleanup(func() { _ = product.Close() })
}

func TestRemoteACPIngressDefaultsToHostIssuedIngressCredential(t *testing.T) {
	storeDir := t.TempDir()
	if _, err := controlserver.LoadOrCreateBearerToken(controlserver.DefaultTokenFile(storeDir)); err != nil {
		t.Fatal(err)
	}
	acpToken, err := controlserver.LoadOrCreateBearerToken(controlserver.DefaultACPIngressTokenFile(storeDir))
	if err != nil {
		t.Fatal(err)
	}
	build := version.BuildInfo()
	server := testenv.NewHTTPServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/control/v1/initialize" {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+acpToken {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(writer).Encode(appserver.ServerInfo{
			ProtocolVersion: acpsdk.ProtocolVersionNumber, EnvelopeVersion: appserver.EnvelopeVersion,
			APIVersion: appserver.HTTPAPIVersion, ServerID: appserver.ServerIdentity,
			DistributionVersion: build.Version, BuildID: build.BuildID, BuildKind: build.BuildKind,
			Capabilities: appserver.RequiredManagedHostCapabilities(), Transports: []string{"http"},
		})
	}))
	product, err := openProductClients(t.Context(), gatewayapp.Config{}, productClientOptions{
		Mode: productClientModeRemote, ControlURL: server.URL, StoreDir: storeDir,
		HTTPClient: server.Client(), ACPIngress: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := product.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedHostOwnershipPreventsEmbeddedFallback(t *testing.T) {
	storeDir := t.TempDir()
	if _, err := controlserver.LoadOrCreateBearerToken(controlserver.DefaultTokenFile(storeDir)); err != nil {
		t.Fatal(err)
	}
	build := version.BuildInfo()
	if err := controlserver.PublishDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir), controlserver.DiscoveryRecord{
		SchemaVersion: controlserver.DiscoverySchemaVersion,
		ServerID:      appserver.ServerIdentity, InstanceID: uuid.NewString(),
		AppName: "caelis", PrincipalID: "local-user", PID: 1, Endpoint: "http://127.0.0.1:7777",
		ProtocolVersion: acpsdk.ProtocolVersionNumber, EnvelopeVersion: appserver.EnvelopeVersion,
		APIVersion: appserver.HTTPAPIVersion, Capabilities: appserver.RequiredManagedHostCapabilities(),
		DistributionVersion: build.Version, BuildID: build.BuildID, BuildKind: build.BuildKind,
		Transports: []string{"http"}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	ownership, err := acquireProductHostOwnership(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ownership.Close() })
	var endpointCalls atomic.Int32
	_, err = openProductClients(t.Context(), gatewayapp.Config{
		AppName: "caelis", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "owned", WorkspaceCWD: t.TempDir(),
	}, productClientOptions{
		Mode: productClientModeManaged,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("loopback unavailable")
		})},
		EmbeddedControlEndpoint: func() (embeddedControlEndpoint, error) {
			endpointCalls.Add(1)
			return roundTripEmbeddedControlFactory(t)()
		},
	})
	if err == nil {
		t.Fatal("owned managed Host unexpectedly fell back")
	}
	if endpointCalls.Load() != 0 {
		t.Fatalf("embedded fallback endpoint calls = %d, want 0", endpointCalls.Load())
	}
}

func TestManagedACPIngressUsesHostIssuedRoleCredential(t *testing.T) {
	storeDir := t.TempDir()
	normalToken, err := controlserver.LoadOrCreateBearerToken(controlserver.DefaultTokenFile(storeDir))
	if err != nil {
		t.Fatal(err)
	}
	acpToken, err := controlserver.LoadOrCreateBearerToken(controlserver.DefaultACPIngressTokenFile(storeDir))
	if err != nil {
		t.Fatal(err)
	}
	instanceID := uuid.NewString()
	build := version.BuildInfo()
	info := appserver.ServerInfo{
		ProtocolVersion: acpsdk.ProtocolVersionNumber, EnvelopeVersion: appserver.EnvelopeVersion,
		APIVersion: appserver.HTTPAPIVersion, ServerID: appserver.ServerIdentity,
		DistributionVersion: build.Version, BuildID: build.BuildID, BuildKind: build.BuildKind,
		InstanceID: instanceID, Capabilities: appserver.RequiredManagedHostCapabilities(), Transports: []string{"http"},
	}
	var authHeaders []string
	server := testenv.NewHTTPServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/readyz":
			_ = json.NewEncoder(writer).Encode(appserver.HostStatus{
				ServerID: info.ServerID, InstanceID: info.InstanceID, Ready: true,
			})
		case "/api/control/v1/initialize":
			authHeaders = append(authHeaders, request.Header.Get("Authorization"))
			_ = json.NewEncoder(writer).Encode(info)
		default:
			http.NotFound(writer, request)
		}
	}))
	if err := controlserver.PublishDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir), controlserver.DiscoveryRecord{
		SchemaVersion: controlserver.DiscoverySchemaVersion,
		ServerID:      info.ServerID, InstanceID: info.InstanceID,
		AppName: "caelis", PrincipalID: "local-user", PID: 1, Endpoint: server.URL,
		ProtocolVersion: info.ProtocolVersion, EnvelopeVersion: info.EnvelopeVersion, APIVersion: info.APIVersion,
		DistributionVersion: info.DistributionVersion, BuildID: info.BuildID, BuildKind: info.BuildKind,
		Capabilities: info.Capabilities, Transports: info.Transports, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := attachManagedHostClient(t.Context(), productClientOptions{
		StoreDir: storeDir, AppName: "caelis", UserID: "local-user", HTTPClient: server.Client(), ACPIngress: true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(authHeaders) != 2 || authHeaders[0] != "Bearer "+normalToken || authHeaders[1] != "Bearer "+acpToken {
		t.Fatalf("initialize authorization headers = %#v, want normal discovery then ACP ingress", authHeaders)
	}
}

func resumeCandidatesContainSession(candidates []appserver.ResumeCandidate, sessionID string) bool {
	for _, candidate := range candidates {
		if candidate.SessionID == sessionID {
			return true
		}
	}
	return false
}

func completionCandidatesContainValue(candidates []appserver.CompletionCandidate, value string) bool {
	for _, candidate := range candidates {
		if candidate.Value == value {
			return true
		}
	}
	return false
}

func TestLocalHostEnvironmentDoesNotInheritClientOrNetworkAuthority(t *testing.T) {
	input := []string{
		"PATH=/usr/bin", "CAELIS_CONTROL_URL=http://127.0.0.1:1", "CAELIS_CONTROL_EMBEDDED=1",
		"CAELIS_CONTROL_TOKEN=secret", "CAELIS_CONTROL_TOKEN_FILE=/tmp/token", "CAELIS_CONTROL_LISTEN=0.0.0.0:1",
		"CAELIS_CONTROL_ALLOWED_HOSTS=example.test", "CAELIS_CONTROL_TLS_CERT=cert", "CAELIS_CONTROL_TLS_KEY=key",
		"CAELIS_MEMORY_BOT_ID=bot-a", "CAELIS_MEMORY_AUDIENCE=private",
	}
	got := localHostEnvironment(input)
	if len(got) != 3 || got[0] != "PATH=/usr/bin" || got[1] != "CAELIS_MEMORY_BOT_ID=bot-a" || got[2] != "CAELIS_MEMORY_AUDIENCE=private" {
		t.Fatalf("local Host environment = %q", got)
	}
}

func TestDetachedLocalHostAbortKillsAndReapsExactChild(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestDetachedLocalHostAbortHelper$")
	command.Env = append(os.Environ(), "CAELIS_ABORT_HELPER=1")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	handle := &detachedLocalHostProcess{command: command}
	if err := handle.abort(); err != nil {
		t.Fatal(err)
	}
	if command.ProcessState == nil {
		t.Fatalf("aborted child was not reaped: %#v", command.ProcessState)
	}
	if err := handle.abort(); err != nil {
		t.Fatalf("second abort = %v", err)
	}
}

func TestDetachedLocalHostAbortHelper(t *testing.T) {
	if os.Getenv("CAELIS_ABORT_HELPER") != "1" {
		return
	}
	time.Sleep(10 * time.Minute)
}

func TestManagedLocalHostRecoversStaleDiscoveryAndConvergesConcurrentClients(t *testing.T) {
	storeDir := t.TempDir()
	token, err := controlserver.LoadOrCreateBearerToken(controlserver.DefaultTokenFile(storeDir))
	if err != nil {
		t.Fatal(err)
	}
	instanceID := uuid.NewString()
	build := version.BuildInfo()
	info := appserver.ServerInfo{
		ProtocolVersion: acpsdk.ProtocolVersionNumber, EnvelopeVersion: appserver.EnvelopeVersion,
		APIVersion: appserver.HTTPAPIVersion, ServerID: appserver.ServerIdentity,
		DistributionVersion: build.Version, BuildID: build.BuildID, BuildKind: build.BuildKind,
		InstanceID: instanceID, Capabilities: appserver.RequiredManagedHostCapabilities(), Transports: []string{"http"},
	}
	server := testenv.NewHTTPServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/readyz":
			_ = json.NewEncoder(writer).Encode(appserver.HostStatus{
				ServerID: info.ServerID, InstanceID: info.InstanceID, Ready: true,
			})
		case "/api/control/v1/initialize":
			if request.Header.Get("Authorization") != "Bearer "+token {
				http.Error(writer, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(writer).Encode(info)
		default:
			http.NotFound(writer, request)
		}
	}))
	if err := controlserver.PublishDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir), controlserver.DiscoveryRecord{
		SchemaVersion: controlserver.DiscoverySchemaVersion,
		ServerID:      appserver.ServerIdentity, InstanceID: uuid.NewString(),
		AppName: "caelis-test", PrincipalID: "local-user", PID: 1,
		Endpoint: "http://127.0.0.1:65534", ProtocolVersion: acpsdk.ProtocolVersionNumber,
		EnvelopeVersion: appserver.EnvelopeVersion, APIVersion: appserver.HTTPAPIVersion,
		DistributionVersion: "v1.2.2", BuildID: "stale-build", BuildKind: "release",
		Capabilities: appserver.RequiredManagedHostCapabilities(), Transports: []string{"http"}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	var started atomic.Bool
	baseTransport := server.Client().Transport
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if !started.Load() {
			return nil, fmt.Errorf("stale Host transport unavailable")
		}
		return baseTransport.RoundTrip(request)
	})}
	var publishOnce sync.Once
	var publishErr error
	var startAttempts atomic.Int32
	start := func(localHostStartRequest) (servicelifecycle.LaunchedProcess, error) {
		startAttempts.Add(1)
		publishOnce.Do(func() {
			publishErr = controlserver.PublishDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir), controlserver.DiscoveryRecord{
				SchemaVersion: controlserver.DiscoverySchemaVersion,
				ServerID:      info.ServerID, InstanceID: info.InstanceID,
				AppName: "caelis-test", PrincipalID: "local-user", PID: 2, Endpoint: server.URL,
				ProtocolVersion: info.ProtocolVersion, EnvelopeVersion: info.EnvelopeVersion, APIVersion: info.APIVersion,
				DistributionVersion: info.DistributionVersion, BuildID: info.BuildID, BuildKind: info.BuildKind,
				Capabilities: info.Capabilities, Transports: info.Transports, StartedAt: time.Now().UTC(),
			})
			started.Store(publishErr == nil)
		})
		return testLaunchedService(2), publishErr
	}

	const clientCount = 12
	results := make(chan *productClients, clientCount)
	errors := make(chan error, clientCount)
	var wait sync.WaitGroup
	for index := range clientCount {
		wait.Add(1)
		go func() {
			defer wait.Done()
			product, err := openProductClients(t.Context(), gatewayapp.Config{
				AppName: "caelis-test", UserID: "local-user", StoreDir: storeDir,
				WorkspaceKey: fmt.Sprintf("workspace-%d", index), WorkspaceCWD: t.TempDir(),
			}, productClientOptions{
				HTTPClient: httpClient, LaunchLocalService: start, ServiceInstallDir: t.TempDir(),
				StartupTimeout: time.Second, PollInterval: time.Millisecond,
			})
			if err != nil {
				errors <- err
				return
			}
			results <- product
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Errorf("concurrent managed attach: %v", err)
	}
	attached := 0
	for product := range results {
		attached++
		if product.Mode != productClientModeManaged || product.BaseURL != server.URL {
			t.Errorf("managed client = %#v", product)
		}
		_ = product.Close()
	}
	if attached != clientCount {
		t.Fatalf("attached clients = %d, want %d", attached, clientCount)
	}
	if !started.Load() || startAttempts.Load() == 0 {
		t.Fatalf("stale discovery recovery started=%v attempts=%d", started.Load(), startAttempts.Load())
	}
}

func TestManagedLocalHostRejectsDiscoveryEndpointInstanceMismatch(t *testing.T) {
	storeDir := t.TempDir()
	token, err := controlserver.LoadOrCreateBearerToken(controlserver.DefaultTokenFile(storeDir))
	if err != nil {
		t.Fatal(err)
	}
	serverInstanceID := uuid.NewString()
	server := testenv.NewHTTPServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/readyz":
			_, _ = fmt.Fprintf(writer, `{"server_id":%q,"instance_id":%q,"ready":true}`, appserver.ServerIdentity, serverInstanceID)
		case "/api/control/v1/initialize":
			if request.Header.Get("Authorization") != "Bearer "+token {
				http.Error(writer, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(writer).Encode(appserver.ServerInfo{
				ProtocolVersion: acpsdk.ProtocolVersionNumber, EnvelopeVersion: appserver.EnvelopeVersion,
				APIVersion: appserver.HTTPAPIVersion, ServerID: appserver.ServerIdentity,
				DistributionVersion: "v1.2.3", BuildID: "server-build", BuildKind: "release",
				InstanceID: serverInstanceID, Capabilities: appserver.RequiredManagedHostCapabilities(), Transports: []string{"http"},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	if err := controlserver.PublishDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir), controlserver.DiscoveryRecord{
		SchemaVersion: controlserver.DiscoverySchemaVersion,
		ServerID:      appserver.ServerIdentity, InstanceID: uuid.NewString(),
		AppName: "caelis", PrincipalID: "local-user", PID: 1, Endpoint: server.URL,
		ProtocolVersion: acpsdk.ProtocolVersionNumber, EnvelopeVersion: appserver.EnvelopeVersion,
		APIVersion: appserver.HTTPAPIVersion, Capabilities: appserver.RequiredManagedHostCapabilities(),
		DistributionVersion: "v1.2.3", BuildID: "discovery-build", BuildKind: "release",
		Transports: []string{"http"}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	var starts atomic.Int32
	_, err = openProductClients(t.Context(), gatewayapp.Config{
		StoreDir: storeDir, WorkspaceKey: "workspace", WorkspaceCWD: t.TempDir(),
	}, productClientOptions{
		HTTPClient: server.Client(), LaunchLocalService: func(localHostStartRequest) (servicelifecycle.LaunchedProcess, error) {
			starts.Add(1)
			return testLaunchedService(1), nil
		},
		StartupTimeout: 20 * time.Millisecond, PollInterval: time.Millisecond,
	})
	if err == nil || err.Error() != "caelis could not start; try again or run `caelis doctor`" {
		t.Fatalf("managed instance mismatch product error = %v", err)
	}
	if starts.Load() != 0 {
		t.Fatalf("instance mismatch started another Host %d times", starts.Load())
	}
	assertManagedDiagnosticContains(t, storeDir, "does not match")
}

func TestManagedLifecycleProbeIgnoresSurfaceCapabilityCompatibility(t *testing.T) {
	storeDir := t.TempDir()
	token, err := controlserver.LoadOrCreateBearerToken(controlserver.DefaultTokenFile(storeDir))
	if err != nil {
		t.Fatal(err)
	}
	instanceID := uuid.NewString()
	info := appserver.ServerInfo{
		ProtocolVersion: acpsdk.ProtocolVersionNumber, EnvelopeVersion: appserver.EnvelopeVersion,
		APIVersion: appserver.HTTPAPIVersion, ServerID: appserver.ServerIdentity,
		DistributionVersion: "v2.0.0", BuildID: "future-build", BuildKind: version.BuildKindRelease,
		InstanceID: instanceID, Capabilities: []string{"future-surface-v2"}, Transports: []string{"http"},
	}
	server := testenv.NewHTTPServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/readyz":
			_ = json.NewEncoder(writer).Encode(appserver.HostStatus{
				ServerID: info.ServerID, InstanceID: info.InstanceID, Ready: true,
			})
		case "/api/control/v1/initialize":
			if request.Header.Get("Authorization") != "Bearer "+token {
				http.Error(writer, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(writer).Encode(info)
		case "/api/control/v1/host/shutdown":
			if request.Header.Get("Authorization") != "Bearer "+token {
				http.Error(writer, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(writer).Encode(appserver.HostStatus{
				ServerID: info.ServerID, InstanceID: info.InstanceID, Ready: false,
			})
			if err := controlserver.RemoveDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir), info.InstanceID); err != nil {
				t.Errorf("remove discovery: %v", err)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	if err := controlserver.PublishDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir), controlserver.DiscoveryRecord{
		SchemaVersion: controlserver.DiscoverySchemaVersion,
		ServerID:      info.ServerID, InstanceID: info.InstanceID,
		AppName: "caelis", PrincipalID: "local-user", PID: 1234, Endpoint: server.URL,
		ProtocolVersion: info.ProtocolVersion, EnvelopeVersion: info.EnvelopeVersion, APIVersion: info.APIVersion,
		DistributionVersion: info.DistributionVersion, BuildID: info.BuildID, BuildKind: info.BuildKind,
		Capabilities: info.Capabilities, Transports: info.Transports, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	options := productClientOptions{
		AppName: "caelis", UserID: "local-user", StoreDir: storeDir, HTTPClient: server.Client(),
	}
	inspection := inspectManagedHost(t.Context(), options)
	if inspection.Probe.State != servicelifecycle.ProbeReady {
		t.Fatalf("lifecycle probe rejected future capabilities: %#v", inspection.Probe)
	}
	if _, _, err := attachManagedHostClient(t.Context(), options); err == nil || !strings.Contains(err.Error(), "needs an update") {
		t.Fatalf("Surface attach error = %v, want update requirement", err)
	}
	if err := requestManagedServiceShutdown(t.Context(), options, servicelifecycle.Status{
		Identity: servicelifecycle.Identity{
			DistributionVersion: info.DistributionVersion, BuildID: info.BuildID, BuildKind: info.BuildKind,
		},
		InstanceID: info.InstanceID, PID: 1234, Endpoint: server.URL,
	}); err != nil {
		t.Fatalf("lifecycle shutdown rejected future capabilities: %v", err)
	}
}

func TestManagedProductFailureKeepsImplementationDetailsInPrivateLog(t *testing.T) {
	storeDir := t.TempDir()
	err := managedProductFailure(storeDir, "start", errors.New("servicelifecycle: hidden implementation detail"), false)
	if got := err.Error(); got != "caelis could not start; try again or run `caelis doctor`" {
		t.Fatalf("product error = %q", got)
	}
	logPath := filepath.Join(productpaths.ServiceLogDir(storeDir), localHostLogFilename)
	raw, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(raw), "servicelifecycle: hidden implementation detail") {
		t.Fatalf("diagnostic log = %q", raw)
	}
	info, statErr := os.Stat(logPath)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("diagnostic log mode = %o", info.Mode().Perm())
	}
}

func TestManagedProductFailureSurfacesPermissionBlocker(t *testing.T) {
	storeDir := t.TempDir()
	err := managedProductFailure(storeDir, "start", errors.New("open store: permission denied"), false)
	if err == nil || err.Error() != "caelis could not start: permission denied" {
		t.Fatalf("permission product error = %v", err)
	}
	diagnostic := managedProductFailure(storeDir, "start", errors.New("servicelifecycle: bind failed inside restricted sandbox"), true)
	if diagnostic == nil || !strings.Contains(diagnostic.Error(), "restricted sandbox") {
		t.Fatalf("doctor-facing product error = %v, want real cause", diagnostic)
	}
}

func TestManagedLocalHostRejectsDifferentAppOrPrincipalForSameStore(t *testing.T) {
	storeDir := t.TempDir()
	if _, err := controlserver.LoadOrCreateBearerToken(controlserver.DefaultTokenFile(storeDir)); err != nil {
		t.Fatal(err)
	}
	if err := controlserver.PublishDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir), controlserver.DiscoveryRecord{
		SchemaVersion: controlserver.DiscoverySchemaVersion,
		ServerID:      appserver.ServerIdentity, InstanceID: uuid.NewString(),
		AppName: "caelis", PrincipalID: "owner-a", PID: 1, Endpoint: "http://127.0.0.1:65534",
		ProtocolVersion: acpsdk.ProtocolVersionNumber, EnvelopeVersion: appserver.EnvelopeVersion,
		APIVersion: appserver.HTTPAPIVersion, Capabilities: appserver.RequiredManagedHostCapabilities(),
		DistributionVersion: "v1.2.3", BuildID: "test-build", BuildKind: "release",
		Transports: []string{"http"}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	var starts atomic.Int32
	_, err := openProductClients(t.Context(), gatewayapp.Config{
		AppName: "other-app", UserID: "owner-b", StoreDir: storeDir,
		WorkspaceKey: "workspace", WorkspaceCWD: t.TempDir(),
	}, productClientOptions{
		LaunchLocalService: func(localHostStartRequest) (servicelifecycle.LaunchedProcess, error) {
			starts.Add(1)
			return testLaunchedService(1), nil
		},
		StartupTimeout: 20 * time.Millisecond, PollInterval: time.Millisecond,
	})
	if err == nil || err.Error() != "caelis could not start; try again or run `caelis doctor`" {
		t.Fatalf("managed Host scope product error = %v", err)
	}
	if starts.Load() != 0 {
		t.Fatalf("scope mismatch started another Host %d times", starts.Load())
	}
	assertManagedDiagnosticContains(t, storeDir, "local Control Host scope")
}

func TestLocalHostStatusAndStopUseDiscoveredInstanceWithoutLoopbackListener(t *testing.T) {
	storeDir := t.TempDir()
	token, err := controlserver.LoadOrCreateBearerToken(controlserver.DefaultTokenFile(storeDir))
	if err != nil {
		t.Fatal(err)
	}
	instanceID := uuid.NewString()
	info := appserver.ServerInfo{
		ProtocolVersion: acpsdk.ProtocolVersionNumber, EnvelopeVersion: appserver.EnvelopeVersion,
		APIVersion: appserver.HTTPAPIVersion, ServerID: appserver.ServerIdentity,
		DistributionVersion: "v1.2.3", BuildID: "test-build", BuildKind: "release",
		InstanceID: instanceID, Capabilities: appserver.RequiredManagedHostCapabilities(), Transports: []string{"http"},
	}
	discoveryPath := controlserver.DefaultDiscoveryFile(storeDir)
	server := testenv.NewHTTPServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/readyz":
			_ = json.NewEncoder(writer).Encode(appserver.HostStatus{
				ServerID: info.ServerID, InstanceID: info.InstanceID, Ready: true,
			})
		case "/api/control/v1/initialize":
			if request.Header.Get("Authorization") != "Bearer "+token {
				http.Error(writer, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(writer).Encode(info)
		case "/api/control/v1/host/shutdown":
			if request.Header.Get("Authorization") != "Bearer "+token {
				http.Error(writer, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(writer).Encode(appserver.HostStatus{
				ServerID: info.ServerID, InstanceID: info.InstanceID, Ready: false,
			})
			if err := controlserver.RemoveDiscoveryRecord(discoveryPath, instanceID); err != nil {
				t.Errorf("remove discovery: %v", err)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	if err := controlserver.PublishDiscoveryRecord(discoveryPath, controlserver.DiscoveryRecord{
		SchemaVersion: controlserver.DiscoverySchemaVersion,
		ServerID:      info.ServerID, InstanceID: info.InstanceID,
		AppName: "caelis-test", PrincipalID: "local-user", PID: 4321, Endpoint: server.URL,
		ProtocolVersion: info.ProtocolVersion, EnvelopeVersion: info.EnvelopeVersion, APIVersion: info.APIVersion,
		DistributionVersion: info.DistributionVersion, BuildID: info.BuildID, BuildKind: info.BuildKind,
		Capabilities: info.Capabilities, Transports: info.Transports, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	config := gatewayapp.Config{AppName: "caelis-test", UserID: "local-user", StoreDir: storeDir}
	options := productClientOptions{
		Mode: productClientModeManaged, AppName: config.AppName, UserID: config.UserID,
		StoreDir: storeDir, HTTPClient: server.Client(), PollInterval: time.Millisecond,
	}
	var output bytes.Buffer
	if err := runLocalHostCommandWithOptions(t.Context(), "status", config, options, outputJSON, &output); err != nil {
		t.Fatal(err)
	}
	var status localHostCommandResult
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status != (localHostCommandResult{
		State: "running", InstanceID: instanceID, PID: 4321, Endpoint: server.URL,
		DistributionVersion: info.DistributionVersion, BuildID: info.BuildID, BuildKind: info.BuildKind,
	}) {
		t.Fatalf("Host status = %#v", status)
	}
	output.Reset()
	if err := runLocalHostCommandWithOptions(t.Context(), "stop", config, options, outputJSON, &output); err != nil {
		t.Fatal(err)
	}
	var stopped localHostCommandResult
	if err := json.Unmarshal(output.Bytes(), &stopped); err != nil {
		t.Fatal(err)
	}
	if stopped != (localHostCommandResult{State: "stopped"}) {
		t.Fatalf("Host stop = %#v", stopped)
	}
}

func TestLocalHostTextStatusIncludesSelectedBuildIdentity(t *testing.T) {
	var output bytes.Buffer
	err := writeLocalHostCommandResult(&output, outputText, localHostCommandResult{
		State: "running", DistributionVersion: "v1.2.3", BuildID: "build-123", BuildKind: version.BuildKindRelease,
		InstanceID: "instance", PID: 42, Endpoint: "http://127.0.0.1:7777",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"distribution_version: v1.2.3", "build_id: build-123", "build_kind: release"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("text status = %q, want %q", output.String(), want)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testLaunchedService(pid int) servicelifecycle.LaunchedProcess {
	return servicelifecycle.LaunchedProcess{
		PID:     pid,
		Abort:   func() error { return nil },
		Release: func() error { return nil },
	}
}

func assertManagedDiagnosticContains(t *testing.T, storeDir string, want string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(productpaths.ServiceLogDir(storeDir), localHostLogFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), want) {
		t.Fatalf("managed diagnostic = %q, want %q", raw, want)
	}
}
