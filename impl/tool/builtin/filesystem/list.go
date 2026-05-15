package filesystem

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/internal/toolutil"
	"github.com/OnslaughtSnail/caelis/impl/tool/internal/argparse"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const ListToolName = "LIST"

const (
	defaultListLimit = 200
	maxListLimit     = 1000
)

type ListTool struct {
	runtime sandbox.Runtime
}

func NewList(runtime sandbox.Runtime) (*ListTool, error) {
	resolvedRuntime, err := runtimeOrDefault(runtime)
	if err != nil {
		return nil, err
	}
	return &ListTool{runtime: resolvedRuntime}, nil
}

func (t *ListTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ListToolName,
		Description: "List one directory.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":  map[string]any{"type": "string", "description": "Directory path. Defaults to cwd."},
				"limit": map[string]any{"type": "integer", "description": "Max entries."},
				"metadata": map[string]any{
					"type":        "boolean",
					"description": "Include size, mode, and mtime.",
				},
			},
			"additionalProperties": false,
		},
	}
}

func (t *ListTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return tool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return tool.Result{}, err
	}
	pathArg, err := argparse.String(args, "path", false)
	if err != nil {
		return tool.Result{}, err
	}
	if pathArg == "" {
		pathArg = "."
	}
	limit, err := argparse.Int(args, "limit", defaultListLimit)
	if err != nil {
		return tool.Result{}, err
	}
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	includeMetadata, err := argparse.Bool(args, "metadata", false)
	if err != nil {
		return tool.Result{}, err
	}
	fsys := fileSystemFromRuntime(t.runtime, call.Metadata)
	target, err := normalizePathWithFS(fsys, pathArg)
	if err != nil {
		return tool.Result{}, err
	}
	items, err := fsys.ReadDir(target)
	if err != nil {
		return tool.Result{}, err
	}
	excludeRules := gitignoreExcludePatterns(fsys, target)
	out := make([]map[string]any, 0, len(items))
	full := make([]map[string]any, 0, len(items))
	for _, item := range items {
		itemPath := filepath.Join(target, item.Name())
		if shouldExcludePath(target, itemPath, item.IsDir(), excludeRules) {
			continue
		}
		entryType := "file"
		if item.IsDir() {
			entryType = "dir"
		}
		entry := map[string]any{
			"name": item.Name(),
			"path": itemPath,
			"type": entryType,
		}
		fullEntry := map[string]any{
			"name": item.Name(),
			"path": itemPath,
			"type": entryType,
		}
		if includeMetadata {
			info, infoErr := item.Info()
			if infoErr != nil {
				continue
			}
			entry["size"] = info.Size()
			entry["mode"] = info.Mode().String()
			entry["mod_time"] = info.ModTime().UTC().Format("2006-01-02T15:04:05Z")
			fullEntry["size"] = info.Size()
			fullEntry["mode"] = info.Mode().String()
			fullEntry["mod_time"] = info.ModTime().UTC().Format("2006-01-02T15:04:05Z")
		}
		out = append(out, entry)
		full = append(full, fullEntry)
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]["name"]) < fmt.Sprint(out[j]["name"])
	})
	sort.Slice(full, func(i, j int) bool {
		return fmt.Sprint(full[i]["name"]) < fmt.Sprint(full[j]["name"])
	})
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	return toolutil.JSONResult(ListToolName, map[string]any{
		"path":      target,
		"entries":   out,
		"count":     len(out),
		"truncated": truncated,
	}, map[string]any{
		"entries":     full,
		"total_count": len(full),
	})
}

var _ tool.Tool = (*ListTool)(nil)
