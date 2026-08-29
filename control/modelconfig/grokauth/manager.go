// Package grokauth owns Caelis Control's single-account xAI OAuth credentials
// and authenticated HTTP transport for the Grok provider.
package grokauth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// ClientID is the public Grok Build OAuth client used by xAI's supported
	// desktop subscription integrations. xAI does not currently publish
	// general third-party client registration, so keep this compatibility
	// dependency isolated here.
	ClientID = "b1a00492-073a-47ea-816f-4c329264a828"
	// DefaultIssuer is xAI's OIDC issuer.
	DefaultIssuer = "https://auth.x.ai"
	// DefaultCredentialRef identifies the single Control-owned xAI account.
	DefaultCredentialRef = "xai:default"
	// DefaultAPIBaseURL is the maintained Grok Build subscription inference
	// endpoint. OAuth session credentials must not be sent to the API-key
	// endpoint at api.x.ai.
	DefaultAPIBaseURL = "https://cli-chat-proxy.grok.com/v1"

	callbackAddress   = "127.0.0.1:0"
	accountsAppOrigin = "https://accounts.x.ai"
	oauthScope        = "openid profile email offline_access grok-cli:access api:access"
	refreshSkew       = 2 * time.Minute
	defaultLifetime   = time.Hour

	// grokBuildProtocolVersion is a compatibility anchor required by the
	// subscription proxy. It is intentionally independent from the Caelis
	// product version and must track the public Grok Build wire contract.
	grokBuildProtocolVersion = "1.0.12"
)

var (
	// ErrNoCredentials indicates that Grok OAuth has not completed for this
	// Caelis state directory.
	ErrNoCredentials = errors.New("grok oauth credentials are unavailable; run /connect grok to sign in")
	// ErrReauthenticationRequired indicates that the refresh credential is no
	// longer usable.
	ErrReauthenticationRequired = errors.New("grok oauth credentials must be renewed; run /connect grok to sign in again")

	errBrowserLoginUnavailable = errors.New("grok browser login is unavailable")
)

// Options configures one single-account OAuth manager. Most fields are test
// seams; production callers normally set only CredentialPath.
type Options struct {
	HTTPClient     *http.Client
	Issuer         string
	CredentialPath string
	BrowserOpener  func(string) error
	Headless       func() bool
	Clock          func() time.Time
	Random         io.Reader
	Listen         func(network string, address string) (net.Listener, error)
	Sleep          func(context.Context, time.Duration) error
}

// LoginOptions controls one interactive login attempt. DeviceCode explicitly
// selects RFC 8628 device authorization.
type LoginOptions struct {
	HTTPClient      *http.Client
	OpenBrowser     bool
	DeviceCode      bool
	CallbackTimeout time.Duration
}

// Manager owns the saved refresh and access tokens for one xAI account.
type Manager struct {
	issuer         string
	credentialPath string
	httpClient     *http.Client
	browserOpener  func(string) error
	headless       func() bool
	now            func() time.Time
	random         io.Reader
	listen         func(string, string) (net.Listener, error)
	sleep          func(context.Context, time.Duration) error

	loginMu             sync.Mutex
	mu                  sync.Mutex
	loaded              bool
	stored              storedCredentials
	access              accessCredentials
	rejectedAccessToken string
}

type accessCredentials struct {
	token     string
	expiresAt time.Time
}

// DefaultCredentialPath returns the xAI credential file below storeDir.
func DefaultCredentialPath(storeDir string) string {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return ""
	}
	return filepath.Join(storeDir, "providers", "grok", "auth.json")
}

// NewManager constructs a single-account Grok OAuth manager.
func NewManager(opts Options) (*Manager, error) {
	issuer := strings.TrimRight(strings.TrimSpace(opts.Issuer), "/")
	if issuer == "" {
		issuer = DefaultIssuer
	}
	parsedIssuer, err := url.Parse(issuer)
	if err != nil || parsedIssuer.Scheme == "" || parsedIssuer.Host == "" {
		return nil, fmt.Errorf("grokauth: issuer must be an absolute URL")
	}
	credentialPath := strings.TrimSpace(opts.CredentialPath)
	if credentialPath == "" {
		return nil, fmt.Errorf("grokauth: credential path is required")
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	browserOpener := opts.BrowserOpener
	if browserOpener == nil {
		browserOpener = openBrowser
	}
	headless := opts.Headless
	if headless == nil {
		headless = detectHeadlessEnvironment
	}
	now := opts.Clock
	if now == nil {
		now = time.Now
	}
	random := opts.Random
	if random == nil {
		random = rand.Reader
	}
	listen := opts.Listen
	if listen == nil {
		listen = net.Listen
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	return &Manager{
		issuer:         issuer,
		credentialPath: credentialPath,
		httpClient:     httpClient,
		browserOpener:  browserOpener,
		headless:       headless,
		now:            now,
		random:         random,
		listen:         listen,
		sleep:          sleep,
	}, nil
}

// HasCredentials reports whether a structurally valid refresh credential is
// available. It never performs network I/O.
func (m *Manager) HasCredentials(_ context.Context) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadStoredLocked(); err != nil {
		return false
	}
	return m.stored.valid()
}

// EnsureAuthenticated refreshes existing credentials or completes browser
// PKCE/device authorization.
func (m *Manager) EnsureAuthenticated(ctx context.Context, opts LoginOptions) error {
	if m == nil {
		return fmt.Errorf("grokauth: manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.loginMu.Lock()
	defer m.loginMu.Unlock()
	_, authErr := m.accessToken(ctx, opts.HTTPClient)
	if authErr == nil {
		return nil
	}
	if !errors.Is(authErr, ErrNoCredentials) && !errors.Is(authErr, ErrReauthenticationRequired) {
		return authErr
	}
	if opts.DeviceCode || (m.headless != nil && m.headless()) || !opts.OpenBrowser {
		return m.loginWithDeviceCode(ctx, opts)
	}
	if err := m.loginWithBrowser(ctx, opts); err == nil {
		return nil
	} else if !errors.Is(err, errBrowserLoginUnavailable) {
		return err
	}
	return m.loginWithDeviceCode(ctx, opts)
}

// AuthenticatedClient clones base and installs a request-time OAuth transport.
// The transport silently refreshes and retries one replayable request when the
// maintained Grok backend rejects its access token with 401.
func (m *Manager) AuthenticatedClient(base *http.Client) (*http.Client, error) {
	if m == nil {
		return nil, fmt.Errorf("grokauth: manager is nil")
	}
	m.mu.Lock()
	err := m.loadStoredLocked()
	hasCredentials := err == nil && m.stored.valid()
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !hasCredentials {
		return nil, ErrNoCredentials
	}
	if base == nil {
		base = &http.Client{}
	}
	clone := *base
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clone.Transport = &authenticatedTransport{manager: m, base: transport}
	return &clone, nil
}

func (m *Manager) accessToken(ctx context.Context, clientOverride *http.Client) (accessCredentials, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadStoredLocked(); err != nil {
		return accessCredentials{}, err
	}
	if m.access.usableAt(m.now(), refreshSkew) {
		return m.access, nil
	}
	if !m.stored.valid() {
		return accessCredentials{}, ErrNoCredentials
	}
	client := clientOverride
	if client == nil {
		client = m.httpClient
	}
	if err := m.refreshWithFileLockLocked(ctx, client); err != nil {
		return accessCredentials{}, err
	}
	return m.access, nil
}

func (m *Manager) refreshWithFileLockLocked(ctx context.Context, client *http.Client) (err error) {
	lock, err := acquireCredentialFileLock(ctx, m.credentialPath+".lock")
	if err != nil {
		return fmt.Errorf("grokauth: acquire credential refresh lock: %w", err)
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("grokauth: release credential refresh lock: %w", closeErr))
		}
	}()
	latest, err := readStoredCredentials(m.credentialPath)
	if err != nil {
		return err
	}
	m.stored = latest
	m.loaded = true
	latestAccess := accessCredentialsFromStored(latest)
	if latestAccess.token != m.rejectedAccessToken && latestAccess.usableAt(m.now(), refreshSkew) {
		m.access = latestAccess
		return nil
	}
	return m.refreshLocked(ctx, client)
}

func (m *Manager) invalidateAccess(token string) {
	if m == nil || strings.TrimSpace(token) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.access.token == token {
		m.access = accessCredentials{}
		m.rejectedAccessToken = token
	}
}

func (a accessCredentials) usableAt(now time.Time, skew time.Duration) bool {
	return strings.TrimSpace(a.token) != "" && a.expiresAt.After(now.Add(skew))
}

func openBrowser(target string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{target}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		command, args = "xdg-open", []string{target}
	}
	if err := exec.Command(command, args...).Start(); err != nil {
		return fmt.Errorf("grokauth: open browser: %w", err)
	}
	return nil
}

func detectHeadlessEnvironment() bool {
	if strings.TrimSpace(os.Getenv("SSH_CONNECTION")) != "" || strings.TrimSpace(os.Getenv("SSH_TTY")) != "" {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CI")), "true") || strings.TrimSpace(os.Getenv("CI")) == "1" {
		return true
	}
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly":
		return strings.TrimSpace(os.Getenv("DISPLAY")) == "" && strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) == ""
	default:
		return false
	}
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
