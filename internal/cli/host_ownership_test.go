package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
)

func TestResolveProductClientModeDefaultsBareLaunchToManagedHost(t *testing.T) {
	tests := []struct {
		name       string
		embedded   bool
		controlURL string
		want       productClientMode
		wantErr    bool
	}{
		{name: "bare launch", want: productClientModeManaged},
		{name: "explicit embedded", embedded: true, want: productClientModeEmbedded},
		{name: "remote", controlURL: "http://127.0.0.1:7777", want: productClientModeRemote},
		{name: "conflicting selectors", embedded: true, controlURL: "http://127.0.0.1:7777", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveProductClientMode(test.embedded, test.controlURL)
			if test.wantErr {
				if err == nil {
					t.Fatal("resolveProductClientMode() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("resolveProductClientMode() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestEmbeddedProductClientsExposeSameHostToBuiltInACPChild(t *testing.T) {
	storeDir := t.TempDir()
	workspace := t.TempDir()
	product, err := openProductClients(context.Background(), gatewayapp.Config{
		StoreDir: storeDir, WorkspaceKey: "embedded-child", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	}, productClientOptions{
		Mode: productClientModeEmbedded, StoreDir: storeDir,
		EmbeddedControlEndpoint: roundTripEmbeddedControlFactory(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = product.Close() })
	if product.BaseURL == "" || product.embeddedControl == nil {
		t.Fatalf("embedded child endpoint = %q adapter=%T", product.BaseURL, product.embeddedControl)
	}
	deps, err := product.stack.PresentationDependencies()
	if err != nil {
		t.Fatal(err)
	}
	var selfArgs []string
	for _, configured := range deps.Assembly.Agents {
		if strings.EqualFold(configured.Name, "self") {
			selfArgs = configured.Args
			break
		}
	}
	joined := strings.Join(selfArgs, " ")
	tokenFile := controlserver.DefaultACPIngressTokenFile(storeDir)
	if !strings.Contains(joined, "-control-url "+product.BaseURL) ||
		!strings.Contains(joined, "-control-token-file "+tokenFile) {
		t.Fatalf("embedded self args = %#v, want same-Host ACP ingress attachment", selfArgs)
	}
	assertChildCredentialCreatesACPIngressSession(t, product, tokenFile, "create-embedded-child")
}

func TestEmbeddedRawControlTokenUsesSeparateChildCredential(t *testing.T) {
	storeDir := t.TempDir()
	workspace := t.TempDir()
	rawToken := strings.Repeat("a", 64)
	product, err := openProductClients(context.Background(), gatewayapp.Config{
		StoreDir: storeDir, WorkspaceKey: "embedded-raw-token", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	}, productClientOptions{
		Mode: productClientModeEmbedded, StoreDir: storeDir, Token: rawToken,
		EmbeddedControlEndpoint: roundTripEmbeddedControlFactory(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = product.Close()
		}
	})

	deps, err := product.stack.PresentationDependencies()
	if err != nil {
		t.Fatal(err)
	}
	var childTokenFile string
	for _, configured := range deps.Assembly.Agents {
		if !strings.EqualFold(configured.Name, "self") {
			continue
		}
		for index := 0; index+1 < len(configured.Args); index++ {
			if configured.Args[index] == "-control-token-file" {
				childTokenFile = configured.Args[index+1]
				break
			}
		}
	}
	if childTokenFile == "" || childTokenFile == controlserver.DefaultTokenFile(storeDir) {
		t.Fatalf("built-in child token file = %q, want an independent protected credential", childTokenFile)
	}
	childToken, err := controlserver.LoadBearerToken(childTokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if childToken == rawToken {
		t.Fatal("built-in child credential reused the embedding's raw bearer token")
	}
	for name, token := range map[string]string{"embedding": rawToken, "child": childToken} {
		remote, clientErr := httpclient.New(httpclient.Config{
			BaseURL: product.BaseURL, BearerToken: token, HTTPClient: product.embeddedControl.HTTPClient(),
			Compatibility: appserver.CurrentCompatibility(),
		})
		if clientErr != nil {
			t.Fatalf("%s client: %v", name, clientErr)
		}
		if info, initializeErr := remote.Initialize(context.Background()); initializeErr != nil || info.ServerID == "" {
			t.Fatalf("%s Initialize() = %#v, %v", name, info, initializeErr)
		}
	}
	assertChildCredentialCreatesACPIngressSession(t, product, childTokenFile, "create-embedded-raw-token-child")
	if err := product.Close(); err != nil {
		closed = true
		t.Fatal(err)
	}
	closed = true
	if _, err := os.Stat(childTokenFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child token file after product close: %v, want removed", err)
	}
}

func TestEmbeddedCustomControlTokenFileUsesDedicatedACPIngressCredential(t *testing.T) {
	storeDir := t.TempDir()
	ordinaryTokenFile := filepath.Join(t.TempDir(), "custom-control.token")
	if _, err := controlserver.LoadOrCreateBearerToken(ordinaryTokenFile); err != nil {
		t.Fatal(err)
	}
	product, err := openProductClients(context.Background(), gatewayapp.Config{
		StoreDir: storeDir, WorkspaceKey: "embedded-custom-token", WorkspaceCWD: t.TempDir(),
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	}, productClientOptions{
		Mode: productClientModeEmbedded, StoreDir: storeDir, TokenFile: ordinaryTokenFile,
		EmbeddedControlEndpoint: roundTripEmbeddedControlFactory(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = product.Close() })
	ingressTokenFile := controlserver.DefaultACPIngressTokenFile(storeDir)
	if filepath.Clean(ordinaryTokenFile) == filepath.Clean(ingressTokenFile) {
		t.Fatal("test ordinary and ACP ingress credential paths unexpectedly match")
	}
	assertChildCredentialCreatesACPIngressSession(t, product, ingressTokenFile, "create-embedded-custom-token-child")
}

func TestEmbeddedProductClientsRejectAliasedACPIngressCredential(t *testing.T) {
	storeDir := t.TempDir()
	ingressTokenFile := controlserver.DefaultACPIngressTokenFile(storeDir)
	if _, err := controlserver.LoadOrCreateBearerToken(ingressTokenFile); err != nil {
		t.Fatal(err)
	}
	_, err := openProductClients(context.Background(), gatewayapp.Config{
		StoreDir: storeDir, WorkspaceKey: "embedded-aliased-token", WorkspaceCWD: t.TempDir(),
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	}, productClientOptions{
		Mode: productClientModeEmbedded, StoreDir: storeDir, TokenFile: ingressTokenFile,
		EmbeddedControlEndpoint: roundTripEmbeddedControlFactory(t),
	})
	if err == nil || !strings.Contains(err.Error(), "credentials must be distinct") {
		t.Fatalf("openProductClients() error = %v, want aliased credential rejection", err)
	}
}

func assertChildCredentialCreatesACPIngressSession(t *testing.T, product *productClients, tokenFile, operationID string) {
	t.Helper()
	token, err := controlserver.LoadBearerToken(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := httpclient.New(httpclient.Config{
		BaseURL: product.BaseURL, BearerToken: token, HTTPClient: product.embeddedControl.HTTPClient(),
		Compatibility: appserver.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := remote.Initialize(context.Background()); err != nil || info.ServerID == "" {
		t.Fatalf("private embedded child Initialize() = %#v, %v", info, err)
	}
	workspace := product.stack.Workspace()
	created, err := remote.CreateSession(context.Background(), appserver.CreateSessionRequest{
		WriteBase:    appserver.WriteBase{OperationID: operationID},
		WorkspaceKey: workspace.Key,
		CWD:          workspace.CWD,
	})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("private embedded child CreateSession() = %#v, %v", created, err)
	}
	deps, err := product.stack.PresentationDependencies()
	if err != nil {
		t.Fatal(err)
	}
	active, err := deps.Sessions.Session(context.Background(), session.SessionRef{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if active.Controller.Kind != session.ControllerKindKernel || active.Controller.Source != "acp_ingress" {
		t.Fatalf("private embedded child controller = %#v, want ACP ingress kernel owner", active.Controller)
	}
}

func TestEmbeddedProductClientsRemainAvailableWhenLoopbackIsForbidden(t *testing.T) {
	storeDir := t.TempDir()
	product, err := openProductClients(context.Background(), gatewayapp.Config{
		StoreDir: storeDir, WorkspaceKey: "restricted", WorkspaceCWD: t.TempDir(),
		SkillDirs: []string{}, Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"},
	}, productClientOptions{
		Mode: productClientModeEmbedded,
		EmbeddedControlEndpoint: func() (embeddedControlEndpoint, error) {
			return nil, os.ErrPermission
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = product.Close() })
	if product.Mode != productClientModeEmbedded || !product.EmbeddedChildBridgeUnavailable {
		t.Fatalf("restricted embedded product = mode %d bridge unavailable %v", product.Mode, product.EmbeddedChildBridgeUnavailable)
	}
	if product.BaseURL != "" || product.embeddedControl != nil {
		t.Fatalf("restricted embedded child endpoint = %q adapter=%T, want none", product.BaseURL, product.embeddedControl)
	}
	if err := product.Clients.Validate(); err != nil || product.Clients.Tasks == nil {
		t.Fatalf("restricted embedded clients = %v, tasks=%T", err, product.Clients.Tasks)
	}
}

func TestProductHostOwnershipIsExclusive(t *testing.T) {
	storeDir := t.TempDir()
	first, err := acquireProductHostOwnership(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()

	if _, err := acquireProductHostOwnership(storeDir); !errors.Is(err, ErrProductHostOwnershipConflict) {
		t.Fatalf("second ownership error = %v, want %v", err, ErrProductHostOwnershipConflict)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireProductHostOwnership(storeDir)
	if err != nil {
		t.Fatalf("ownership after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if got := productHostOwnershipPath(storeDir); got != filepath.Join(storeDir, "runtime", "service", productHostOwnershipFilename) {
		t.Fatalf("ownership path = %q", got)
	}
	if _, err := os.Stat(productHostOwnershipPath(storeDir)); err != nil {
		t.Fatalf("ownership lock file must remain after unlock: %v", err)
	}
}

func TestProductHostOwnershipRetainsInodeAcrossRelease(t *testing.T) {
	storeDir := t.TempDir()
	path := productHostOwnershipPath(storeDir)

	first, err := acquireProductHostOwnership(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	firstInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// A waiter opens the same path while the first owner still holds the lock.
	waiterObservedConflict := make(chan struct{})
	waiterAcquired := make(chan error, 1)
	var waiterCloser interface{ Close() error }
	var waiterMu sync.Mutex
	go func() {
		// Wait longer than productHostOwnershipTimeout by retrying after first fails.
		// First attempt times out, then after release a later acquire succeeds.
		conflictReported := false
		for {
			closer, err := acquireProductHostOwnership(storeDir)
			if err == nil {
				waiterMu.Lock()
				waiterCloser = closer
				waiterMu.Unlock()
				waiterAcquired <- nil
				return
			}
			if !errors.Is(err, ErrProductHostOwnershipConflict) {
				waiterAcquired <- err
				return
			}
			if !conflictReported {
				close(waiterObservedConflict)
				conflictReported = true
			}
		}
	}()
	select {
	case <-waiterObservedConflict:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not observe the held ownership lock")
	}

	// A third process must not succeed by racing a deleted/recreated lock file.
	if _, err := acquireProductHostOwnership(storeDir); !errors.Is(err, ErrProductHostOwnershipConflict) {
		t.Fatalf("third concurrent ownership error = %v, want conflict", err)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waiterAcquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not acquire ownership after release")
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(firstInfo, afterInfo) {
		t.Fatal("ownership lock file inode changed after release; dual authority is possible")
	}
	waiterMu.Lock()
	closer := waiterCloser
	waiterMu.Unlock()
	if closer != nil {
		_ = closer.Close()
	}
}
