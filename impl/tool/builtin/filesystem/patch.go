package filesystem

import (
	"context"

	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/internal/toolutil"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const PatchToolName = "PATCH"

type PatchTool struct {
	runtime sandbox.Runtime
}

func NewPatch(runtime sandbox.Runtime) (*PatchTool, error) {
	resolvedRuntime, err := runtimeOrDefault(runtime)
	if err != nil {
		return nil, err
	}
	return &PatchTool{runtime: resolvedRuntime}, nil
}

func (t *PatchTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        PatchToolName,
		Description: "Patch one file by exact old-to-new replacement.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        map[string]any{"type": "string", "description": "Target file path."},
				"old":         map[string]any{"type": "string", "description": "Exact original text to replace."},
				"new":         map[string]any{"type": "string", "description": "Replacement text."},
				"replace_all": map[string]any{"type": "boolean", "description": "Replace all occurrences instead of one."},
			},
			"required": []string{"path", "old", "new"},
		},
	}
}

func (t *PatchTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return tool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return tool.Result{}, err
	}
	fsys := fileSystemFromRuntime(t.runtime, call.Metadata)
	plan, err := planPatchMutation(fsys, args)
	if err != nil {
		return tool.Result{}, err
	}
	if err := fsys.WriteFile(plan.path, []byte(plan.after), plan.mode); err != nil {
		return tool.Result{}, err
	}
	diffStats := CountLineDiff(plan.before, plan.after)
	result, err := toolutil.JSONResult(PatchToolName, map[string]any{
		"path":           plan.path,
		"replaced":       plan.replaced,
		"created":        plan.created,
		"previous_empty": plan.before == "",
		"added_lines":    diffStats.Added,
		"removed_lines":  diffStats.Removed,
		"hunk":           plan.hunk,
	})
	if err != nil {
		return tool.Result{}, err
	}
	attachMutationDiffMeta(result.Meta, plan.before, plan.after, plan.hunk)
	return result, nil
}

var _ tool.Tool = (*PatchTool)(nil)
