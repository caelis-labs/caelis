package filesystem

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

const (
	defaultListLimit = 200
	maxListLimit     = 1000
)

type ListDirectoryTool struct {
	Sandbox sandbox.Runtime
}

type listDirectoryInput struct {
	Path     string `json:"path,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Metadata bool   `json:"metadata,omitempty"`
}

func NewListDirectoryTool(runtime sandbox.Runtime) (*ListDirectoryTool, error) {
	if runtime == nil {
		return nil, fmt.Errorf("tools/filesystem: sandbox runtime is required")
	}
	return &ListDirectoryTool{Sandbox: runtime}, nil
}

func (t *ListDirectoryTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ListDirectoryToolName,
		Description: "List one directory.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":     map[string]any{"type": "string", "description": "Directory path. Defaults to the workspace root."},
				"limit":    map[string]any{"type": "integer", "description": "Maximum number of entries."},
				"metadata": map[string]any{"type": "boolean", "description": "Include size, mode, and modification time."},
			},
			"additionalProperties": false,
		},
	}
}

func (t *ListDirectoryTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := checkContext(ctx); err != nil {
		return tool.Result{}, err
	}
	var input listDirectoryInput
	if err := decodeInput(call, &input); err != nil {
		return tool.Result{}, err
	}
	pathArg := input.Path
	if pathArg == "" {
		pathArg = "."
	}
	limit := clampLimit(input.Limit, defaultListLimit, maxListLimit)
	fsys, err := runtimeFileSystem(t.Sandbox, call.Meta)
	if err != nil {
		return tool.Result{}, err
	}
	target, err := normalizePath(fsys, pathArg)
	if err != nil {
		return tool.Result{}, err
	}
	items, err := fsys.ReadDir(target)
	if err != nil {
		return tool.Result{}, err
	}
	rules := workspaceExcludeRules(fsys, target)
	entries := make([]map[string]any, 0, len(items))
	for _, item := range items {
		itemPath := filepath.Join(target, item.Name())
		if shouldExclude(target, itemPath, item.IsDir(), rules) {
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
		if input.Metadata {
			info, infoErr := item.Info()
			if infoErr != nil {
				continue
			}
			entry["size"] = info.Size()
			entry["mode"] = info.Mode().String()
			entry["mod_time"] = info.ModTime().UTC().Format("2006-01-02T15:04:05Z")
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i int, j int) bool {
		left, _ := entries[i]["name"].(string)
		right, _ := entries[j]["name"].(string)
		return left < right
	})
	total := len(entries)
	truncated := total > limit
	visible := entries
	if truncated {
		visible = entries[:limit]
	}
	return jsonResult(call, ListDirectoryToolName, map[string]any{
		"path":      target,
		"entries":   visible,
		"count":     len(visible),
		"truncated": truncated,
	}, map[string]any{
		"path":        target,
		"total_count": total,
	})
}

var _ tool.Tool = (*ListDirectoryTool)(nil)
