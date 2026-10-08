//go:build darwin || linux || windows

package gatewayapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/internal/sandboxrouter"
)

func TestApplicationNativeWorkspaceAccess(t *testing.T) {
	if err := validateApplicationExecutionPlatform("workspace-write"); err != nil {
		t.Fatalf("ordinary native execution should be available: %v", err)
	}
	root := t.TempDir()
	notebook := filepath.Join(root, "notebook 空 格")
	extra := filepath.Join(root, "extra")
	readOnly := filepath.Join(root, "read-only")
	for _, path := range []string{notebook, extra, readOnly} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	profile := application.Workspace{CWD: notebook, Access: []application.WorkspaceAccess{{Path: extra, Mode: "read-write"}, {Path: readOnly, Mode: "read-only"}}}
	ref, err := applicationSessionWorkspace(profile, filepath.Join(root, "store"), "session-one")
	resolvedNotebook, resolveErr := filepath.EvalSymlinks(notebook)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if err != nil || ref.CWD != resolvedNotebook || ref.Key != "session-one" {
		t.Fatalf("pinned Notebook workspace = %+v, %v", ref, err)
	}
	writable, access, err := applicationWorkspaceAccess(profile.Access, ref.CWD)
	resolvedExtra, resolveErr := filepath.EvalSymlinks(extra)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	resolvedReadOnly, resolveErr := filepath.EvalSymlinks(readOnly)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if err != nil || !slices.Equal(writable, []string{resolvedNotebook, resolvedExtra}) || !slices.Equal(access, []string{resolvedExtra, resolvedReadOnly}) {
		t.Fatalf("directory grants = writable:%v access:%v error:%v", writable, access, err)
	}
	if _, _, err := applicationWorkspaceAccess([]application.WorkspaceAccess{{Path: readOnly, Mode: "read-only"}, {Path: readOnly, Mode: "read-write"}}, ref.CWD); err == nil {
		t.Fatal("conflicting access modes accepted")
	}
	if _, _, err := applicationWorkspaceAccess([]application.WorkspaceAccess{{Path: "../other", Mode: "read-write"}}, ref.CWD); err == nil {
		t.Fatal("relative application grant accepted")
	}
}

func TestApplicationNativeWorkspacePolicy(t *testing.T) {
	if os.Getenv("CAELIS_TEST_APPLICATION_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_APPLICATION_NATIVE=1 for native directory policy acceptance")
	}
	root := applicationNativePolicyRoot(t)
	cwd, extra, readOnly, outside := filepath.Join(root, "工作 空 格"), filepath.Join(root, "extra"), filepath.Join(root, "read-only"), filepath.Join(root, "outside")
	for _, path := range []string{cwd, extra, readOnly, outside, filepath.Join(cwd, "tmp")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Keep ordinary scratch grants inside CWD so the read-only fixture has no
	// independent ambient write authority.
	for _, name := range []string{"TMP", "TEMP", "TMPDIR"} {
		t.Setenv(name, filepath.Join(cwd, "tmp"))
	}
	store, err := application.Open(filepath.Join(root, "application.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	connection, err := store.Register(t.Context(), "owner", application.Registration{OperationID: "register", Name: "policy", Credential: "app-client-" + strings.Repeat("ab", 32)})
	if err != nil {
		t.Fatal(err)
	}
	profile := application.Profile{Execution: "workspace-write", Workspace: application.Workspace{CWD: cwd, Access: []application.WorkspaceAccess{{Path: extra, Mode: "read-write"}, {Path: readOnly, Mode: "read-only"}}}}
	executor, err := newApplicationExecutionRuntime(cwd, root, filepath.Join(root, "authority"), store, connection.Scope, profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	command := "printf allowed > effect.txt"
	if runtime.GOOS == "windows" {
		command = "$ErrorActionPreference='Stop'; [IO.File]::WriteAllText('effect.txt', 'allowed')"
	}
	route, err := sandboxrouter.Current("")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{cwd, extra} {
		result, err := executor.Run(t.Context(), sandbox.CommandRequest{Command: command, Dir: dir})
		if err != nil || result.ExitCode != 0 || result.Backend != route.Backend || result.Route != sandbox.RouteSandbox {
			t.Fatalf("bound command %s = %+v, %v", dir, result, err)
		}
		if data, err := os.ReadFile(filepath.Join(dir, "effect.txt")); err != nil || string(data) != "allowed" {
			t.Fatalf("bound command effect = %q, %v", data, err)
		}
	}
	for _, dir := range []string{readOnly, outside} {
		if result, err := executor.Run(t.Context(), sandbox.CommandRequest{Command: command, Dir: dir}); err == nil {
			t.Fatalf("undeclared write accepted in %s: %+v", dir, result)
		}
		if err := executor.FileSystem().WriteFile(filepath.Join(dir, "effect.txt"), []byte("denied"), 0600); err == nil {
			t.Fatalf("file tool received an undeclared write grant in %s", dir)
		}
		if _, err := os.Stat(filepath.Join(dir, "effect.txt")); !os.IsNotExist(err) {
			t.Fatalf("undeclared write created a file in %s: %v", dir, err)
		}
		link := filepath.Join(cwd, "link-"+filepath.Base(dir))
		applicationDirectoryLink(t, link, dir)
		if err := executor.FileSystem().WriteFile(filepath.Join(link, "escape.txt"), []byte("denied"), 0600); err == nil {
			t.Fatalf("directory link bypassed file-tool write authority in %s", dir)
		}
		if _, err := os.Stat(filepath.Join(dir, "escape.txt")); !os.IsNotExist(err) {
			t.Fatalf("directory link created a file outside write authority: %v", err)
		}
	}
}

// TestApplicationNativeToolEffects is opt-in because outer sandboxes may prohibit
// native execution. It exercises ordinary SDK tools and persistent Notebook bytes.
func TestApplicationNativeToolEffects(t *testing.T) {
	if os.Getenv("CAELIS_TEST_APPLICATION_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_APPLICATION_NATIVE=1 for native application execution")
	}
	copyCommand := "/bin/cat MEMORY.md > output.txt"
	if runtime.GOOS == "windows" {
		copyCommand = "[IO.File]::WriteAllText('output.txt', [IO.File]::ReadAllText('MEMORY.md'))"
	}
	root := t.TempDir()
	cwd := filepath.Join(root, "notebook")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	cwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	store, err := application.Open(filepath.Join(root, "application.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	connection, err := store.Register(t.Context(), "owner", application.Registration{OperationID: "register", Name: "native", Credential: "app-client-" + strings.Repeat("ab", 32)})
	if err != nil {
		t.Fatal(err)
	}
	profile := application.Profile{Version: "v1", Model: "configured-model", ToolsVersion: "v1", Execution: "workspace-write", Workspace: application.Workspace{CWD: cwd}}
	runtime, err := newApplicationExecutionRuntime(cwd, root, filepath.Join(root, "authority"), store, connection.Scope, profile, profile.ExecutionConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	binding := application.Binding{Scope: connection.Scope, SessionID: "session-native", Profile: profile, CreationDigest: "digest"}
	if err := store.PutBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	tools, err := newApplicationNativeTools(runtime, store, binding, cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(name, input string) {
		t.Helper()
		for _, candidate := range tools {
			if candidate.Definition().Name != name {
				continue
			}
			result, err := candidate.Call(t.Context(), tool.Call{Name: name, Input: json.RawMessage(input), Execution: tool.InvocationContext{SessionID: binding.SessionID, TurnID: "turn", ItemID: name}})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if len(result.Content) == 0 {
				t.Fatalf("%s empty result", name)
			}
			return
		}
		t.Fatalf("ordinary tool %s not assembled", name)
	}
	call("Write", `{"path":"MEMORY.md","content":"notebook identity"}`)
	call("Read", `{"path":"MEMORY.md"}`)
	input, err := json.Marshal(map[string]string{"command": copyCommand})
	if err != nil {
		t.Fatal(err)
	}
	call("RunCommand", string(input))
	got, err := os.ReadFile(filepath.Join(cwd, "output.txt"))
	if err != nil || string(got) != "notebook identity" {
		t.Fatalf("native command effect = %q, %v", got, err)
	}
	if err := store.Revoke(context.Background(), connection.Scope); err != nil {
		t.Fatal(err)
	}
	if err := runtime.FileSystem().WriteFile(filepath.Join(cwd, "after-revoke.txt"), []byte("effect"), 0600); err == nil {
		t.Fatal("revoked lease allowed delayed filesystem effect")
	}
	if _, err := os.Stat(filepath.Join(cwd, "after-revoke.txt")); !os.IsNotExist(err) {
		t.Fatalf("revoked effect created file: %v", err)
	}
	_, err = runtime.Run(t.Context(), sandbox.CommandRequest{Command: "echo revoked", Dir: cwd})
	if err == nil {
		t.Fatal("revoked lease allowed command")
	}
}
