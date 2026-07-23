package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	npmHandoffOwnershipName = "ownership.json"
	npmHandoffPlanName      = "plan.json"

	windowsNPMDetachedReason = "The npm launcher handoff is unavailable; wait for the background update to finish before starting Caelis again"
)

type npmInstallResult struct {
	Deferred bool
	Handoff  bool
	Reason   string
}

type windowsNPMInstallStrategy uint8

const (
	windowsNPMForegroundHandoff windowsNPMInstallStrategy = iota + 1
	windowsNPMDetachedCompatibility
)

type npmHandoffOwnership struct {
	Version   int    `json:"version"`
	LockPath  string `json:"lock_path"`
	LockToken string `json:"lock_token"`
}

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
	reportProgress(progress, ProgressEvent{
		Stage: ProgressInstalling, Detail: MethodNPM, Deferred: windows,
	})
	if windows {
		switch m.windowsNPMInstallStrategy() {
		case windowsNPMForegroundHandoff:
			handoffDir := strings.TrimSpace(m.env(EnvNPMUpdateHandoffDir))
			if err := m.writeNPMHandoffPlan(handoffDir, cmd, currentVersion, latestVersion); err != nil {
				return npmInstallResult{}, err
			}
			reportProgress(progress, ProgressEvent{
				Stage: ProgressInstalling, Detail: MethodNPM, Done: true, Deferred: true,
			})
			return npmInstallResult{Deferred: true, Handoff: true}, nil
		case windowsNPMDetachedCompatibility:
			if err := m.scheduleWindowsNPMInstall(cmd); err != nil {
				return npmInstallResult{}, err
			}
			reportProgress(progress, ProgressEvent{
				Stage: ProgressInstalling, Detail: MethodNPM, Done: true, Deferred: true,
			})
			return npmInstallResult{Deferred: true, Reason: windowsNPMDetachedReason}, nil
		default:
			return npmInstallResult{}, fmt.Errorf("unsupported Windows npm install strategy")
		}
	}
	if err := m.cfg.CommandRun(ctx, cmd[0], cmd[1:], stdout, stderr); err != nil {
		return npmInstallResult{}, err
	}
	reportProgress(progress, ProgressEvent{Stage: ProgressInstalling, Detail: MethodNPM, Done: true})
	return npmInstallResult{}, nil
}

func (m *Manager) windowsNPMInstallStrategy() windowsNPMInstallStrategy {
	if strings.TrimSpace(m.env(EnvNPMUpdateHandoffDir)) != "" {
		return windowsNPMForegroundHandoff
	}
	return windowsNPMDetachedCompatibility
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
		Version:        1,
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

func (m *Manager) scheduleWindowsNPMInstall(cmd []string) error {
	script, err := os.CreateTemp("", "caelis-npm-update-*.cmd")
	if err != nil {
		return err
	}
	scriptPath := script.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(scriptPath)
		}
	}()
	body := windowsNPMInstallScript(os.Getpid(), cmd)
	if _, err := script.WriteString(body); err != nil {
		_ = script.Close()
		return err
	}
	if err := script.Close(); err != nil {
		return err
	}
	if err := m.cfg.CommandStart("cmd.exe", []string{"/C", "start", "", "/B", scriptPath}); err != nil {
		return err
	}
	committed = true
	return nil
}

func windowsNPMInstallScript(pid int, cmd []string) string {
	pidText := strconv.Itoa(pid)
	lines := []string{
		"@echo off",
		"setlocal",
		":wait",
		fmt.Sprintf(`tasklist /FI "PID eq %s" /NH | find "%s" > nul`, pidText, pidText),
		"if not errorlevel 1 (",
		"  timeout /t 1 /nobreak > nul",
		"  goto wait",
		")",
		windowsNPMCommandLine(cmd) + " > nul 2> nul",
		"del \"%~f0\" > nul 2> nul",
		"",
	}
	return strings.Join(lines, "\r\n")
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
