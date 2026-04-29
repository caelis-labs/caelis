package filesystem

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
	"github.com/OnslaughtSnail/caelis/sdk/tool/builtin/internal/toolutil"
	"github.com/OnslaughtSnail/caelis/sdk/tool/internal/argparse"
)

const ListToolName = "LIST"

type ListTool struct {
	runtime sdksandbox.Runtime
}

func NewList(runtime sdksandbox.Runtime) (*ListTool, error) {
	resolvedRuntime, err := runtimeOrDefault(runtime)
	if err != nil {
		return nil, err
	}
	return &ListTool{runtime: resolvedRuntime}, nil
}

func (t *ListTool) Definition() sdktool.Definition {
	return sdktool.Definition{
		Name:        ListToolName,
		Description: "List files and directories in one path.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":              map[string]any{"type": "string", "description": "directory path"},
				"respect_gitignore": map[string]any{"type": "boolean", "description": "When true, filter direct entries ignored by .gitignore in the listed directory."},
			},
		},
	}
}

func (t *ListTool) Call(ctx context.Context, call sdktool.Call) (sdktool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return sdktool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return sdktool.Result{}, err
	}
	pathArg, err := argparse.String(args, "path", false)
	if err != nil {
		return sdktool.Result{}, err
	}
	if pathArg == "" {
		pathArg = "."
	}
	respectGitignore, err := argparse.Bool(args, "respect_gitignore", false)
	if err != nil {
		return sdktool.Result{}, err
	}
	fsys := fileSystemFromRuntime(t.runtime, call.Metadata)
	target, err := normalizePathWithFS(fsys, pathArg)
	if err != nil {
		return sdktool.Result{}, err
	}
	items, err := fsys.ReadDir(target)
	if err != nil {
		return sdktool.Result{}, err
	}
	excludeRules := []pathExcludeRule(nil)
	if respectGitignore {
		excludeRules = gitignoreExcludePatterns(fsys, target)
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		itemPath := filepath.Join(target, item.Name())
		if shouldExcludePath(target, itemPath, item.IsDir(), excludeRules) {
			continue
		}
		info, infoErr := item.Info()
		if infoErr != nil {
			continue
		}
		out = append(out, map[string]any{
			"name":     item.Name(),
			"path":     itemPath,
			"is_dir":   item.IsDir(),
			"size":     info.Size(),
			"mode":     info.Mode().String(),
			"mod_time": info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]["name"]) < fmt.Sprint(out[j]["name"])
	})
	return toolutil.JSONResult(ListToolName, map[string]any{
		"path":    target,
		"entries": out,
		"count":   len(out),
	})
}

var _ sdktool.Tool = (*ListTool)(nil)
