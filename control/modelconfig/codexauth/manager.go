// Package codexauth owns Caelis Control's single-account Codex OAuth
// credentials and authenticated HTTP transport.
package codexauth

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
	// ClientID is the public OAuth client ID used by the Codex CLI.
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// DefaultIssuer is the OpenAI OAuth issuer used by Codex.
	DefaultIssuer = "https://auth.openai.com"
	// RedirectURI is the registered localhost callback used by the Codex CLI.
	RedirectURI = "http://localhost:1455/auth/callback"
	// DefaultCredentialRef identifies the single Control-owned Codex account.
	DefaultCredentialRef = "codex:default"

	callbackAddress = "localhost:1455"
	refreshSkew     = 60 * time.Second
	defaultLifetime = time.Hour
	deviceCodeTTL   = 15 * time.Minute
)

var (
	// ErrNoCredentials indicates that interactive Codex authentication has not
	// completed for this Caelis state directory.
	ErrNoCredentials = errors.New("codex oauth credentials are unavailable; run /connect codex to sign in")
	// ErrReauthenticationRequired indicates that the saved refresh token can no
	// longer produce a usable access token.
	ErrReauthenticationRequired = errors.New("codex oauth credentials must be renewed; run /connect codex to sign in again")
	// ErrDeviceCodeUnavailable indicates that the configured OpenAI issuer does
	// not expose the Codex device-code flow.
	ErrDeviceCodeUnavailable = errors.New("codex device-code login is unavailable")

	errBrowserLoginUnavailable = errors.New("codex browser login is unavailable")
)

// Options configures one single-account OAuth manager. Issuer, HTTPClient,
// BrowserOpener, Headless, Clock, Random, and Listen are primarily injection
// points for focused tests; production callers normally set only
// CredentialPath.
type Options struct {
	HTTPClient     *http.Client
	Issuer         string
	CredentialPath string
	BrowserOpener  func(string) error
	Headless       func() bool
	Clock          func() time.Time
	Random         io.Reader
	Listen         func(network string, address string) (net.Listener, error)
}

// LoginOptions controls one interactive login attempt. DeviceCode explicitly
// selects device authorization; otherwise a detected headless environment or
// an unavailable browser transparently falls back to it.
type LoginOptions struct {
	HTTPClient      *http.Client
	OpenBrowser     bool
	DeviceCode      bool
	CallbackTimeout time.Duration
}

// Manager owns the saved refresh and access tokens for the single Codex
// account configured under one Caelis state directory.
type Manager struct {
	issuer         string
	credentialPath string
	httpClient     *http.Client
	browserOpener  func(string) error
	headless       func() bool
	now            func() time.Time
	random         io.Reader
	listen         func(string, string) (net.Listener, error)

	loginMu             sync.Mutex
	mu                  sync.Mutex
	loaded              bool
	stored              storedCredentials
	access              accessCredentials
	rejectedAccessToken string
}

type accessCredentials struct {
	token     string
	accountID string
	expiresAt time.Time
}

// DefaultCredentialPath returns the Control-owned Codex credential file below
// the supplied Caelis state directory.
func DefaultCredentialPath(storeDir string) string {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return ""
	}
	return filepath.Join(storeDir, "providers", "codex", "auth.json")
}

// NewManager constructs a single-account Codex OAuth manager.
func NewManager(opts Options) (*Manager, error) {
	issuer := strings.TrimRight(strings.TrimSpace(opts.Issuer), "/")
	if issuer == "" {
		issuer = DefaultIssuer
	}
	parsedIssuer, err := url.Parse(issuer)
	if err != nil || parsedIssuer.Scheme == "" || parsedIssuer.Host == "" {
		return nil, fmt.Errorf("codexauth: issuer must be an absolute URL")
	}
	credentialPath := strings.TrimSpace(opts.CredentialPath)
	if credentialPath == "" {
		return nil, fmt.Errorf("codexauth: credential path is required")
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
	return &Manager{
		issuer:         issuer,
		credentialPath: credentialPath,
		httpClient:     httpClient,
		browserOpener:  browserOpener,
		headless:       headless,
		now:            now,
		random:         random,
		listen:         listen,
	}, nil
}

// HasCredentials reports whether a structurally valid single-account refresh
// credential is available. It never performs network I/O or refreshes tokens.
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

// EnsureAuthenticated refreshes an existing credential when possible and
// otherwise completes either a localhost PKCE browser login or device-code
// login, depending on the requested mode and host environment.
func (m *Manager) EnsureAuthenticated(ctx context.Context, opts LoginOptions) error {
	if m == nil {
		return fmt.Errorf("codexauth: manager is nil")
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
	preferDeviceCode := opts.DeviceCode || (m.headless != nil && m.headless())
	if preferDeviceCode {
		deviceErr := m.loginWithDeviceCode(ctx, opts)
		if deviceErr == nil {
			return nil
		}
		if opts.DeviceCode || !errors.Is(deviceErr, ErrDeviceCodeUnavailable) {
			return deviceErr
		}
		if !opts.OpenBrowser {
			return errors.Join(authErr, deviceErr)
		}
		// Match the official CLI fallback: keep the localhost callback server
		// available without trying to launch a graphical browser. The progress
		// URL lets a remote user finish this flow manually.
		return m.login(ctx, opts, false)
	}
	if !opts.OpenBrowser {
		return fmt.Errorf("codexauth: browser login is required: %w", authErr)
	}
	browserErr := m.login(ctx, opts, true)
	if browserErr == nil || !errors.Is(browserErr, errBrowserLoginUnavailable) {
		return browserErr
	}
	deviceErr := m.loginWithDeviceCode(ctx, opts)
	if deviceErr == nil {
		return nil
	}
	if errors.Is(deviceErr, ErrDeviceCodeUnavailable) {
		manualBrowserErr := m.login(ctx, opts, false)
		if manualBrowserErr == nil {
			return nil
		}
		return errors.Join(browserErr, deviceErr, manualBrowserErr)
	}
	return errors.Join(browserErr, deviceErr)
}

// AuthenticatedClient clones base and installs a request-time OAuth transport.
// The transport is restricted to the Codex backend allowlist and refreshes the
// saved access token with a 60-second expiry skew.
func (m *Manager) AuthenticatedClient(base *http.Client) (*http.Client, error) {
	if m == nil {
		return nil, fmt.Errorf("codexauth: manager is nil")
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
	if m == nil {
		return accessCredentials{}, fmt.Errorf("codexauth: manager is nil")
	}
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
		return fmt.Errorf("codexauth: acquire credential refresh lock: %w", err)
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("codexauth: release credential refresh lock: %w", closeErr))
		}
	}()

	latest, err := readStoredCredentials(m.credentialPath)
	if err != nil {
		return err
	}
	selectedAccountID := strings.TrimSpace(m.stored.AccountID)
	if selectedAccountID != "" && latest.AccountID != selectedAccountID {
		return fmt.Errorf("codexauth: stored ChatGPT account changed while refreshing: %w", ErrReauthenticationRequired)
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
	return strings.TrimSpace(a.token) != "" &&
		strings.TrimSpace(a.accountID) != "" &&
		a.expiresAt.After(now.Add(skew))
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
		return fmt.Errorf("codexauth: open browser: %w", err)
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
