package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	EnvInstallMethod         = "CAELIS_INSTALL_METHOD"
	EnvNPMPackageDir         = "CAELIS_NPM_PACKAGE_DIR"
	EnvNPMPlatformPackage    = "CAELIS_NPM_PLATFORM_PACKAGE"
	EnvNPMPlatformPackageDir = "CAELIS_NPM_PLATFORM_PACKAGE_DIR"

	MethodRaw = "raw"
	MethodNPM = "npm"
	MethodDev = "dev"

	defaultGitHubAPIURL      = "https://api.github.com/repos/caelis-labs/caelis/releases/latest"
	defaultGitHubReleaseBase = "https://github.com/caelis-labs/caelis/releases/download"
	defaultNPMRegistry       = "https://registry.npmjs.org"
	npmPackageName           = "@caelis/caelis"
	dailyCheckInterval       = 24 * time.Hour
	updateLockMaxAge         = 30 * time.Minute
	defaultCheckHTTPTimeout  = 60 * time.Second
)

// Config carries updater dependencies. Zero values use production defaults.
type Config struct {
	StoreDir       string
	CurrentVersion string
	Executable     string
	GOOS           string
	GOARCH         string

	GitHubAPIURL      string
	GitHubReleaseBase string
	NPMRegistry       string

	HTTPClient    *http.Client
	Now           func() time.Time
	Env           func(string) string
	LookPath      func(string) (string, error)
	CommandOutput func(context.Context, string, []string) ([]byte, error)
	CommandRun    func(context.Context, string, []string, io.Writer, io.Writer) error
	CommandStart  func(string, []string) error
}

// Manager checks for and applies Caelis updates for the local product host.
type Manager struct {
	cfg Config
}

type CheckOptions struct {
	Auto  bool
	Force bool
}

type UpdateOptions struct {
	CheckOnly bool
	Stdout    io.Writer
	Stderr    io.Writer
}

type Result struct {
	CurrentVersion string    `json:"current_version,omitempty"`
	LatestVersion  string    `json:"latest_version,omitempty"`
	InstallMethod  string    `json:"install_method,omitempty"`
	Available      bool      `json:"available"`
	Checked        bool      `json:"checked"`
	Updated        bool      `json:"updated"`
	Deferred       bool      `json:"deferred"`
	Skipped        bool      `json:"skipped"`
	Reason         string    `json:"reason,omitempty"`
	Command        []string  `json:"command,omitempty"`
	LastCheckedAt  time.Time `json:"last_checked_at,omitempty"`
}

type installSource struct {
	Method string
	Reason string
}

func New(cfg Config) *Manager {
	return &Manager{cfg: normalizeConfig(cfg)}
}

func normalizeConfig(cfg Config) Config {
	if cfg.CurrentVersion == "" {
		cfg.CurrentVersion = "dev"
	}
	if cfg.GOOS == "" {
		cfg.GOOS = runtime.GOOS
	}
	if cfg.GOARCH == "" {
		cfg.GOARCH = runtime.GOARCH
	}
	if cfg.GitHubAPIURL == "" {
		cfg.GitHubAPIURL = defaultGitHubAPIURL
	}
	if cfg.GitHubReleaseBase == "" {
		cfg.GitHubReleaseBase = defaultGitHubReleaseBase
	}
	if cfg.NPMRegistry == "" {
		cfg.NPMRegistry = defaultNPMRegistry
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: defaultCheckHTTPTimeout}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Env == nil {
		cfg.Env = os.Getenv
	}
	if cfg.LookPath == nil {
		cfg.LookPath = exec.LookPath
	}
	if cfg.CommandOutput == nil {
		cfg.CommandOutput = defaultCommandOutput
	}
	if cfg.CommandRun == nil {
		cfg.CommandRun = defaultCommandRun
	}
	if cfg.CommandStart == nil {
		cfg.CommandStart = defaultCommandStart
	}
	return cfg
}

func defaultCommandOutput(ctx context.Context, name string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Output()
}

func defaultCommandRun(ctx context.Context, name string, args []string, stdout io.Writer, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func defaultCommandStart(name string, args []string) error {
	cmd := exec.Command(name, args...)
	return cmd.Start()
}

func (m *Manager) Check(ctx context.Context, opts CheckOptions) (Result, error) {
	ctx = contextOrBackground(ctx)
	current := m.currentDisplayVersion()
	result := Result{CurrentVersion: current}
	source := m.installSource(ctx)
	result.InstallMethod = source.Method
	if source.Reason != "" {
		result.Skipped = true
		result.Reason = source.Reason
		return result, nil
	}
	if opts.Auto && !opts.Force {
		if cached, ok := m.cachedResult(current, source.Method); ok {
			return cached, nil
		}
	}
	latest, err := m.latestVersion(ctx, source.Method)
	if err != nil {
		_ = m.saveState(stateFile{
			CurrentVersion: current,
			InstallMethod:  source.Method,
			LastCheckedAt:  m.now(),
			LastError:      err.Error(),
		})
		return result, err
	}
	result.Checked = true
	result.LatestVersion = displayVersion(latest)
	result.Available = compareVersions(latest, current) > 0
	result.LastCheckedAt = m.now()
	_ = m.saveState(stateFile{
		CurrentVersion: current,
		LatestVersion:  result.LatestVersion,
		InstallMethod:  source.Method,
		Available:      result.Available,
		LastCheckedAt:  result.LastCheckedAt,
	})
	return result, nil
}

func (m *Manager) Update(ctx context.Context, opts UpdateOptions) (Result, error) {
	if opts.CheckOnly {
		return m.Check(ctx, CheckOptions{Force: true})
	}
	releaseLock, locked, err := m.acquireUpdateLock()
	if err != nil {
		return Result{CurrentVersion: m.currentDisplayVersion()}, err
	}
	if !locked {
		return Result{
			CurrentVersion: m.currentDisplayVersion(),
			Skipped:        true,
			Reason:         "another update is already running",
		}, nil
	}
	defer releaseLock()

	result, err := m.Check(ctx, CheckOptions{Force: true})
	if err != nil || result.Skipped || !result.Available {
		return result, err
	}
	switch result.InstallMethod {
	case MethodRaw:
		deferred, err := m.installRaw(ctx, result.LatestVersion, opts.Stderr)
		if err != nil {
			return result, err
		}
		result.Deferred = deferred
		result.Updated = !deferred
	case MethodNPM:
		cmd, err := m.npmInstallCommand(result.LatestVersion)
		if err != nil {
			return result, err
		}
		result.Command = append([]string(nil), cmd...)
		deferred, err := m.installNPM(ctx, cmd, opts.Stdout, opts.Stderr)
		if err != nil {
			return result, err
		}
		result.Deferred = deferred
		result.Updated = !deferred
	default:
		result.Skipped = true
		result.Reason = "unsupported install method"
	}
	m.saveUpdateResultState(result)
	return result, nil
}

func (m *Manager) saveUpdateResultState(result Result) {
	switch {
	case result.Updated:
		_ = m.saveState(stateFile{
			CurrentVersion: result.LatestVersion,
			LatestVersion:  result.LatestVersion,
			InstallMethod:  result.InstallMethod,
			Available:      false,
			LastCheckedAt:  m.now(),
		})
	case result.Deferred:
		current := strings.TrimSpace(result.CurrentVersion)
		if current == "" {
			current = m.currentDisplayVersion()
		}
		_ = m.saveState(stateFile{
			CurrentVersion: current,
			LatestVersion:  result.LatestVersion,
			InstallMethod:  result.InstallMethod,
			Available:      true,
			LastCheckedAt:  m.now(),
		})
	}
}

func (m *Manager) installSource(ctx context.Context) installSource {
	if isDevVersion(m.cfg.CurrentVersion) {
		return installSource{Method: MethodDev, Reason: "development build"}
	}
	if strings.EqualFold(strings.TrimSpace(m.env(EnvInstallMethod)), MethodNPM) {
		if !m.isGlobalNPMInstall(ctx) {
			return installSource{Method: MethodNPM, Reason: "npm install is not global"}
		}
		return installSource{Method: MethodNPM}
	}
	return installSource{Method: MethodRaw}
}

func (m *Manager) isGlobalNPMInstall(ctx context.Context) bool {
	npm, err := m.cfg.LookPath("npm")
	if err != nil || strings.TrimSpace(npm) == "" {
		return false
	}
	packageDir := strings.TrimSpace(m.env(EnvNPMPackageDir))
	if packageDir == "" {
		packageDir = strings.TrimSpace(m.env(EnvNPMPlatformPackageDir))
	}
	if packageDir == "" {
		return false
	}
	out, err := m.cfg.CommandOutput(ctx, npm, []string{"root", "-g"})
	if err != nil {
		return false
	}
	globalRoot := strings.TrimSpace(string(out))
	if globalRoot == "" {
		return false
	}
	return pathWithinRoot(globalRoot, packageDir)
}

func pathWithinRoot(root string, target string) bool {
	root = cleanExistingPath(root)
	target = cleanExistingPath(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func cleanExistingPath(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	// Walk up to the nearest existing ancestor so that symlinked
	// prefixes (e.g. /var → /private/var on macOS) are resolved even
	// when the full target path does not exist yet.
	parent := filepath.Dir(path)
	if parent != path {
		resolvedParent := cleanExistingPath(parent)
		return filepath.Join(resolvedParent, filepath.Base(path))
	}
	return path
}

func (m *Manager) latestVersion(ctx context.Context, method string) (string, error) {
	switch method {
	case MethodRaw:
		return m.latestGitHubVersion(ctx)
	case MethodNPM:
		return m.latestNPMVersion(ctx)
	default:
		return "", fmt.Errorf("unsupported install method %q", method)
	}
}

func (m *Manager) latestGitHubVersion(ctx context.Context) (string, error) {
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := m.getJSON(ctx, m.cfg.GitHubAPIURL, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.TagName) == "" {
		return "", errors.New("latest GitHub release has no tag_name")
	}
	return payload.TagName, nil
}

func (m *Manager) latestNPMVersion(ctx context.Context) (string, error) {
	npm, err := m.cfg.LookPath("npm")
	if err != nil {
		return "", err
	}
	out, err := m.cfg.CommandOutput(ctx, npm, []string{"view", npmPackageName, "version", "--registry=" + m.cfg.NPMRegistry})
	if err != nil {
		return "", err
	}
	version := strings.Trim(strings.TrimSpace(string(out)), "\"")
	if version == "" {
		return "", errors.New("npm returned an empty latest version")
	}
	return version, nil
}

func (m *Manager) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "caelis-updater")
	resp, err := m.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (m *Manager) currentDisplayVersion() string {
	if isDevVersion(m.cfg.CurrentVersion) {
		return "dev"
	}
	return displayVersion(m.cfg.CurrentVersion)
}

func (m *Manager) now() time.Time {
	return m.cfg.Now().UTC()
}

func (m *Manager) env(key string) string {
	if m.cfg.Env == nil {
		return ""
	}
	return strings.TrimSpace(m.cfg.Env(key))
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// HintEligible reports whether a background update check should surface a TUI hint.
func (m *Manager) HintEligible(result Result) bool {
	if result.Skipped || !result.Available {
		return false
	}
	if strings.TrimSpace(result.LatestVersion) == "" {
		return false
	}
	return !m.IsUpdateLockHeld()
}
