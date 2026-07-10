package filesystem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
)

const WriteToolName = "WRITE"

type WriteTool struct {
	runtime sandbox.Runtime
}

func NewWrite(runtime sandbox.Runtime) (*WriteTool, error) {
	resolvedRuntime, err := runtimeOrDefault(runtime)
	if err != nil {
		return nil, err
	}
	return &WriteTool{runtime: resolvedRuntime}, nil
}

func (t *WriteTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        WriteToolName,
		Description: "Create a new file or intentionally replace the full contents of one file. Prefer PATCH for localized edits to existing files because WRITE overwrites the entire target. Include if_revision when replacing a file previously read.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "minLength": 1, "description": "Target file."},
				"content": map[string]any{"type": "string", "description": "Full new contents."},
				"if_revision": map[string]any{
					"type":        "string",
					"description": "Revision guard from READ.",
				},
			},
			"required":             []string{"path", "content"},
			"additionalProperties": false,
		},
		Metadata:              toolutil.AnnotationMetadata(false, true, true, false),
		ExecutionRequirements: fileSystemExecutionRequirements(),
	}
}

func (t *WriteTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return tool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return tool.Result{}, err
	}
	if err := tool.RejectUnknownArgs(args, "path", "content", "if_revision"); err != nil {
		return tool.Result{}, err
	}
	fsys := fileSystemFromRuntime(t.runtime, call.Metadata)
	plan, err := planWriteMutation(fsys, args)
	if err != nil {
		return tool.Result{}, err
	}
	if plan.created {
		if err := createWorkspaceParentDirs(fsys, plan.path); err != nil {
			return tool.Result{}, err
		}
	}
	if err := fsys.WriteFile(plan.path, []byte(plan.after), plan.mode); err != nil {
		return tool.Result{}, err
	}
	diffStats := CountLineDiff(plan.before, plan.after)
	payload := map[string]any{
		"path":     plan.path,
		"changed":  plan.before != plan.after || plan.created,
		"summary":  mutationSummary(plan.created, diffStats.Added, diffStats.Removed),
		"revision": textRevision(plan.after),
	}
	meta := map[string]any{
		"created":        plan.created,
		"previous_empty": plan.before == "",
		"bytes_written":  len([]byte(plan.after)),
		"line_count":     lineCount(plan.after),
		"added_lines":    diffStats.Added,
		"removed_lines":  diffStats.Removed,
		"revision":       textRevision(plan.after),
	}
	result, err := toolutil.JSONResult(WriteToolName, payload, meta)
	if err != nil {
		return tool.Result{}, err
	}
	attachMutationDiffMeta(result.Metadata, plan.before, plan.after, plan.hunk)
	return result, nil
}

type mkdirAllFileSystem interface {
	MkdirAll(path string, perm os.FileMode) error
}

func createWorkspaceParentDirs(fsys sandbox.FileSystem, target string) error {
	mkdirer, ok := fsys.(mkdirAllFileSystem)
	if !ok {
		return nil
	}
	cwd, err := fsys.Getwd()
	if err != nil {
		return err
	}
	cwd = filepath.Clean(cwd)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(cwd, target)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	parent := filepath.Dir(target)
	if parent == "." || parent == target || parent == cwd {
		return nil
	}
	return mkdirer.MkdirAll(parent, 0o755)
}

func lineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func mutationSummary(created bool, added int, removed int) string {
	action := "updated"
	if created {
		action = "created"
	}
	return action + " (" + lineDeltaSummary(added, removed) + ")"
}

func lineDeltaSummary(added int, removed int) string {
	switch {
	case added > 0 && removed > 0:
		return fmt.Sprintf("+%d/-%d lines", added, removed)
	case added > 0:
		return fmt.Sprintf("+%d lines", added)
	case removed > 0:
		return fmt.Sprintf("-%d lines", removed)
	default:
		return "no line changes"
	}
}

var _ tool.Tool = (*WriteTool)(nil)
