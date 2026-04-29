package filesystem

import (
	"context"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
	"github.com/OnslaughtSnail/caelis/sdk/tool/builtin/internal/toolutil"
)

const PatchToolName = "PATCH"

type PatchTool struct {
	runtime sdksandbox.Runtime
}

func NewPatch(runtime sdksandbox.Runtime) (*PatchTool, error) {
	resolvedRuntime, err := runtimeOrDefault(runtime)
	if err != nil {
		return nil, err
	}
	return &PatchTool{runtime: resolvedRuntime}, nil
}

func (t *PatchTool) Definition() sdktool.Definition {
	return sdktool.Definition{
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

func (t *PatchTool) Call(ctx context.Context, call sdktool.Call) (sdktool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return sdktool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return sdktool.Result{}, err
	}
	fsys := fileSystemFromRuntime(t.runtime, call.Metadata)
	plan, err := planPatchMutation(fsys, args)
	if err != nil {
		return sdktool.Result{}, err
	}
	if err := fsys.WriteFile(plan.path, []byte(plan.after), plan.mode); err != nil {
		return sdktool.Result{}, err
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
		return sdktool.Result{}, err
	}
	attachMutationDiffMeta(result.Meta, plan.before, plan.after, plan.hunk)
	return result, nil
}

var _ sdktool.Tool = (*PatchTool)(nil)
