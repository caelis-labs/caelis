package hooks

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/ports/plugin"
	"github.com/caelis-labs/caelis/ports/session"
)

func TestHookHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_KERNEL_HOOKS_HELPER") != "1" {
		return
	}
	time.Sleep(2 * time.Second)
	os.Exit(0)
}

func TestRunExpandsPluginCompatibilityVariables(t *testing.T) {
	pluginDir := t.TempDir()
	workspaceDir := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	scriptName := "hook.sh"
	scriptBody := "#!/bin/sh\nprintf '%s|%s|%s' \"$1\" \"$CLAUDE_PLUGIN_ROOT\" \"$CUSTOM_PATH\"\n"
	if runtime.GOOS == "windows" {
		scriptName = "hook.cmd"
		scriptBody = "@echo off\r\necho %~1^|%CLAUDE_PLUGIN_ROOT%^|%CUSTOM_PATH%\r\n"
	}
	script := filepath.Join(pluginDir, scriptName)
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatalf("write hook script: %v", err)
	}
	customPathTemplate := filepath.Join("${CODEX_PLUGIN_ROOT}", "custom")

	res := Run(context.Background(), plugin.HookSpec{
		PluginID:  "superpowers",
		Event:     plugin.HookEventSessionStart,
		Command:   filepath.Join("${CLAUDE_PLUGIN_ROOT}", scriptName),
		Args:      []string{"${CAELIS_WORKSPACE_DIR}"},
		Env:       map[string]string{"CUSTOM_PATH": customPathTemplate},
		PluginDir: pluginDir,
		WorkDir:   "${CAELIS_PLUGIN_ROOT}",
	}, session.SessionRef{SessionID: "s1"}, workspaceDir)

	if res.Error != nil {
		t.Fatalf("Run() error = %v, stderr = %s", res.Error, res.Stderr)
	}
	gotParts := strings.Split(strings.TrimSpace(res.Stdout), "|")
	wantParts := []string{workspaceDir, pluginDir, filepath.Join(pluginDir, "custom")}
	if len(gotParts) != len(wantParts) {
		t.Fatalf("Run() stdout = %q, want %q", strings.TrimSpace(res.Stdout), strings.Join(wantParts, "|"))
	}
	for i := range wantParts {
		if filepath.Clean(gotParts[i]) != filepath.Clean(wantParts[i]) {
			t.Fatalf("Run() stdout part %d = %q, want %q; full stdout = %q", i, gotParts[i], wantParts[i], strings.TrimSpace(res.Stdout))
		}
	}
}

func TestRunFallsBackToShellForExecFormatError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec format shell fallback is POSIX-only")
	}
	pluginDir := t.TempDir()
	script := filepath.Join(pluginDir, "polyglot.cmd")
	if err := os.WriteFile(script, []byte("printf '%s' shell-fallback\n"), 0o700); err != nil {
		t.Fatalf("write hook script: %v", err)
	}

	res := Run(context.Background(), plugin.HookSpec{
		PluginID:  "superpowers",
		Event:     plugin.HookEventSessionStart,
		Command:   script,
		PluginDir: pluginDir,
	}, session.SessionRef{SessionID: "s1"}, t.TempDir())

	if res.Error != nil {
		t.Fatalf("Run() error = %v, stderr = %s", res.Error, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "shell-fallback" {
		t.Fatalf("Run() stdout = %q, want shell-fallback", strings.TrimSpace(res.Stdout))
	}
}

func TestRunTimesOut(t *testing.T) {
	res := Run(context.Background(), plugin.HookSpec{
		PluginID: "superpowers",
		Event:    plugin.HookEventSessionStart,
		Command:  os.Args[0],
		Args:     []string{"-test.run=^TestHookHelperProcess$"},
		Env:      map[string]string{"CAELIS_KERNEL_HOOKS_HELPER": "1"},
		Timeout:  "20ms",
	}, session.SessionRef{SessionID: "s1"}, t.TempDir())

	if res.Error == nil {
		t.Fatal("Run() error = nil, want timeout error")
	}
}
