package presets

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	backendpolicy "github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policyfs"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
)

func TestFilesystemScratchWriteAndPatchMatchSandboxEnforcement(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tempRoot := os.TempDir()
	if runtime.GOOS != "windows" {
		tempRoot = "/tmp"
	}
	scratch, err := os.MkdirTemp(tempRoot, "caelis-policy-scratch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(scratch); err != nil {
			t.Error(err)
		}
	})
	base, err := host.New(host.Config{CWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(scratch, "protected.txt")
	rt := scratchFileSystemRuntime{Runtime: base, cfg: sandbox.Config{
		CWD: workspace, WritableRoots: []string{workspace}, ReadOnlySubpaths: []string{blocked},
	}}
	write, err := filesystem.NewWrite(rt)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := filesystem.NewPatch(rt)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(scratch, "file.txt")
	for _, step := range []struct {
		tool tool.Tool
		args map[string]any
	}{
		{write, map[string]any{"path": target, "content": "before\n"}},
		{patch, map[string]any{"path": target, "edits": []map[string]any{{"old": "before", "new": "after"}}}},
	} {
		raw, err := json.Marshal(step.args)
		if err != nil {
			t.Fatal(err)
		}
		call := tool.Call{Name: step.tool.Definition().Name, Input: raw}
		input := policy.ToolContext{Tool: step.tool.Definition(), Call: call, Options: policy.ModeOptions{
			WorkspaceRoot: workspace, WritableRoots: []string{workspace}, TempRoot: filepath.Join(workspace, "narrow-temp"),
		}}
		decision, err := WorkspaceWriteMode().DecideTool(t.Context(), input)
		if err != nil || decision.Action != policy.ActionAllow || decision.Approval != nil {
			t.Fatalf("%s action=%s error=%v, want no approval", call.Name, decision.Action, err)
		}
		call.Metadata = map[string]any{"sandbox_constraints": decision.Constraints}
		if _, err := step.tool.Call(t.Context(), call); err != nil {
			t.Fatalf("%s sandbox filesystem rejected approved scratch path: %v", call.Name, err)
		}
		// Scratch admission must not add a target override that could reopen
		// protected files in the underlying sandbox filesystem.
		if err := rt.FileSystemFor(decision.Constraints).WriteFile(blocked, []byte("blocked"), 0600); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("protected scratch write error=%v, want permission denied", err)
		}
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "after\n" {
		t.Fatalf("file=%q error=%v, want actual Write then Patch effects", data, err)
	}
}

// File tools use the same constraint-aware policyfs composition as native
// backends; no command execution or additional filesystem grant is needed.
type scratchFileSystemRuntime struct {
	sandbox.Runtime
	cfg sandbox.Config
}

func (r scratchFileSystemRuntime) FileSystemFor(constraints sandbox.Constraints) sandbox.FileSystem {
	return policyfs.New(r.FileSystem(), func() backendpolicy.Policy {
		return backendpolicy.Default(r.cfg, constraints)
	})
}
