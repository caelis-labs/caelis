package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/plugin"
	"github.com/OnslaughtSnail/caelis/ports/session"
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
	script := filepath.Join(pluginDir, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s|%s|%s' \"$1\" \"$CLAUDE_PLUGIN_ROOT\" \"$CUSTOM_PATH\"\n"), 0o700); err != nil {
		t.Fatalf("write hook script: %v", err)
	}

	res := Run(context.Background(), plugin.HookSpec{
		PluginID:  "superpowers",
		Event:     plugin.HookEventSessionStart,
		Command:   "${CLAUDE_PLUGIN_ROOT}/hook.sh",
		Args:      []string{"${CAELIS_WORKSPACE_DIR}"},
		Env:       map[string]string{"CUSTOM_PATH": "${CODEX_PLUGIN_ROOT}/custom"},
		PluginDir: pluginDir,
		WorkDir:   "${CAELIS_PLUGIN_ROOT}",
	}, session.SessionRef{SessionID: "s1"}, workspaceDir)

	if res.Error != nil {
		t.Fatalf("Run() error = %v, stderr = %s", res.Error, res.Stderr)
	}
	want := workspaceDir + "|" + pluginDir + "|" + filepath.Join(pluginDir, "custom")
	if strings.TrimSpace(res.Stdout) != want {
		t.Fatalf("Run() stdout = %q, want %q", strings.TrimSpace(res.Stdout), want)
	}
}

func TestRunFallsBackToShellForExecFormatError(t *testing.T) {
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
