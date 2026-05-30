package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

const (
	defaultGlobLimit = 200
	maxGlobLimit     = 1000
)

type GlobFilesTool struct {
	Sandbox sandbox.Runtime
}

type globFilesInput struct {
	Pattern string   `json:"pattern"`
	Exclude []string `json:"exclude,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}

func NewGlobFilesTool(runtime sandbox.Runtime) (*GlobFilesTool, error) {
	if runtime == nil {
		return nil, fmt.Errorf("tools/filesystem: sandbox runtime is required")
	}
	return &GlobFilesTool{Sandbox: runtime}, nil
}

func (t *GlobFilesTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        GlobFilesToolName,
		Description: "Find files by glob pattern.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Glob pattern."},
				"exclude": map[string]any{
					"type":        "array",
					"description": "Relative exclude glob patterns.",
					"items":       map[string]any{"type": "string"},
				},
				"limit": map[string]any{"type": "integer", "description": "Maximum number of matches."},
			},
			"required":             []any{"pattern"},
			"additionalProperties": false,
		},
	}
}

func (t *GlobFilesTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := checkContext(ctx); err != nil {
		return tool.Result{}, err
	}
	var input globFilesInput
	if err := decodeInput(call, &input); err != nil {
		return tool.Result{}, err
	}
	limit := clampLimit(input.Limit, defaultGlobLimit, maxGlobLimit)
	fsys, err := runtimeFileSystem(t.Sandbox, call.Meta)
	if err != nil {
		return tool.Result{}, err
	}
	pattern, err := normalizePath(fsys, input.Pattern)
	if err != nil {
		return tool.Result{}, err
	}
	pattern = filepath.Clean(pattern)
	matches, err := globMatches(ctx, fsys, pattern, input.Exclude, limit+1)
	if err != nil {
		return tool.Result{}, err
	}
	sort.Strings(matches)
	truncated := len(matches) > limit
	visible := matches
	if truncated {
		visible = matches[:limit]
	}
	return jsonResult(call, GlobFilesToolName, map[string]any{
		"pattern":   pattern,
		"matches":   visible,
		"count":     len(visible),
		"truncated": truncated,
	}, map[string]any{
		"pattern":     pattern,
		"total_count": len(matches),
	})
}

func globMatches(ctx context.Context, fsys sandbox.FileSystem, pattern string, exclude []string, max int) ([]string, error) {
	matches := make([]string, 0, 16)
	if !hasPathGlobMeta(filepath.ToSlash(pattern)) {
		info, err := fsys.Stat(pattern)
		if errors.Is(err, fs.ErrNotExist) {
			return matches, nil
		}
		if err != nil {
			return nil, err
		}
		root := filepath.Dir(pattern)
		rules := append(workspaceExcludeRules(fsys, root), parseExclude(exclude)...)
		if !shouldExclude(root, pattern, info.IsDir(), rules) {
			matches = append(matches, pattern)
		}
		return matches, nil
	}
	root, relPattern := splitAbsoluteGlobPattern(pattern)
	if relPattern == "" {
		relPattern = filepath.Base(pattern)
	}
	if _, err := fsys.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return matches, nil
		}
		return nil, err
	}
	rules := append(workspaceExcludeRules(fsys, root), parseExclude(exclude)...)
	walkErr := walkDir(fsys, root, func(candidate string, d fs.DirEntry, walkErr error) error {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if walkErr != nil || d == nil {
			return nil
		}
		if candidate != root && shouldExclude(root, candidate, d.IsDir(), rules) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, candidate)
		if err != nil || rel == "." {
			return nil
		}
		if pathGlobMatch(relPattern, rel) {
			matches = append(matches, candidate)
			if max > 0 && len(matches) >= max {
				return fs.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return nil, walkErr
	}
	return matches, nil
}

var _ tool.Tool = (*GlobFilesTool)(nil)
