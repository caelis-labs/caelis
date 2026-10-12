// Package localclient assembles real Host clients for Surface integration tests.
package localclient

import (
	"context"
	"net/http"
	"path/filepath"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/gatewayapptest"
)

// New binds typed Control clients to an isolated Host with a test
// model provider. The caller owns the returned close function and test paths;
// Surface packages receive no concrete Host assembly.
func New(ctx context.Context, storeDir, workspace, providerURL string, httpClient *http.Client) (appserver.AppServerClients, func() error, error) {
	clients, _, closeHost, err := NewWithLifecycle(ctx, storeDir, workspace, providerURL, httpClient)
	return clients, closeHost, err
}

// NewWithLifecycle exposes separate producer quiescence and resource closure
// for tests that must observe final output before deliberately losing transport.
// The concrete Host and Runtime remain private to test assembly.
func NewWithLifecycle(ctx context.Context, storeDir, workspace, providerURL string, httpClient *http.Client) (appserver.AppServerClients, func(context.Context) error, func() error, error) {
	host, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName: "client-test", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "workspace", WorkspaceCWD: workspace, SkillDirs: []string{},
		Sandbox: gatewayapp.SandboxConfig{RequestedType: "host"}, SandboxHostAuthorityDir: filepath.Join(storeDir, "sandbox-authority"),
		ResolveProviderHTTPClient: gatewayapptest.StaticProviderHTTPClient(httpClient),
	})
	if err != nil {
		return appserver.AppServerClients{}, nil, nil, err
	}
	if _, err := gatewayapptest.ConnectModel(ctx, host, gatewayapp.ModelConfig{
		Provider: "custom-chat", Model: "observation", BaseURL: providerURL,
		Token: "test-token", ContextWindowTokens: 128000, MaxOutputTok: 1024,
	}); err != nil {
		_ = host.Close()
		return appserver.AppServerClients{}, nil, nil, err
	}
	server, err := local.NewAppServer(host)
	if err != nil {
		_ = host.Close()
		return appserver.AppServerClients{}, nil, nil, err
	}
	clients, err := server.Bind(appserver.Principal{ID: "local-user"})
	if err != nil {
		_ = host.Close()
		return appserver.AppServerClients{}, nil, nil, err
	}
	return clients, host.Quiesce, host.Close, nil
}
