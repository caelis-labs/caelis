package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/client/httpclient"
	"github.com/caelis-labs/caelis/internal/servicelifecycle"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

// productClientMode selects how a presentation surface reaches Control.
//
// Product topology has one live Control Host per store. Managed mode discovers
// or starts that independent Host and attaches focused clients. Explicit remote
// mode attaches a caller-selected Host; embedded mode is an explicit
// single-client exception. A shared state directory is never live authority.
type productClientMode int

const (
	// Managed is the safe zero value so a bare product launch converges on the
	// user-private local Host instead of constructing another authority.
	productClientModeManaged productClientMode = iota
	productClientModeRemote
	productClientModeEmbedded
)

type productClientOptions struct {
	Mode               productClientMode
	ControlURL         string
	Token              string
	TokenFile          string
	WorkspaceKey       string
	WorkspaceCWD       string
	UserID             string
	AppName            string
	StoreDir           string
	ListenAddress      string
	HTTPClient         *http.Client
	LaunchLocalService func(localHostStartRequest) (servicelifecycle.LaunchedProcess, error)
	ServiceInstallDir  string
	StartupTimeout     time.Duration
	PollInterval       time.Duration
	// SurfaceHostCause keeps the user-visible Host start/connect blocker in the
	// command error. Diagnostic commands such as doctor need the real cause
	// instead of a generic retry hint.
	SurfaceHostCause bool
	// EmbeddedControlEndpoint replaces the private child-facing loopback
	// adapter in tests. Production leaves it nil and binds an OS-selected port.
	EmbeddedControlEndpoint func() (embeddedControlEndpoint, error)
}

type productClients struct {
	Clients   controlclient.AppServerClients
	Tasks     taskstream.Client
	Mode      productClientMode
	BaseURL   string
	Workspace gatewayapp.Config
	// stack is non-nil only for embedded single-client mode. Surfaces must not
	// use it; it exists solely so the CLI can own Host lifecycle.
	stack *gatewayapp.Stack
	// ownership is the product Host ownership guard for serve/embedded entry.
	ownership io.Closer
	// embeddedControl is a private loopback adapter used only by built-in ACP
	// children. Tests inject a handler-backed RoundTripper endpoint instead.
	embeddedControl embeddedControlEndpoint
	// childCredentialCleanup removes the process-private credential used only
	// when the embedding itself was configured with a raw bearer token.
	childCredentialCleanup func() error
}

func (p *productClients) Close() error {
	if p == nil {
		return nil
	}
	var errs []error
	if p.stack != nil {
		errs = append(errs, p.stack.Close())
		p.stack = nil
	}
	if p.embeddedControl != nil {
		if err := p.embeddedControl.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, err)
		}
		p.embeddedControl = nil
	}
	if p.childCredentialCleanup != nil {
		errs = append(errs, p.childCredentialCleanup())
		p.childCredentialCleanup = nil
	}
	if p.ownership != nil {
		errs = append(errs, p.ownership.Close())
		p.ownership = nil
	}
	return errors.Join(errs...)
}

func openProductClients(ctx context.Context, cfg gatewayapp.Config, options productClientOptions) (*productClients, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	options.ControlURL = strings.TrimSpace(options.ControlURL)
	options.Token = strings.TrimSpace(options.Token)
	options.TokenFile = strings.TrimSpace(options.TokenFile)
	options.UserID = firstNonEmpty(strings.TrimSpace(options.UserID), strings.TrimSpace(cfg.UserID), "local-user")
	options.AppName = firstNonEmpty(strings.TrimSpace(options.AppName), strings.TrimSpace(cfg.AppName), "caelis")
	options.StoreDir = firstNonEmpty(strings.TrimSpace(options.StoreDir), strings.TrimSpace(cfg.StoreDir))
	options.WorkspaceKey = firstNonEmpty(strings.TrimSpace(options.WorkspaceKey), strings.TrimSpace(cfg.WorkspaceKey))
	options.WorkspaceCWD = firstNonEmpty(strings.TrimSpace(options.WorkspaceCWD), strings.TrimSpace(cfg.WorkspaceCWD))

	switch options.Mode {
	case productClientModeManaged:
		return openManagedProductClients(ctx, cfg, options)
	case productClientModeRemote:
		return openRemoteProductClients(ctx, options)
	case productClientModeEmbedded:
		return openEmbeddedProductClients(cfg, options)
	default:
		return nil, fmt.Errorf("cli: unsupported product client mode %d", options.Mode)
	}
}

func openRemoteProductClients(ctx context.Context, options productClientOptions) (*productClients, error) {
	baseURL := strings.TrimSpace(options.ControlURL)
	if baseURL == "" {
		return nil, errors.New("cli: --control-url is required to attach to an external Control Host")
	}
	token, err := resolveControlToken(options)
	if err != nil {
		return nil, err
	}
	remote, err := httpclient.New(httpclient.Config{
		BaseURL:       baseURL,
		BearerToken:   token,
		HTTPClient:    options.HTTPClient,
		EventBuffer:   256,
		Compatibility: controlclient.CurrentCompatibility(),
	})
	if err != nil {
		return nil, err
	}
	initCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := remote.Initialize(initCtx); err != nil {
		return nil, fmt.Errorf("cli: attach to Control Host %s failed: %w", baseURL, err)
	}
	clients, tasks, err := httpclient.BindAppServer(remote)
	if err != nil {
		return nil, err
	}
	return &productClients{
		Clients: clients,
		Tasks:   tasks,
		Mode:    productClientModeRemote,
		BaseURL: baseURL,
		Workspace: gatewayapp.Config{
			AppName: options.AppName, UserID: options.UserID, StoreDir: options.StoreDir,
			WorkspaceKey: options.WorkspaceKey, WorkspaceCWD: options.WorkspaceCWD,
		},
	}, nil
}

func openEmbeddedProductClients(cfg gatewayapp.Config, options productClientOptions) (*productClients, error) {
	ownership, err := acquireProductHostOwnership(cfg.StoreDir)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(options.Token)
	tokenFile := strings.TrimSpace(options.TokenFile)
	var childToken string
	var childTokenFile string
	var childCredentialCleanup func() error
	if token != "" && tokenFile != "" {
		_ = ownership.Close()
		return nil, errors.New("cli: configure either CAELIS_CONTROL_TOKEN or a Control token file, not both")
	}
	if token == "" {
		if tokenFile == "" {
			tokenFile = controlserver.DefaultTokenFile(cfg.StoreDir)
		}
		token, err = controlserver.LoadOrCreateBearerToken(tokenFile)
		if err != nil {
			_ = ownership.Close()
			return nil, err
		}
		childToken = token
		childTokenFile = tokenFile
	} else {
		childTokenFile, childToken, childCredentialCleanup, err = newEphemeralChildControlCredential()
		if err != nil {
			_ = ownership.Close()
			return nil, err
		}
	}
	cleanupChildCredentialOnError := func() {
		if childCredentialCleanup != nil {
			_ = childCredentialCleanup()
		}
	}
	openEndpoint := options.EmbeddedControlEndpoint
	if openEndpoint == nil {
		openEndpoint = newLoopbackEmbeddedControlEndpoint
	}
	endpoint, err := openEndpoint()
	if err != nil {
		cleanupChildCredentialOnError()
		_ = ownership.Close()
		return nil, err
	}
	if endpoint == nil || strings.TrimSpace(endpoint.BaseURL()) == "" {
		cleanupChildCredentialOnError()
		if endpoint != nil {
			_ = endpoint.Close()
		}
		_ = ownership.Close()
		return nil, errors.New("cli: embedded child Control endpoint is unavailable")
	}
	cfg.ChildControlURL = endpoint.BaseURL()
	cfg.ChildControlTokenFile = childTokenFile
	stack, err := gatewayapp.NewLocalStack(cfg)
	if err != nil {
		cleanupChildCredentialOnError()
		_ = endpoint.Close()
		_ = ownership.Close()
		return nil, err
	}
	appServer, err := local.NewAppServer(stack)
	if err != nil {
		cleanupChildCredentialOnError()
		_ = endpoint.Close()
		_ = stack.Close()
		_ = ownership.Close()
		return nil, err
	}
	clients, tasks, err := appServer.Bind(controlclient.Principal{ID: stack.UserID})
	if err != nil {
		cleanupChildCredentialOnError()
		_ = endpoint.Close()
		_ = stack.Close()
		_ = ownership.Close()
		return nil, err
	}
	authenticator, err := controlserver.BearerTokenAuthenticator(token, controlclient.Principal{ID: stack.UserID})
	if err != nil {
		cleanupChildCredentialOnError()
		_ = endpoint.Close()
		_ = stack.Close()
		_ = ownership.Close()
		return nil, err
	}
	if childToken != token {
		childAuthenticator, childAuthErr := controlserver.BearerTokenAuthenticator(childToken, controlclient.Principal{ID: stack.UserID})
		if childAuthErr != nil {
			cleanupChildCredentialOnError()
			_ = endpoint.Close()
			_ = stack.Close()
			_ = ownership.Close()
			return nil, childAuthErr
		}
		authenticator = anyControlAuthenticator(authenticator, childAuthenticator)
	}
	build := version.BuildInfo()
	handler, err := controlserver.Handler(controlserver.Dependencies{
		Services: appServer.Services, TaskStreams: appServer.TaskStreams,
	}, controlserver.Config{
		Authenticator: authenticator,
		AllowedHosts:  []string{"127.0.0.1"},
		ServerInfo: controlclient.ServerInfo{
			ServerID:            controlclient.ServerIdentity,
			DistributionVersion: build.Version,
			BuildID:             build.BuildID,
			BuildKind:           build.BuildKind,
			Capabilities:        controlclient.RequiredManagedHostCapabilities(),
		},
	})
	if err != nil {
		cleanupChildCredentialOnError()
		_ = endpoint.Close()
		_ = stack.Close()
		_ = ownership.Close()
		return nil, err
	}
	if err := endpoint.Start(handler); err != nil {
		cleanupChildCredentialOnError()
		_ = endpoint.Close()
		_ = stack.Close()
		_ = ownership.Close()
		return nil, err
	}
	return &productClients{
		Clients:                clients,
		Tasks:                  tasks,
		Mode:                   productClientModeEmbedded,
		BaseURL:                endpoint.BaseURL(),
		stack:                  stack,
		ownership:              ownership,
		embeddedControl:        endpoint,
		childCredentialCleanup: childCredentialCleanup,
		Workspace: gatewayapp.Config{
			AppName: stack.AppName, UserID: stack.UserID, StoreDir: cfg.StoreDir,
			WorkspaceKey: stack.Workspace.Key, WorkspaceCWD: stack.Workspace.CWD,
		},
	}, nil
}

type embeddedControlEndpoint interface {
	BaseURL() string
	Start(http.Handler) error
	HTTPClient() *http.Client
	Close() error
}

type loopbackEmbeddedControlEndpoint struct {
	listener net.Listener
	server   *http.Server
}

func newLoopbackEmbeddedControlEndpoint() (embeddedControlEndpoint, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return &loopbackEmbeddedControlEndpoint{listener: listener}, nil
}

func (e *loopbackEmbeddedControlEndpoint) BaseURL() string {
	if e == nil || e.listener == nil {
		return ""
	}
	return "http://" + e.listener.Addr().String()
}

func (e *loopbackEmbeddedControlEndpoint) Start(handler http.Handler) error {
	if e == nil || e.listener == nil {
		return errors.New("cli: embedded child Control listener is unavailable")
	}
	if handler == nil {
		return errors.New("cli: embedded child Control handler is required")
	}
	if e.server != nil {
		return errors.New("cli: embedded child Control endpoint is already started")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	listener := e.listener
	e.server = server
	go func() { _ = server.Serve(listener) }()
	return nil
}

func (*loopbackEmbeddedControlEndpoint) HTTPClient() *http.Client { return nil }

func (e *loopbackEmbeddedControlEndpoint) Close() error {
	if e == nil {
		return nil
	}
	var errs []error
	if e.server != nil {
		if err := e.server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, err)
		}
		e.server = nil
	}
	if e.listener != nil {
		if err := e.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
		e.listener = nil
	}
	return errors.Join(errs...)
}

func resolveControlToken(options productClientOptions) (string, error) {
	if token := strings.TrimSpace(options.Token); token != "" {
		if options.TokenFile != "" {
			return "", errors.New("cli: configure either CAELIS_CONTROL_TOKEN or a Control token file, not both")
		}
		return token, nil
	}
	tokenFile := strings.TrimSpace(options.TokenFile)
	if tokenFile == "" {
		tokenFile = controlserver.DefaultTokenFile(options.StoreDir)
	}
	token, err := controlserver.LoadBearerToken(tokenFile)
	if err != nil {
		return "", fmt.Errorf("cli: load Control bearer token from %s: %w", tokenFile, err)
	}
	return token, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type remoteHostOnlyOptions struct {
	SystemPrompt               string
	ApprovalMode               string
	PolicyProfile              string
	DangerouslySkipPermissions bool
	ModelProfile               string
	ReasoningEffort            string
	SandboxBackend             string
	SandboxHelper              string
	ContextWindow              int
	OperationRetention         string
}

// rejectRemoteHostOnlyOptions fails closed when remote attach is selected with
// Host-construction flags that are not applied through the Control client path.
// Callers must not display Host-only features as active when they were ignored.
func rejectRemoteHostOnlyOptions(mode productClientMode, options remoteHostOnlyOptions) error {
	if mode != productClientModeRemote && mode != productClientModeManaged {
		return nil
	}
	switch {
	case options.DangerouslySkipPermissions:
		return errors.New("cli: --dangerously-skip-permissions requires explicit --embedded mode; attached Hosts cannot enable Host escape mode")
	case options.SystemPrompt != "":
		return errors.New("cli: --system-prompt requires explicit --embedded mode or a Host-side configuration mutation")
	case options.ApprovalMode != "":
		return errors.New("cli: --approval-mode requires explicit --embedded mode or a Host-side configuration mutation")
	case options.PolicyProfile != "":
		return errors.New("cli: --policy-profile requires explicit --embedded mode or a Host-side configuration mutation")
	case options.ModelProfile != "":
		return errors.New("cli: --model-profile requires explicit --embedded mode or a Host-side configuration mutation")
	case options.ReasoningEffort != "":
		return errors.New("cli: --reasoning-effort requires explicit --embedded mode or a Host-side configuration mutation")
	case options.SandboxBackend != "":
		return errors.New("cli: --sandbox-backend requires explicit --embedded mode or a Host-side configuration mutation")
	case options.SandboxHelper != "":
		return errors.New("cli: --sandbox-helper-path requires explicit --embedded mode")
	case options.ContextWindow != 0:
		return errors.New("cli: --context-window requires explicit --embedded mode or a Host-side configuration mutation")
	case options.OperationRetention != "":
		return errors.New("cli: --control-operation-retention requires explicit --embedded mode")
	default:
		return nil
	}
}
