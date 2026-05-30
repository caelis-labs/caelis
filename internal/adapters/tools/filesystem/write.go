package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

type WriteFileTool struct {
	Sandbox sandbox.Runtime
}

type writeFileInput struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	CreateDirs bool   `json:"create_dirs,omitempty"`
}

func NewWriteFileTool(runtime sandbox.Runtime) (*WriteFileTool, error) {
	if runtime == nil {
		return nil, fmt.Errorf("tools/filesystem: sandbox runtime is required")
	}
	return &WriteFileTool{Sandbox: runtime}, nil
}

func (t *WriteFileTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        WriteFileToolName,
		Description: "Create or overwrite one text file.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        map[string]any{"type": "string", "description": "File path."},
				"content":     map[string]any{"type": "string", "description": "Complete new file content."},
				"create_dirs": map[string]any{"type": "boolean", "description": "Create parent directories when missing."},
			},
			"required":             []any{"path", "content"},
			"additionalProperties": false,
		},
		Meta: map[string]any{
			"caelis.permission": "write",
			"caelis.kind":       "filesystem",
		},
	}
}

func (t *WriteFileTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := checkContext(ctx); err != nil {
		return tool.Result{}, err
	}
	var input writeFileInput
	if err := decodeInput(call, &input); err != nil {
		return tool.Result{}, err
	}
	fsys, err := runtimeFileSystem(t.Sandbox, call.Meta)
	if err != nil {
		return tool.Result{}, err
	}
	target, err := normalizePath(fsys, input.Path)
	if err != nil {
		return tool.Result{}, err
	}
	before, created, mode, err := readExistingFile(fsys, target)
	if err != nil {
		return tool.Result{}, err
	}
	after := input.Content
	if input.CreateDirs {
		if err := fsys.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return tool.Result{}, err
		}
	}
	if err := fsys.WriteFile(target, []byte(after), mode); err != nil {
		return tool.Result{}, err
	}
	stats := countLineDiff(before, after)
	changed := created || before != after
	return jsonResult(call, WriteFileToolName, map[string]any{
		"path":          target,
		"created":       created,
		"changed":       changed,
		"added_lines":   stats.Added,
		"removed_lines": stats.Removed,
		"summary":       mutationSummary(created, stats.Added, stats.Removed),
	}, map[string]any{
		"path":          target,
		"created":       created,
		"changed":       changed,
		"added_lines":   stats.Added,
		"removed_lines": stats.Removed,
	})
}

func readExistingFile(fsys sandbox.FileSystem, target string) (string, bool, os.FileMode, error) {
	info, err := fsys.Stat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return "", true, 0o644, nil
	}
	if err != nil {
		return "", false, 0, err
	}
	if info.IsDir() {
		return "", false, 0, fmt.Errorf("tools/filesystem: path is a directory: %s", target)
	}
	raw, err := fsys.ReadFile(target)
	if err != nil {
		return "", false, 0, err
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	return string(raw), false, mode, nil
}

var _ tool.Tool = (*WriteFileTool)(nil)
