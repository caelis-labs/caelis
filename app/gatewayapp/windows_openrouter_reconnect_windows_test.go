package gatewayapp_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/internal/testenv"
	"golang.org/x/sys/windows"
)

// TestWindowsOpenRouterReconnectPreservesCustomReasoningLevels exercises the
// real Windows Host, config store, HTTP AppServer, and client path reported in
// issue #2. The API key is intentionally not validated by /connect.
func TestWindowsOpenRouterReconnectPreservesCustomReasoningLevels(t *testing.T) {
	testCases := []struct {
		name              string
		cancellationPoint string
		reasoningLevels   []string
		acceptedEffort    string
		rejectedEffort    string
		wantDefaultEffort string
	}{
		{name: "before-request", cancellationPoint: "before-request", reasoningLevels: []string{"low", "high", "max"}, acceptedEffort: "high", rejectedEffort: "medium", wantDefaultEffort: "high"},
		{name: "during-persistence", cancellationPoint: "during-persistence", reasoningLevels: []string{"low", "high", "max"}, acceptedEffort: "high", rejectedEffort: "medium", wantDefaultEffort: "high"},
		{name: "configured-capabilities-remain-authoritative", cancellationPoint: "before-request", reasoningLevels: []string{"low", "max"}, acceptedEffort: "max", rejectedEffort: "high"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			storeDir := filepath.Join(root, "store")
			workspace := filepath.Join(root, "workspace")
			for _, dir := range []string{storeDir, workspace} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			host, err := gatewayapp.NewLocalStack(gatewayapp.Config{
				AppName: "caelis-windows-reconnect-test", UserID: "local-user", StoreDir: storeDir,
				WorkspaceKey: "workspace", WorkspaceCWD: workspace,
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
			handler, err := controlserver.Handler(controlserver.Dependencies{Services: appServer.Services}, controlserver.Config{
				Authenticator: authenticator,
				AllowedHosts:  []string{"127.0.0.1", "localhost"},
			})
			if err != nil {
				t.Fatal(err)
			}
			server := testenv.NewHTTPServer(t, handler)
			client, err := httpclient.New(httpclient.Config{
				BaseURL: server.URL, BearerToken: token, HTTPClient: server.Client(),
				Compatibility: appserver.CurrentCompatibility(),
			})
			if err != nil {
				t.Fatal(err)
			}
			clients, err := httpclient.AppServerClients(client)
			if err != nil {
				t.Fatal(err)
			}
			credentials, err := credentialstore.New(storeDir)
			if err != nil {
				t.Fatal(err)
			}

			initial, err := gatewayapp.LoadAppConfig(storeDir)
			if err != nil {
				t.Fatal(err)
			}
			imageInput := true
			connectConfig := appserver.ConnectConfig{
				Provider: "openrouter", Model: "stealth/ox-alpha", APIKey: "intentionally-invalid-key",
				ContextWindowTokens: 1048576, MaxOutputTokens: 131072,
				ReasoningLevels: append([]string(nil), testCase.reasoningLevels...), ImageInput: &imageInput,
			}
			canceledRequest := appserver.ConnectModelRequest{
				WriteBase: appserver.WriteBase{OperationID: "windows-openrouter-canceled", ExpectedRevision: &initial.ConfigurationRevision},
				Config:    connectConfig,
			}
			credentialRef := credentialstore.BuildReference("openrouter", modelconfig.BuildProviderEndpointID("openrouter", "default", ""))
			switch testCase.cancellationPoint {
			case "before-request":
				canceledCtx, cancel := context.WithCancel(context.Background())
				cancel()
				canceled, cancelErr := clients.Configuration.ConnectModel(canceledCtx, canceledRequest)
				if cancelErr == nil || canceled.Outcome == appserver.OutcomeCommitted || !errors.Is(cancelErr, context.Canceled) {
					t.Fatalf("ConnectModel(pre-canceled) = %#v, %v", canceled, cancelErr)
				}
			case "during-persistence":
				cancelWindowsConnectDuringPersistence(t, storeDir, credentialRef, clients.Configuration, canceledRequest)
			default:
				t.Fatalf("unknown cancellation point %q", testCase.cancellationPoint)
			}
			afterCancel, err := gatewayapp.LoadAppConfig(storeDir)
			if err != nil {
				t.Fatal(err)
			}
			if afterCancel.ConfigurationRevision != initial.ConfigurationRevision {
				t.Fatalf("configuration revision after canceled connect = %d, want %d", afterCancel.ConfigurationRevision, initial.ConfigurationRevision)
			}
			if _, credentialErr := credentials.LookupSource(context.Background(), credentialRef); !errors.Is(credentialErr, os.ErrNotExist) {
				t.Fatalf("credential after canceled connect error = %v, want os.ErrNotExist", credentialErr)
			}

			connected, err := clients.Configuration.ConnectModel(context.Background(), appserver.ConnectModelRequest{
				WriteBase: appserver.WriteBase{OperationID: "windows-openrouter-connect", ExpectedRevision: &afterCancel.ConfigurationRevision},
				Config:    connectConfig,
			})
			if err != nil || connected.Outcome != appserver.OutcomeCommitted || connected.ErrorCode != "" {
				t.Fatalf("ConnectModel(reconnect) = %#v, %v", connected, err)
			}
			persisted, err := gatewayapp.LoadAppConfig(storeDir)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.ConfigurationRevision != connected.Revision {
				t.Fatalf("persisted configuration revision = %d, want %d", persisted.ConfigurationRevision, connected.Revision)
			}
			endpoints := make(map[string]modelconfig.ProviderEndpointConfig, len(persisted.Models.ProviderEndpoints))
			for _, endpoint := range persisted.Models.ProviderEndpoints {
				normalized := modelconfig.NormalizeProviderEndpoint(endpoint)
				endpoints[normalized.ID] = normalized
			}
			var configured modelconfig.Config
			for _, candidate := range persisted.Models.Configs {
				normalized := modelconfig.NormalizeConfig(candidate)
				if endpoint, ok := endpoints[normalized.ProviderEndpointID]; ok {
					normalized = modelconfig.MergeConfigProviderEndpoint(normalized, endpoint)
				}
				if normalized.Provider == "openrouter" && normalized.Model == "stealth/ox-alpha" {
					configured = normalized
					break
				}
			}
			defaultMismatch := configured.DefaultReasoningEffort != testCase.wantDefaultEffort
			if testCase.wantDefaultEffort == "" {
				defaultMismatch = configured.DefaultReasoningEffort == testCase.rejectedEffort
			}
			if configured.ID != "openrouter@default/openrouter/stealth/ox-alpha" ||
				configured.Alias != "openrouter/stealth/ox-alpha" ||
				configured.ContextWindowTokens != 1048576 || configured.MaxOutputTok != 131072 ||
				configured.ImageInput == nil || !*configured.ImageInput || configured.CredentialRef != credentialRef ||
				!slices.Equal(configured.ReasoningLevels, testCase.reasoningLevels) || defaultMismatch {
				t.Fatalf("persisted custom OpenRouter model = %#v", configured)
			}
			if source, credentialErr := credentials.LookupSource(context.Background(), credentialRef); credentialErr != nil || source.APIKey != connectConfig.APIKey {
				t.Fatalf("committed OpenRouter credential = %#v, %v", source, credentialErr)
			}
			profileID := modelprofile.BuildProviderID(configured.ID)
			profile, found := modelprofile.Lookup(persisted.ModelProfiles, profileID)
			if !found || profile.ID != profileID || persisted.ModelProfiles.DefaultProfileID != profileID || persisted.ModelProfiles.DefaultEffort != configured.DefaultReasoningEffort {
				t.Fatalf("persisted OpenRouter profile/default = %#v, profile %#v", persisted.ModelProfiles, profile)
			}

			used, err := clients.Configuration.UseModel(context.Background(), appserver.UseModelRequest{
				WriteBase:       appserver.WriteBase{OperationID: "windows-openrouter-use-accepted", ExpectedRevision: &connected.Revision},
				Model:           configured.ID,
				ReasoningEffort: testCase.acceptedEffort,
			})
			if err != nil || used.Outcome != appserver.OutcomeCommitted || used.Revision != connected.Revision+1 || used.ErrorCode != "" {
				t.Fatalf("UseModel(%s) = %#v, %v", testCase.acceptedEffort, used, err)
			}

			rejected, rejectErr := clients.Configuration.UseModel(context.Background(), appserver.UseModelRequest{
				WriteBase:       appserver.WriteBase{OperationID: "windows-openrouter-use-rejected", ExpectedRevision: &used.Revision},
				Model:           configured.ID,
				ReasoningEffort: testCase.rejectedEffort,
			})
			if rejectErr == nil || rejected.Outcome != appserver.OutcomeRejected || rejected.ErrorCode != errorcode.InvalidArgument ||
				!strings.Contains(rejected.Detail, "does not support reasoning level") || rejected.Detail == "conflict" {
				t.Fatalf("UseModel(unsupported) = %#v, %v", rejected, rejectErr)
			}
		})
	}
}

type connectModelResult struct {
	result appserver.CommandResult
	err    error
}

func cancelWindowsConnectDuringPersistence(
	t *testing.T,
	storeDir string,
	credentialRef string,
	client appserver.ConfigurationClient,
	request appserver.ConnectModelRequest,
) {
	t.Helper()
	configLock, err := acquireWindowsTestFileLock(filepath.Join(storeDir, "config.json.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lockOpen := true
	t.Cleanup(func() {
		if lockOpen {
			_ = configLock.Close()
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan connectModelResult, 1)
	go func() {
		connected, connectErr := client.ConnectModel(ctx, request)
		result <- connectModelResult{result: connected, err: connectErr}
	}()

	credentials, err := credentialstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	transactionObserved := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		probeCtx, stopProbe := context.WithTimeout(context.Background(), 75*time.Millisecond)
		_, probeErr := credentials.LookupSource(probeCtx, credentialRef)
		stopProbe()
		if errors.Is(probeErr, context.DeadlineExceeded) {
			transactionObserved = true
			break
		}
		if probeErr != nil && !errors.Is(probeErr, os.ErrNotExist) {
			t.Fatalf("probe credential transaction: %v", probeErr)
		}
		select {
		case got := <-result:
			t.Fatalf("ConnectModel returned before persistence cancellation: %#v, %v", got.result, got.err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !transactionObserved {
		t.Fatal("ConnectModel did not enter its credential transaction")
	}
	// Once replacement holds the credential lock, all remaining pre-persist
	// work is in-memory. Staying blocked across several config-lock retry periods
	// proves the request reached the held AppConfig persistence lock.
	select {
	case got := <-result:
		t.Fatalf("ConnectModel returned instead of waiting at persistence: %#v, %v", got.result, got.err)
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	select {
	case got := <-result:
		if got.err == nil || got.result.Outcome == appserver.OutcomeCommitted || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("ConnectModel(persistence canceled) = %#v, %v", got.result, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ConnectModel did not return after persistence cancellation")
	}
	rollbackCtx, stopRollbackProbe := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopRollbackProbe()
	if _, rollbackErr := credentials.LookupSource(rollbackCtx, credentialRef); !errors.Is(rollbackErr, os.ErrNotExist) {
		t.Fatalf("credential transaction after persistence cancellation error = %v, want os.ErrNotExist", rollbackErr)
	}
	if err := configLock.Close(); err != nil {
		t.Fatal(err)
	}
	lockOpen = false
}

type windowsTestFileLock struct {
	file       *os.File
	overlapped *windows.Overlapped
}

func acquireWindowsTestFileLock(path string) (*windowsTestFileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	overlapped := &windows.Overlapped{}
	if err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		overlapped,
	); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &windowsTestFileLock{file: file, overlapped: overlapped}, nil
}

func (l *windowsTestFileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return errors.Join(
		windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, l.overlapped),
		file.Close(),
	)
}
