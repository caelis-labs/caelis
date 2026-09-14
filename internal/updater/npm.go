package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	npmHandoffOwnershipName = "ownership.json"
	npmHandoffPlanName      = "plan.json"
)

type npmInstallResult struct {
	Deferred bool
	Handoff  bool
}

// npmHandoffOwnership records the update-lock ownership that the launcher
// releases after installing or verifying a failure.
type npmHandoffOwnership struct {
	Version   int    `json:"version"`
	LockPath  string `json:"lock_path"`
	LockToken string `json:"lock_token"`
}

// npmHandoffPlan is executed by the npm launcher after this process exits and
// releases the running executable.
type npmHandoffPlan struct {
	Version        int      `json:"version"`
	Command        []string `json:"command"`
	CommandLine    string   `json:"command_line"`
	CurrentVersion string   `json:"current_version"`
	LatestVersion  string   `json:"latest_version"`
	Executable     string   `json:"executable"`
}

func (m *Manager) npmInstallCommand(latest string) ([]string, error) {
	npm, err := m.cfg.LookPath("npm")
	if err != nil {
		return nil, err
	}
	return []string{npm, "install", "-g", npmPackageName + "@" + npmVersion(latest), "--registry=" + m.cfg.NPMRegistry}, nil
}

func (m *Manager) installNPM(
	ctx context.Context,
	cmd []string,
	currentVersion string,
	latestVersion string,
	stdout io.Writer,
	stderr io.Writer,
	progress progressReporter,
) (npmInstallResult, error) {
	if len(cmd) == 0 {
		return npmInstallResult{}, fmt.Errorf("missing npm command")
	}
	windows := strings.EqualFold(m.cfg.GOOS, "windows")
	if windows {
		// Windows cannot replace a running executable, so the npm launcher
		// installs after this process exits. A launcher that does not offer the
		// handoff must run npm itself rather than leaving a weaker detached path.
		handoffDir := strings.TrimSpace(m.env(EnvNPMUpdateHandoffDir))
		if handoffDir == "" {
			return npmInstallResult{}, fmt.Errorf(
				"npm update on Windows requires the npm launcher handoff; run %q or update through the npm launcher",
				"npm install -g "+npmPackageName+"@"+npmVersion(latestVersion),
			)
		}
		reportProgress(progress, ProgressEvent{
			Stage: ProgressInstalling, Detail: MethodNPM, Deferred: true,
		})
		if err := m.writeNPMHandoffPlan(handoffDir, cmd, currentVersion, latestVersion); err != nil {
			return npmInstallResult{}, err
		}
		reportProgress(progress, ProgressEvent{
			Stage: ProgressInstalling, Detail: MethodNPM, Done: true, Deferred: true,
		})
		return npmInstallResult{Deferred: true, Handoff: true}, nil
	}
	reportProgress(progress, ProgressEvent{Stage: ProgressInstalling, Detail: MethodNPM})
	if err := m.cfg.CommandRun(ctx, cmd[0], cmd[1:], nil, stdout, stderr); err != nil {
		return npmInstallResult{}, err
	}
	if _, err := m.verifyNPMArtifact(ctx, latestVersion); err != nil {
		return npmInstallResult{}, err
	}
	reportProgress(progress, ProgressEvent{Stage: ProgressInstalling, Detail: MethodNPM, Done: true})
	return npmInstallResult{}, nil
}

// verifyNPMArtifact verifies the binary the npm launcher will execute, so a
// changed global prefix cannot report a false success. npm installs an exact
// version, so the executed target must report that version.
func (m *Manager) verifyNPMArtifact(ctx context.Context, expected string) (string, error) {
	dir := m.env(EnvNPMPlatformPackageDir)
	if dir == "" {
		return "", fmt.Errorf("verify npm update: %s is not set", EnvNPMPlatformPackageDir)
	}
	executable := filepath.Join(dir, "runtime", rawBinaryName(m.cfg.GOOS))
	installed, err := m.artifactVersion(ctx, executable)
	if err != nil {
		return "", err
	}
	if compareVersions(installed, expected) != 0 {
		return "", fmt.Errorf("npm update target reports Caelis %s, expected %s", displayVersion(installed), displayVersion(expected))
	}
	return installed, nil
}

func (m *Manager) writeNPMHandoffPlan(
	dir string,
	cmd []string,
	currentVersion string,
	latestVersion string,
) error {
	if err := ensureNPMHandoffDirectory(dir); err != nil {
		return err
	}
	var err error
	executable := strings.TrimSpace(m.cfg.Executable)
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return err
		}
	}
	// The launcher keeps running after this process exits. Point the lock at
	// the parent PID so a later `caelis update` does not treat a live npm
	// install as an abandoned file.
	if err := m.transferUpdateLockToParent(); err != nil {
		return err
	}
	lockPath := m.lockPath()
	lockToken := ""
	if lockPath != "" {
		data, err := os.ReadFile(lockPath)
		if err != nil {
			return fmt.Errorf("read npm update lock: %w", err)
		}
		lockToken = strings.TrimSpace(string(data))
	}
	ownership := npmHandoffOwnership{
		Version:   1,
		LockPath:  lockPath,
		LockToken: lockToken,
	}
	plan := npmHandoffPlan{
		Version:        2,
		Command:        append([]string(nil), cmd...),
		CommandLine:    windowsNPMCommandLine(cmd),
		CurrentVersion: strings.TrimSpace(currentVersion),
		LatestVersion:  strings.TrimSpace(latestVersion),
		Executable:     filepath.Clean(executable),
	}
	// Ownership is published first so the launcher can release the transferred
	// lock even when plan parsing or child termination prevents installation.
	if err := writeAtomicNPMHandoffJSON(dir, npmHandoffOwnershipName, ownership); err != nil {
		return err
	}
	if err := writeAtomicNPMHandoffJSON(dir, npmHandoffPlanName, plan); err != nil {
		return err
	}
	return nil
}

func ensureNPMHandoffDirectory(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("npm update handoff directory is empty")
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("create npm update handoff directory: %w", err)
	}
	return nil
}

func writeAtomicNPMHandoffJSON(dir string, name string, value any) error {
	// Publish by rename so the launcher cannot observe partial ownership or a
	// partial command.
	tmp, err := os.CreateTemp(dir, ".caelis-npm-plan-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	encoder := json.NewEncoder(tmp)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, name)); err != nil {
		return err
	}
	committed = true
	return nil
}

func windowsNPMCommandLine(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cmd))
	for _, part := range cmd {
		parts = append(parts, windowsQuote(part))
	}
	line := strings.Join(parts, " ")
	switch strings.ToLower(filepath.Ext(cmd[0])) {
	case ".bat", ".cmd":
		return "call " + line
	default:
		return line
	}
}

func windowsQuote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
