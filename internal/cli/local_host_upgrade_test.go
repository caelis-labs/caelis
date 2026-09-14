package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/servicelifecycle"
	"github.com/caelis-labs/caelis/internal/testenv"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/google/uuid"
)

// These tests drive the managed-launch combination path that a normal TUI,
// headless, or ACP launch uses: discover the Store Host, converge it on the
// launching binary through servicelifecycle.Manager, then attach only to a
// matching ready endpoint. The handler-backed host below is one controlserver +
// gatewayapp generation, so no real service or installer runs.
//
// Host quiesce semantics are owned by the Host and its own tests:
// internal/kernel TestGatewayQuiesceCancelsAndDrainsActiveTurnThenRejectsAdmission,
// app/gatewayapp TestSessionRuntimeRegistryQuiesceWaitsForRuntimeWorkAfterGatewayDrain,
// and app/controlserver TestAuthenticatedHostShutdownQuiescesInMemoryServer.

const upgradeTestAPIPrefix = "/api/control/v1"

// stampReleaseIdentity pins the build identity the managed candidate is derived
// from so release upgrade and downgrade selection is deterministic.
func stampReleaseIdentity(t *testing.T, distribution string, buildID string) {
	t.Helper()
	oldVersion, oldBuildID, oldBuildKind := version.Version, version.BuildID, version.BuildKind
	version.Version, version.BuildID, version.BuildKind = distribution, buildID, version.BuildKindRelease
	t.Cleanup(func() {
		version.Version, version.BuildID, version.BuildKind = oldVersion, oldBuildID, oldBuildKind
	})
}

func releaseHostIdentity(distribution string, buildID string) servicelifecycle.Identity {
	return servicelifecycle.Identity{
		DistributionVersion: distribution, BuildID: buildID, BuildKind: version.BuildKindRelease,
	}
}

func managedHostInfo(identity servicelifecycle.Identity, instanceID string) appserver.ServerInfo {
	return appserver.ServerInfo{
		ProtocolVersion: acpsdk.ProtocolVersionNumber, EnvelopeVersion: appserver.EnvelopeVersion,
		APIVersion: appserver.HTTPAPIVersion, ServerID: appserver.ServerIdentity,
		DistributionVersion: identity.DistributionVersion, BuildID: identity.BuildID, BuildKind: identity.BuildKind,
		InstanceID: instanceID, Capabilities: appserver.RequiredManagedHostCapabilities(), Transports: []string{"http"},
	}
}

// upgradeTestHost publishes and answers for one ready Host generation. Shutdown
// acknowledges readiness loss and then removes the discovery record, so startup
// proceeds on Host absence rather than on any work completing.
type upgradeTestHost struct {
	t        *testing.T
	storeDir string
	token    string
	server   *testenv.HTTPServer

	mu        sync.Mutex
	current   appserver.ServerInfo
	serving   bool
	publishes int
	shutdowns int
}

func newUpgradeTestHost(t *testing.T, storeDir string, token string) *upgradeTestHost {
	t.Helper()
	host := &upgradeTestHost{t: t, storeDir: storeDir, token: token}
	host.server = testenv.NewHTTPServer(t, http.HandlerFunc(host.serveHTTP))
	return host
}

func (h *upgradeTestHost) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	h.mu.Lock()
	info, serving := h.current, h.serving
	h.mu.Unlock()
	switch request.URL.Path {
	case "/readyz":
		// The lifecycle probe is unauthenticated discovery metadata.
		_ = json.NewEncoder(writer).Encode(appserver.HostStatus{
			ServerID: info.ServerID, InstanceID: info.InstanceID, Ready: serving,
		})
	case upgradeTestAPIPrefix + "/initialize":
		if !h.authorized(request) {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(writer).Encode(info)
	case upgradeTestAPIPrefix + "/host/shutdown":
		if !h.authorized(request) {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.mu.Lock()
		h.shutdowns++
		h.mu.Unlock()
		_ = json.NewEncoder(writer).Encode(appserver.HostStatus{
			ServerID: info.ServerID, InstanceID: info.InstanceID, Ready: false,
		})
		go h.stop(info.InstanceID)
	default:
		http.NotFound(writer, request)
	}
}

func (h *upgradeTestHost) authorized(request *http.Request) bool {
	return request.Header.Get("Authorization") == "Bearer "+h.token
}

func (h *upgradeTestHost) stop(instanceID string) {
	if err := controlserver.RemoveDiscoveryRecord(controlserver.DefaultDiscoveryFile(h.storeDir), instanceID); err != nil {
		h.t.Errorf("remove discovery record: %v", err)
	}
	h.mu.Lock()
	h.serving = false
	h.mu.Unlock()
}

// become makes one new ready Host generation the Store's published endpoint.
func (h *upgradeTestHost) become(info appserver.ServerInfo) error {
	record := controlserver.DiscoveryRecord{
		SchemaVersion: controlserver.DiscoverySchemaVersion,
		ServerID:      info.ServerID, InstanceID: info.InstanceID,
		AppName: "caelis", PrincipalID: "local-user", PID: 1, Endpoint: h.server.URL,
		ProtocolVersion: info.ProtocolVersion, EnvelopeVersion: info.EnvelopeVersion, APIVersion: info.APIVersion,
		DistributionVersion: info.DistributionVersion, BuildID: info.BuildID, BuildKind: info.BuildKind,
		Capabilities: info.Capabilities, Transports: info.Transports, StartedAt: time.Now().UTC(),
	}
	if err := controlserver.PublishDiscoveryRecord(controlserver.DefaultDiscoveryFile(h.storeDir), record); err != nil {
		return err
	}
	h.mu.Lock()
	h.current = info
	h.serving = true
	h.publishes++
	h.mu.Unlock()
	return nil
}

func (h *upgradeTestHost) shutdownCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.shutdowns
}

func (h *upgradeTestHost) publishCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.publishes
}

func (h *upgradeTestHost) currentInstance() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.current.InstanceID
}

func mustBearerToken(t *testing.T, storeDir string) string {
	t.Helper()
	token, err := controlserver.LoadOrCreateBearerToken(controlserver.DefaultTokenFile(storeDir))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func noEmbeddedFallback(calls *atomic.Int32) func() (embeddedControlEndpoint, error) {
	return func() (embeddedControlEndpoint, error) {
		calls.Add(1)
		return nil, errors.New("managed launch must not fall back to embedded")
	}
}

// A first launch of a newer managed build must interrupt the older ready Host
// and start the new one before attaching, never leaving two Hosts per Store.
func TestManagedLocalHostUpgradeShutsDownOldHostThenStartsNew(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	storeDir := t.TempDir()
	stampReleaseIdentity(t, "v1.3.0", "new-build")

	host := newUpgradeTestHost(t, storeDir, mustBearerToken(t, storeDir))
	oldInstance, newInstance := uuid.NewString(), uuid.NewString()
	if err := host.become(managedHostInfo(releaseHostIdentity("v1.2.0", "old-build"), oldInstance)); err != nil {
		t.Fatal(err)
	}

	var launches atomic.Int32
	launch := func(localHostStartRequest) (servicelifecycle.LaunchedProcess, error) {
		launches.Add(1)
		if host.shutdownCount() != 1 {
			t.Errorf("replacement launched while the old Host was still running")
		}
		if _, err := controlserver.LoadDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("old Host discovery still published when the replacement started: %v", err)
		}
		if err := host.become(managedHostInfo(releaseHostIdentity("v1.3.0", "new-build"), newInstance)); err != nil {
			return servicelifecycle.LaunchedProcess{}, err
		}
		return testLaunchedService(2), nil
	}
	product, err := openProductClients(t.Context(), gatewayapp.Config{
		AppName: "caelis", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "workspace", WorkspaceCWD: t.TempDir(),
	}, productClientOptions{
		Mode: productClientModeManaged, HTTPClient: host.server.Client(),
		LaunchLocalService: launch, ServiceInstallDir: t.TempDir(),
		StartupTimeout: time.Second, PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = product.Close() })

	if product.Mode != productClientModeManaged || product.BaseURL != host.server.URL {
		t.Fatalf("upgraded product attachment = %#v", product)
	}
	if launches.Load() != 1 || host.shutdownCount() != 1 {
		t.Fatalf("upgrade launches=%d shutdowns=%d, want exactly one of each", launches.Load(), host.shutdownCount())
	}
	if host.currentInstance() != newInstance {
		t.Fatalf("attached Host instance = %q, want the freshly started replacement", host.currentInstance())
	}
}

// Concurrent launches of the same version must converge on one Host.
func TestManagedLocalHostConcurrentLaunchOfSameVersionStartsOnce(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	storeDir := t.TempDir()
	stampReleaseIdentity(t, "v1.3.0", "only-build")

	host := newUpgradeTestHost(t, storeDir, mustBearerToken(t, storeDir))
	identity := releaseHostIdentity("v1.3.0", "only-build")
	onlyInstance := uuid.NewString()
	var launches atomic.Int32
	launch := func(localHostStartRequest) (servicelifecycle.LaunchedProcess, error) {
		launches.Add(1)
		if err := host.become(managedHostInfo(identity, onlyInstance)); err != nil {
			return servicelifecycle.LaunchedProcess{}, err
		}
		return testLaunchedService(1), nil
	}
	var embeddedCalls atomic.Int32

	const clients = 8
	products := make(chan *productClients, clients)
	failures := make(chan error, clients)
	var wait sync.WaitGroup
	for index := range clients {
		wait.Add(1)
		go func() {
			defer wait.Done()
			product, err := openProductClients(t.Context(), gatewayapp.Config{
				AppName: "caelis", UserID: "local-user", StoreDir: storeDir,
				WorkspaceKey: "workspace-" + string(rune('a'+index)), WorkspaceCWD: t.TempDir(),
			}, productClientOptions{
				Mode: productClientModeManaged, HTTPClient: host.server.Client(),
				LaunchLocalService: launch, ServiceInstallDir: t.TempDir(),
				StartupTimeout: time.Second, PollInterval: time.Millisecond,
				EmbeddedControlEndpoint: noEmbeddedFallback(&embeddedCalls),
			})
			if err != nil {
				failures <- err
				return
			}
			products <- product
		}()
	}
	wait.Wait()
	close(products)
	close(failures)
	for err := range failures {
		t.Errorf("concurrent managed launch: %v", err)
	}
	attached := 0
	for product := range products {
		attached++
		if product.Mode != productClientModeManaged || product.BaseURL != host.server.URL {
			t.Errorf("concurrent managed product = %#v", product)
		}
		_ = product.Close()
	}
	if attached != clients {
		t.Fatalf("attached products = %d, want %d", attached, clients)
	}
	if launches.Load() != 1 || host.publishCount() != 1 {
		t.Fatalf("concurrent launches=%d publishes=%d, want exactly one Host", launches.Load(), host.publishCount())
	}
	if embeddedCalls.Load() != 0 {
		t.Fatalf("embedded fallback calls = %d, want 0", embeddedCalls.Load())
	}
}

// An older caller must never shut down or replace a newer running release.
func TestManagedLocalHostDoesNotDowngradeNewerHost(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	storeDir := t.TempDir()
	stampReleaseIdentity(t, "v1.3.0", "older-build")

	host := newUpgradeTestHost(t, storeDir, mustBearerToken(t, storeDir))
	newerInstance := uuid.NewString()
	if err := host.become(managedHostInfo(releaseHostIdentity("v1.4.0", "newer-build"), newerInstance)); err != nil {
		t.Fatal(err)
	}

	replaced := false
	var embeddedCalls atomic.Int32
	product, err := openProductClients(t.Context(), gatewayapp.Config{
		AppName: "caelis", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "workspace", WorkspaceCWD: t.TempDir(),
	}, productClientOptions{
		Mode: productClientModeManaged, HTTPClient: host.server.Client(),
		LaunchLocalService: func(localHostStartRequest) (servicelifecycle.LaunchedProcess, error) {
			replaced = true
			return testLaunchedService(1), nil
		},
		ServiceInstallDir: t.TempDir(),
		StartupTimeout:    time.Second, PollInterval: time.Millisecond,
		EmbeddedControlEndpoint: noEmbeddedFallback(&embeddedCalls),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = product.Close() })

	if replaced || host.shutdownCount() != 0 || embeddedCalls.Load() != 0 {
		t.Fatalf("older caller replaced=%v shutdowns=%d embedded=%d, want no replacement",
			replaced, host.shutdownCount(), embeddedCalls.Load())
	}
	record, err := controlserver.LoadDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir))
	if err != nil {
		t.Fatal(err)
	}
	if record.BuildID != "newer-build" || record.InstanceID != newerInstance {
		t.Fatalf("running Host after older launch = %#v, want the newer release untouched", record)
	}
	if product.Mode != productClientModeManaged || product.BaseURL != host.server.URL {
		t.Fatalf("attached product = %#v", product)
	}
}

// A failed replacement must surface a managed startup failure instead of
// silently attaching to the old Host or falling back to embedded. The previous
// selection is restored from the durable record, yet the caller still fails.
func TestManagedLocalHostUpgradeFailureSurfacesErrorWithoutFallback(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	storeDir := t.TempDir()
	installDir := t.TempDir()
	host := newUpgradeTestHost(t, storeDir, mustBearerToken(t, storeDir))
	oldInstance, restoredInstance := uuid.NewString(), uuid.NewString()

	// A previous successful launch selected and started v1.2.0, leaving the
	// durable selection record a real installation would have.
	stampReleaseIdentity(t, "v1.2.0", "old-build")
	first, err := openProductClients(t.Context(), gatewayapp.Config{
		AppName: "caelis", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "workspace", WorkspaceCWD: t.TempDir(),
	}, productClientOptions{
		Mode: productClientModeManaged, HTTPClient: host.server.Client(),
		ServiceInstallDir: installDir, StartupTimeout: time.Second, PollInterval: time.Millisecond,
		LaunchLocalService: func(localHostStartRequest) (servicelifecycle.LaunchedProcess, error) {
			if err := host.become(managedHostInfo(releaseHostIdentity("v1.2.0", "old-build"), oldInstance)); err != nil {
				return servicelifecycle.LaunchedProcess{}, err
			}
			return testLaunchedService(1), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	stampReleaseIdentity(t, "v1.3.0", "new-build")

	var upgradeAttempts atomic.Int32
	launch := func(request localHostStartRequest) (servicelifecycle.LaunchedProcess, error) {
		if strings.Contains(request.Executable, string(filepath.Separator)+"1.3.0"+string(filepath.Separator)) {
			upgradeAttempts.Add(1)
			return servicelifecycle.LaunchedProcess{}, errors.New("replacement Host did not become ready")
		}
		if err := host.become(managedHostInfo(releaseHostIdentity("v1.2.0", "old-build"), restoredInstance)); err != nil {
			return servicelifecycle.LaunchedProcess{}, err
		}
		return testLaunchedService(2), nil
	}
	var embeddedCalls atomic.Int32
	product, err := openProductClients(t.Context(), gatewayapp.Config{
		AppName: "caelis", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "workspace", WorkspaceCWD: t.TempDir(),
	}, productClientOptions{
		Mode: productClientModeManaged, HTTPClient: host.server.Client(),
		LaunchLocalService: launch, ServiceInstallDir: installDir,
		StartupTimeout: time.Second, PollInterval: time.Millisecond,
		EmbeddedControlEndpoint: noEmbeddedFallback(&embeddedCalls),
	})
	if err == nil || product != nil {
		t.Fatalf("failed upgrade returned product=%#v err=%v, want a surfaced failure", product, err)
	}
	var failure *managedStartupFailure
	if !errors.As(err, &failure) {
		t.Fatalf("failed upgrade error = %v, want a managed startup failure", err)
	}
	var compatibility *managedCompatibilityError
	if errors.As(err, &compatibility) {
		t.Fatalf("failed upgrade reported a version incompatibility instead of a startup failure: %v", err)
	}
	if embeddedCalls.Load() != 0 {
		t.Fatalf("failed upgrade embedded fallback calls = %d, want 0", embeddedCalls.Load())
	}
	if upgradeAttempts.Load() != 1 || host.shutdownCount() != 1 {
		t.Fatalf("failed upgrade attempts=%d shutdowns=%d, want one replacement attempt after one shutdown",
			upgradeAttempts.Load(), host.shutdownCount())
	}
	record, err := controlserver.LoadDiscoveryRecord(controlserver.DefaultDiscoveryFile(storeDir))
	if err != nil {
		t.Fatal(err)
	}
	if record.BuildID != "old-build" || record.InstanceID != restoredInstance {
		t.Fatalf("restored Host discovery = %#v, want the previous release restored", record)
	}
}
