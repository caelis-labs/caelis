//go:build darwin || linux

package gatewayapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/internal/sandboxrouter"
)

func TestApplicationNativeExecutionEnvironment(t *testing.T) {
	if os.Getenv("CAELIS_TEST_APPLICATION_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_APPLICATION_NATIVE=1 for native application execution")
	}
	root := applicationNativePolicyRoot(t)
	home, cwd, tools := filepath.Join(root, "home"), filepath.Join(root, "work"), filepath.Join(root, "tools")
	for _, dir := range []string{home, cwd, tools, filepath.Join(cwd, "tmp"), filepath.Join(cwd, ".git")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "cli-config"), []byte("cli-ready"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "fixture-cli"), []byte("#!/bin/sh\n/bin/cat \"$HOME/cli-config\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", tools+":/usr/bin:/bin")
	// Keep the ordinary temporary write grant disjoint from the synthetic home.
	t.Setenv("TMPDIR", filepath.Join(cwd, "tmp"))
	t.Setenv("REMOVE_AT_CONFIG", "inherited")
	t.Setenv("REMOVE_AT_COMMAND", "inherited")
	t.Setenv("CAELIS_CONTROL_TOKEN", "synthetic-private")
	store, err := application.Open(filepath.Join(root, "application.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	connection, err := store.Register(t.Context(), "owner", application.Registration{OperationID: "environment", Name: "environment", Credential: "app-client-" + strings.Repeat("ab", 32)})
	if err != nil {
		t.Fatal(err)
	}
	profile := application.Profile{Version: "v1", Model: "configured-model", ToolsVersion: "v1", Execution: "workspace-write", Workspace: application.Workspace{CWD: cwd}, ExecutionConfig: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{
		Set: map[string]string{"SESSION_VALUE": "first", "OVERRIDE": "configured", "ZDOTDIR": home}, Unset: []string{"REMOVE_AT_CONFIG"},
	}}}
	first, err := newApplicationExecutionRuntime(cwd, root, filepath.Join(root, "authority"), store, connection.Scope, profile, profile.ExecutionConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	profile.ExecutionConfig = sandbox.CloneExecutionConfig(profile.ExecutionConfig)
	profile.ExecutionConfig.Environment.Set["SESSION_VALUE"] = "second"
	second, err := newApplicationExecutionRuntime(cwd, root, filepath.Join(root, "authority"), store, connection.Scope, profile, profile.ExecutionConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	route, err := sandboxrouter.Current("")
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := sandbox.New(sandbox.Config{CWD: cwd, RequestedBackend: route.Backend, WritableRoots: []string{cwd}, BaseEnv: runtimeCommandEnvironment(), Execution: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{Set: map[string]string{"SESSION_VALUE": "ordinary"}, Unset: []string{"REMOVE_AT_CONFIG"}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ordinary.Close()

	const command = `printf 'environment:%s|%s|%s|%s|%s|%s|%s|' "$HOME" "$PWD" "$SESSION_VALUE" "$OVERRIDE" "${REMOVE_AT_CONFIG-unset}" "${REMOVE_AT_COMMAND-unset}" "${CAELIS_CONTROL_TOKEN-unset}"; fixture-cli`
	for _, entry := range []struct {
		name string
		rt   sandbox.Runtime
	}{{"first", first}, {"second", second}, {"ordinary", ordinary}} {
		t.Run(entry.name, func(t *testing.T) {
			want := "environment:" + home + "|" + cwd + "|" + entry.name + "|request|unset|unset|unset|cli-ready"
			for _, route := range []sandbox.Route{sandbox.RouteSandbox, sandbox.RouteHost} {
				request := sandbox.CommandRequest{Command: command, Env: map[string]string{"OVERRIDE": "request"}, UnsetEnv: []string{"REMOVE_AT_COMMAND"}, Constraints: sandbox.Constraints{Route: route}}
				result, err := entry.rt.Run(t.Context(), request)
				if err != nil || result.Stdout != want {
					t.Fatalf("Run(%s) environment mismatch: %v; stdout=%q stderr=%q", route, err, result.Stdout, result.Stderr)
				}
				for _, tty := range []bool{false, true} {
					request.TTY = tty
					if tty {
						request.Command = `IFS= read -r input; ` + command
					}
					opened, err := entry.rt.Start(t.Context(), request)
					if err != nil {
						t.Fatal(err)
					}
					if tty {
						if err := opened.WriteInput(t.Context(), []byte("continue\n")); err != nil {
							t.Fatal(err)
						}
					}
					status, err := opened.Wait(t.Context(), 5*time.Second)
					if err != nil || status.Running {
						t.Fatalf("Start(%s, tty=%v) did not finish: %v", route, tty, err)
					}
					result, err := opened.Result(t.Context())
					if err != nil || !strings.Contains(result.Stdout, want) {
						t.Fatalf("Start(%s, tty=%v) environment mismatch: %v; stdout=%q", route, tty, err, result.Stdout)
					}
				}
			}
		})
	}
	if _, err := first.Run(t.Context(), sandbox.CommandRequest{Command: `printf denied > "$HOME/outside-write"`}); err == nil {
		t.Fatal("HOME inheritance granted sandbox write authority")
	}
	if _, err := os.Stat(filepath.Join(home, "outside-write")); !os.IsNotExist(err) {
		t.Fatalf("sandbox wrote outside its authorized roots: %v", err)
	}
	if os.Getenv("SESSION_VALUE") != "" || os.Getenv("REMOVE_AT_CONFIG") != "inherited" || os.Getenv("CAELIS_CONTROL_TOKEN") != "synthetic-private" {
		t.Fatal("Session configuration mutated the Host")
	}
	opened, err := first.Start(t.Context(), sandbox.CommandRequest{Command: `read -r input; printf revoked > after-revoke`, TTY: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(t.Context(), connection.Scope); err != nil {
		t.Fatal(err)
	}
	if err := opened.WriteInput(t.Context(), []byte("continue\n")); err == nil {
		t.Fatal("revoked lease accepted TTY input")
	}
	if _, err := first.Start(t.Context(), sandbox.CommandRequest{Command: "touch after-revoke", Constraints: sandbox.Constraints{Route: sandbox.RouteHost}}); err == nil {
		t.Fatal("revoked lease accepted approved Host launch")
	}
	if _, err := os.Stat(filepath.Join(cwd, "after-revoke")); !os.IsNotExist(err) {
		t.Fatalf("revocation allowed a new effect: %v", err)
	}
}
