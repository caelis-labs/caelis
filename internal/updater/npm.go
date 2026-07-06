package updater

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (m *Manager) npmInstallCommand(latest string) ([]string, error) {
	npm, err := m.cfg.LookPath("npm")
	if err != nil {
		return nil, err
	}
	return []string{npm, "install", "-g", npmPackageName + "@" + npmVersion(latest), "--registry=" + m.cfg.NPMRegistry}, nil
}

func (m *Manager) installNPM(ctx context.Context, cmd []string, stdout io.Writer, stderr io.Writer) (bool, error) {
	if len(cmd) == 0 {
		return false, fmt.Errorf("missing npm command")
	}
	if strings.EqualFold(m.cfg.GOOS, "windows") {
		writeUpdateProgress(stderr, "Scheduling %s after caelis exits\n", strings.Join(cmd, " "))
		if err := m.scheduleWindowsNPMInstall(cmd); err != nil {
			return false, err
		}
		return true, nil
	}
	writeUpdateProgress(stderr, "Running %s\n", strings.Join(cmd, " "))
	if err := m.cfg.CommandRun(ctx, cmd[0], cmd[1:], stdout, stderr); err != nil {
		return false, err
	}
	return false, nil
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
