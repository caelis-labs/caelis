package filesystem

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
)

const PatchToolName = "Patch"

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
		Description: "Apply surgical text replacements to one file. All edits are validated against the current file before the replacement batch is written. Copy old exactly. Line-ending differences are tolerated; a unique multiline match also tolerates consistent indentation or trailing whitespace differences when new changes only line bodies. On a match failure, no edits are written; use the short diagnostic to correct old and new.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "minLength": 1, "description": "Target file."},
				"edits": map[string]any{
					"type":        "array",
					"description": "Replacements validated together and written as one batch.",
					"minItems":    1,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"old":         map[string]any{"type": "string", "minLength": 1, "description": "Text to replace; copy current text exactly."},
							"new":         map[string]any{"type": "string", "description": "Replacement text."},
							"replace_all": map[string]any{"type": "boolean", "description": "Replace all exact matches, allowing line-ending differences but no whitespace fallback. Otherwise old must identify one location."},
						},
						"required":             []string{"old", "new"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"path", "edits"},
			"additionalProperties": false,
		},
		Metadata:              toolutil.AnnotationMetadata(false, true, true, false),
		ExecutionRequirements: fileSystemExecutionRequirements(),
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
	if err := tool.RejectUnknownArgs(args, "path", "edits", retiredPatchIfRevisionArg); err != nil {
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
	payload := map[string]any{
		"path":         plan.path,
		"replacements": plan.replaced,
		"edit_count":   plan.editCount,
		"changed":      plan.before != plan.after || plan.created,
		"summary":      mutationSummary(plan.created, diffStats.Added, diffStats.Removed),
		"revision":     textRevision(plan.after),
	}
	meta := map[string]any{
		"created":        plan.created,
		"previous_empty": plan.before == "",
		"added_lines":    diffStats.Added,
		"removed_lines":  diffStats.Removed,
		"hunk":           plan.hunk,
		"revision":       textRevision(plan.after),
	}
	result, err := toolutil.JSONResult(PatchToolName, payload, meta)
	if err != nil {
		return tool.Result{}, err
	}
	attachMutationDiffMeta(result.Metadata, plan.before, plan.after, plan.hunk)
	return result, nil
}

var _ tool.Tool = (*PatchTool)(nil)
