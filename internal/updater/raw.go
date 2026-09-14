package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/caelis-labs/caelis/internal/version"
)

const (
	// installerBaseURL serves the official installation scripts. Raw
	// self-update runs the same install.sh / install.ps1 published for manual
	// installs, so there is one installation implementation.
	installerBaseURL = "https://caelis.dev"
	// maxInstallerScriptBytes bounds a download of the installer script.
	maxInstallerScriptBytes = 1 << 20
	// maxLatestBytes bounds the latest-only release channel response.
	maxLatestBytes = 1024
)

// latestRawVersion reads the same latest-only channel as install.sh and install.ps1.
func (m *Manager) latestRawVersion(ctx context.Context) (string, error) {
	body, err := m.downloadBytes(ctx, m.cfg.HTTPClient, m.cfg.ReleasesBaseURL+"/latest.txt", maxLatestBytes+1)
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(body))
	// Canonical tags require all three version components and exclude build
	// metadata, matching the versioned paths published to the raw channel.
	if len(body) > maxLatestBytes || !semver.IsValid(version) || semver.Canonical(version) != version {
		return "", errors.New("release channel returned an invalid latest version")
	}
	return version, nil
}

// installRaw installs the latest raw release through the official installer and
// returns the version actually installed on disk.
//
// The installer script is latest-only, so a release can land between the
// preceding check and the install. Accepting any installed version at or above
// the checked target keeps that race from failing an otherwise successful
// update while still rejecting a stale or partial artifact.
func (m *Manager) installRaw(ctx context.Context, latest string, stdout io.Writer, stderr io.Writer, progress progressReporter) (string, error) {
	installDir, err := m.installTargetDir()
	if err != nil {
		return "", err
	}
	reportProgress(progress, ProgressEvent{Stage: ProgressInstalling})
	if err := m.runOfficialInstaller(ctx, installDir, stdout, stderr); err != nil {
		return "", err
	}
	installed, err := m.artifactVersion(ctx, filepath.Join(installDir, rawBinaryName(m.cfg.GOOS)))
	if err != nil {
		return "", err
	}
	if compareVersions(installed, latest) < 0 {
		return "", fmt.Errorf("installed Caelis %s is older than the released %s", displayVersion(installed), displayVersion(latest))
	}
	reportProgress(progress, ProgressEvent{Stage: ProgressInstalling, Done: true})
	return installed, nil
}

// runOfficialInstaller downloads and runs the official installer, passing the
// current install directory and release channel so the installer replaces this
// executable instead of writing a second copy to its own default directory.
func (m *Manager) runOfficialInstaller(ctx context.Context, installDir string, stdout io.Writer, stderr io.Writer) error {
	scriptURL := installerBaseURL + "/" + installerScriptName(m.cfg.GOOS)
	scriptPath, cleanup, err := m.downloadInstallerScript(ctx, scriptURL)
	if err != nil {
		return err
	}
	defer cleanup()
	name, args := installerCommand(m.cfg.GOOS, scriptPath)
	if !strings.EqualFold(strings.TrimSpace(m.cfg.GOOS), "windows") {
		// install.sh uses bash features (the shebang is bash and get_latest_version
		// relies on [[ =~ ]]), so POSIX sh such as dash would fail. Resolve bash
		// explicitly and name the missing dependency instead of exec'ing sh.
		bash, err := m.cfg.LookPath(name)
		if err != nil {
			return fmt.Errorf("official installer requires bash on PATH: %w", err)
		}
		if strings.TrimSpace(bash) == "" {
			return errors.New("official installer requires bash on PATH")
		}
		name = bash
	}
	// CommandRun appends these to the inherited environment.
	env := []string{
		"CAELIS_INSTALL_DIR=" + installDir,
		"CAELIS_RELEASES_BASE_URL=" + m.cfg.ReleasesBaseURL,
	}
	if err := m.cfg.CommandRun(ctx, name, args, env, stdout, stderr); err != nil {
		return fmt.Errorf("official installer %s failed: %w", scriptURL, err)
	}
	return nil
}

func (m *Manager) downloadInstallerScript(ctx context.Context, scriptURL string) (string, func(), error) {
	body, err := m.downloadBytes(ctx, m.cfg.HTTPClient, scriptURL, maxInstallerScriptBytes+1)
	if err != nil {
		return "", nil, fmt.Errorf("download installer %s: %w", scriptURL, err)
	}
	if len(body) == 0 || len(body) > maxInstallerScriptBytes {
		return "", nil, fmt.Errorf("download installer %s: unexpected script size %d", scriptURL, len(body))
	}
	dir, err := os.MkdirTemp("", "caelis-installer-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, installerScriptName(m.cfg.GOOS))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// artifactVersion runs an installed Caelis binary and returns the release
// version it reports. It requires a canonical semver release with a release
// build identity, so a development or mismatched artifact cannot pass as an
// update.
func (m *Manager) artifactVersion(ctx context.Context, executable string) (string, error) {
	out, err := m.cfg.CommandOutput(ctx, executable, []string{"version", "--format", "json"})
	if err != nil {
		return "", fmt.Errorf("verify installed Caelis %s: %w", executable, err)
	}
	var info version.Info
	if err := json.Unmarshal(out, &info); err != nil {
		return "", fmt.Errorf("verify installed Caelis %s: %w", executable, err)
	}
	installed := strings.TrimSpace(info.Version)
	if !semver.IsValid(installed) || semver.Canonical(installed) != installed {
		return "", fmt.Errorf("verify installed Caelis %s: invalid release version %q", executable, installed)
	}
	if info.BuildKind != version.BuildKindRelease {
		return "", fmt.Errorf("verify installed Caelis %s: build kind %q is not a release", executable, info.BuildKind)
	}
	if strings.TrimSpace(info.BuildID) == "" {
		return "", fmt.Errorf("verify installed Caelis %s: missing build identity", executable)
	}
	return installed, nil
}

// installTargetDir resolves the directory that holds the running executable so
// the official installer replaces that binary. It refuses a target whose
// resolved name is not the standard binary: the installer always writes
// `caelis` (`caelis.exe`), so a renamed or unresolvable target would leave the
// original command running the old version.
func (m *Manager) installTargetDir() (string, error) {
	executable := strings.TrimSpace(m.cfg.Executable)
	if executable == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("locate current Caelis executable: %w", err)
		}
		executable = exe
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(executable))
	if err != nil {
		return "", fmt.Errorf("resolve Caelis executable %s: %w", executable, err)
	}
	if name := filepath.Base(resolved); name != rawBinaryName(m.cfg.GOOS) {
		return "", fmt.Errorf("unexpected Caelis executable name %q: refusing to install a different binary", name)
	}
	return filepath.Dir(resolved), nil
}

func installerScriptName(goos string) string {
	if strings.EqualFold(strings.TrimSpace(goos), "windows") {
		return "install.ps1"
	}
	return "install.sh"
}

func installerCommand(goos string, scriptPath string) (string, []string) {
	if strings.EqualFold(strings.TrimSpace(goos), "windows") {
		return "powershell", []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath}
	}
	// install.sh is a bash script; the runner resolves bash on PATH so a system
	// without it reports a clear dependency error instead of a shell failure.
	return "bash", []string{scriptPath}
}

func rawBinaryName(goos string) string {
	if strings.EqualFold(strings.TrimSpace(goos), "windows") {
		return "caelis.exe"
	}
	return "caelis"
}

func (m *Manager) downloadBytes(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	if client == nil {
		client = &http.Client{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "caelis-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}
