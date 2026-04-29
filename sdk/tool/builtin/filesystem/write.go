package filesystem

import (
	"context"
	"strings"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
	"github.com/OnslaughtSnail/caelis/sdk/tool/builtin/internal/toolutil"
)

const WriteToolName = "WRITE"

type WriteTool struct {
	runtime sdksandbox.Runtime
}

func NewWrite(runtime sdksandbox.Runtime) (*WriteTool, error) {
	resolvedRuntime, err := runtimeOrDefault(runtime)
	if err != nil {
		return nil, err
	}
	return &WriteTool{runtime: resolvedRuntime}, nil
}

func (t *WriteTool) Definition() sdktool.Definition {
	return sdktool.Definition{
		Name:        WriteToolName,
		Description: "Write complete file contents to one path.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Target file path."},
				"content": map[string]any{"type": "string", "description": "Full file contents to write."},
			},
			"required": []string{"path", "content"},
		},
	}
}

func (t *WriteTool) Call(ctx context.Context, call sdktool.Call) (sdktool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return sdktool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return sdktool.Result{}, err
	}
	fsys := fileSystemFromRuntime(t.runtime, call.Metadata)
	plan, err := planWriteMutation(fsys, args)
	if err != nil {
		return sdktool.Result{}, err
	}
	if err := fsys.WriteFile(plan.path, []byte(plan.after), plan.mode); err != nil {
		return sdktool.Result{}, err
	}
	diffStats := CountLineDiff(plan.before, plan.after)
	result, err := toolutil.JSONResult(WriteToolName, map[string]any{
		"path":           plan.path,
		"created":        plan.created,
		"previous_empty": plan.before == "",
		"bytes_written":  len([]byte(plan.after)),
		"line_count":     lineCount(plan.after),
		"added_lines":    diffStats.Added,
		"removed_lines":  diffStats.Removed,
	})
	if err != nil {
		return sdktool.Result{}, err
	}
	attachMutationDiffMeta(result.Meta, plan.before, plan.after, plan.hunk)
	return result, nil
}

func lineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

var _ sdktool.Tool = (*WriteTool)(nil)
