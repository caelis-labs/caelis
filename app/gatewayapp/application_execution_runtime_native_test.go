//go:build darwin

package gatewayapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/application"
)

func TestApplicationDarwinNativeWorkspaceAccess(t *testing.T) {
	if err := validateApplicationExecutionPlatform("workspace-write"); err != nil {
		t.Fatalf("same-user Seatbelt native execution should be available: %v", err)
	}
	root := t.TempDir()
	notebook := filepath.Join(root, "notebook")
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

// TestApplicationNativeToolEffectsOnDarwin is opt-in because outer sandboxes
// may prohibit sandbox_apply. It exercises ordinary SDK tools, native Seatbelt
// command execution and real persistent Notebook bytes, not merely registration.
func TestApplicationNativeToolEffectsOnDarwin(t *testing.T) {
	if os.Getenv("CAELIS_TEST_APPLICATION_NATIVE") != "1" {
		t.Skip("set CAELIS_TEST_APPLICATION_NATIVE=1 for native Seatbelt execution")
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
	runtime, err := newApplicationExecutionRuntime(cwd, root, store, connection.Scope, profile)
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
	call("RunCommand", `{"command":"/bin/cat MEMORY.md > output.txt"}`)
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
	_, err = runtime.Run(t.Context(), sandbox.CommandRequest{Command: "/bin/echo revoked", Dir: cwd})
	if err == nil {
		t.Fatal("revoked lease allowed command")
	}
}
