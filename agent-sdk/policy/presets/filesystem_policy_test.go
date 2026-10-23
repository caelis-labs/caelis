package presets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
)

func TestFilesystemWritesReuseSandboxScratchRoots(t *testing.T) {
	t.Parallel()
	roots := []string{os.TempDir()}
	if runtime.GOOS != "windows" {
		roots = append(roots, "/tmp", "/var/tmp", "/private/tmp")
	}
	for _, root := range roots {
		for _, name := range []string{filesystem.WriteToolName, filesystem.PatchToolName} {
			t.Run(name+"/"+root, func(t *testing.T) {
				// A narrower app-provided temp root must not hide the existing
				// scratch write authority enforced by the sandbox filesystem.
				input := filesystemWriteContext(t, name, filepath.Join(root, "caelis-scratch-test", "new", "file.txt"))
				decision, err := WorkspaceWriteMode().DecideTool(t.Context(), input)
				if err != nil || decision.Action != policy.ActionAllow {
					t.Fatalf("decision=%+v error=%v, want no approval", decision, err)
				}
				if decision.Approval != nil || decision.Constraints.Route != sandbox.RouteSandbox {
					t.Fatalf("scratch write changed execution authority: %+v", decision)
				}
			})
		}
	}
}

func TestFilesystemScratchAllowlistDoesNotMatchSiblingOrParentPaths(t *testing.T) {
	t.Parallel()
	paths := []string{testOutsidePath(), filepath.Join(testWorkspaceRoot()+"-other", "file.txt")}
	if runtime.GOOS != "windows" {
		paths = append(paths, "/tmp-other/file.txt", "/var/tmp-other/file.txt", "/private/tmp-other/file.txt", "/tmp/../outside/file.txt")
	}
	for _, path := range paths {
		decision, err := WorkspaceWriteMode().DecideTool(t.Context(), filesystemWriteContext(t, filesystem.PatchToolName, path))
		if err != nil || decision.Action != policy.ActionAskApproval {
			t.Fatalf("%s: decision=%+v error=%v, want scoped approval", path, decision, err)
		}
	}
}

func TestFilesystemScratchSymlinksCannotBypassWriteApproval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink fixture; Windows junction containment has native fsboundary coverage")
	}
	t.Parallel()
	root := t.TempDir()
	link := filepath.Join(root, "outside")
	if err := os.Symlink(filepath.Dir(testOutsidePath()), link); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{filepath.Base(testOutsidePath()), filepath.Join("new", "file.txt")} {
		input := filesystemWriteContext(t, filesystem.WriteToolName, filepath.Join(link, suffix))
		// Also exercise an explicit lexical temp-root match. It must not
		// authorize the physical path beyond that root through a symlink.
		input.Options.TempRoot = root
		decision, err := WorkspaceWriteMode().DecideTool(t.Context(), input)
		if err != nil || decision.Action != policy.ActionAskApproval {
			t.Fatalf("decision=%+v error=%v, want approval for escaped target", decision, err)
		}
	}
}

func TestFilesystemScratchSymlinksCannotBypassSensitivePathDeny(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink fixture; Windows junction containment has native fsboundary coverage")
	}
	home := t.TempDir()
	setHomeForPresetsTest(t, home)
	secret := filepath.Join(home, ".ssh")
	if err := os.Mkdir(secret, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "sensitive")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filesystem.WriteToolName, filesystem.PatchToolName} {
		input := filesystemWriteContext(t, name, filepath.Join(link, "new-key"))
		decision, err := WorkspaceWriteMode().DecideTool(t.Context(), input)
		if err != nil || decision.Action != policy.ActionDeny {
			t.Fatalf("decision=%+v error=%v, want sensitive path denied", decision, err)
		}
	}
}

func filesystemWriteContext(t *testing.T, name, path string) policy.ToolContext {
	t.Helper()
	input := writeCtx(path)
	input.Tool.Name, input.Call.Name = name, name
	input.Options.TempRoot = filepath.Join(testWorkspaceRoot(), "scratch")
	if name == filesystem.PatchToolName {
		raw, err := json.Marshal(map[string]any{"path": path, "edits": []map[string]any{{"old": "before", "new": "after"}}})
		if err != nil {
			t.Fatal(err)
		}
		input.Call.Input = raw
	}
	return input
}
