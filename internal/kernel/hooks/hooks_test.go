package hooks

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/plugin"
)

func TestHookHelperProcess(t *testing.T) {
	switch os.Getenv("CAELIS_KERNEL_HOOKS_HELPER") {
	case "block":
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)
		<-interrupt
	case "env":
		args := helperArgsAfterDoubleDash(os.Args)
		if len(args) != 1 {
			fmt.Fprintf(os.Stderr, "helper args = %#v, want one argument", args)
			os.Exit(2)
		}
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(2)
		}
		pluginRoot := os.Getenv("CLAUDE_PLUGIN_ROOT")
		cwdInfo, cwdErr := os.Stat(cwd)
		rootInfo, rootErr := os.Stat(pluginRoot)
		if cwdErr != nil || rootErr != nil || !os.SameFile(cwdInfo, rootInfo) {
			fmt.Fprintf(os.Stderr, "cwd %q does not resolve to plugin root %q", cwd, pluginRoot)
			os.Exit(2)
		}
		fmt.Printf("%s|%s|%s", args[0], pluginRoot, os.Getenv("CUSTOM_PATH"))
		os.Exit(0)
	case "args":
		fmt.Print(strings.Join(helperArgsAfterDoubleDash(os.Args), " "))
		os.Exit(0)
	default:
		return
	}
}

func helperArgsAfterDoubleDash(args []string) []string {
	for index, arg := range args {
		if arg == "--" {
			return args[index+1:]
		}
	}
	return nil
}

func TestRunExpandsPluginCompatibilityVariables(t *testing.T) {
	pluginDir := t.TempDir()
	workspaceDir := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	customPathTemplate := filepath.Join("${CODEX_PLUGIN_ROOT}", "custom")

	res := Run(context.Background(), plugin.HookSpec{
		PluginID: "superpowers",
		Event:    plugin.HookEventSessionStart,
		Command:  os.Args[0],
		Args:     []string{"-test.run=^TestHookHelperProcess$", "--", "${CAELIS_WORKSPACE_DIR}"},
		Env: map[string]string{
			"CAELIS_KERNEL_HOOKS_HELPER": "env",
			"CUSTOM_PATH":                customPathTemplate,
		},
		PluginDir: pluginDir,
		Timeout:   "1m",
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
	res := run(context.Background(), plugin.HookSpec{
		PluginID:  "superpowers",
		Event:     plugin.HookEventSessionStart,
		Command:   script,
		PluginDir: pluginDir,
		Env:       map[string]string{"CAELIS_KERNEL_HOOKS_HELPER": "args"},
	}, session.SessionRef{SessionID: "s1"}, t.TempDir(), runOptions{
		shellFallback: func(script string, args []string) (string, []string) {
			if script != filepath.Join(pluginDir, "polyglot.cmd") || len(args) != 0 {
				t.Fatalf("shell fallback input = %q %#v", script, args)
			}
			return os.Args[0], []string{"-test.run=^TestHookHelperProcess$", "--", "shell-fallback"}
		},
	})

	if res.Error != nil {
		t.Fatalf("Run() error = %v, stderr = %s", res.Error, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "shell-fallback" {
		t.Fatalf("Run() stdout = %q, want shell-fallback", strings.TrimSpace(res.Stdout))
	}
}

func TestDefaultShellFallbackCommand(t *testing.T) {
	args := []string{"first", "second"}
	name, gotArgs := defaultShellFallbackCommand("/plugin/hook", args)

	if name != "/bin/sh" {
		t.Fatalf("shell name = %q, want /bin/sh", name)
	}
	wantArgs := []string{"/plugin/hook", "first", "second"}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("shell args = %#v, want %#v", gotArgs, wantArgs)
	}
	gotArgs[1] = "mutated"
	if args[0] != "first" {
		t.Fatalf("defaultShellFallbackCommand aliased input args: %#v", args)
	}
}

func TestRunTimesOut(t *testing.T) {
	res := Run(context.Background(), plugin.HookSpec{
		PluginID: "superpowers",
		Event:    plugin.HookEventSessionStart,
		Command:  os.Args[0],
		Args:     []string{"-test.run=^TestHookHelperProcess$"},
		Env:      map[string]string{"CAELIS_KERNEL_HOOKS_HELPER": "block"},
		Timeout:  "20ms",
	}, session.SessionRef{SessionID: "s1"}, t.TempDir())

	if res.Error == nil {
		t.Fatal("Run() error = nil, want timeout error")
	}
}
