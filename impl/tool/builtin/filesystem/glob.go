package filesystem

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/internal/toolutil"
	"github.com/OnslaughtSnail/caelis/impl/tool/internal/argparse"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const GlobToolName = "GLOB"

const (
	defaultGlobLimit = 200
	maxGlobLimit     = 1000
)

type GlobTool struct {
	runtime sandbox.Runtime
}

func NewGlob(runtime sandbox.Runtime) (*GlobTool, error) {
	resolvedRuntime, err := runtimeOrDefault(runtime)
	if err != nil {
		return nil, err
	}
	return &GlobTool{runtime: resolvedRuntime}, nil
}

func (t *GlobTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        GlobToolName,
		Description: "Find files by glob.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Glob pattern."},
				"exclude": map[string]any{
					"type":        "array",
					"description": "Relative exclude globs.",
					"items":       map[string]any{"type": "string"},
				},
				"limit": map[string]any{"type": "integer", "description": "Max matches."},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
	}
}

func (t *GlobTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return tool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return tool.Result{}, err
	}
	pattern, err := argparse.String(args, "pattern", true)
	if err != nil {
		return tool.Result{}, err
	}
	exclude, err := parseStringSliceArg(args, "exclude")
	if err != nil {
		return tool.Result{}, err
	}
	limit, err := argparse.Int(args, "limit", defaultGlobLimit)
	if err != nil {
		return tool.Result{}, err
	}
	if limit <= 0 {
		limit = defaultGlobLimit
	}
	if limit > maxGlobLimit {
		limit = maxGlobLimit
	}
	fsys := fileSystemFromRuntime(t.runtime, call.Metadata)
	if !filepath.IsAbs(pattern) {
		wd, err := fsys.Getwd()
		if err != nil {
			return tool.Result{}, err
		}
		pattern = filepath.Join(wd, pattern)
	}
	pattern = filepath.Clean(pattern)

	matches := make([]string, 0, 16)
	if !hasPathGlobMeta(filepath.ToSlash(pattern)) {
		if info, err := fsys.Stat(pattern); err == nil {
			root := filepath.Dir(pattern)
			excludeRules := append(workspaceExcludeRules(fsys, root), excludeRulesFromPatterns(exclude)...)
			if !shouldExcludePath(root, pattern, info.IsDir(), excludeRules) {
				matches = append(matches, pattern)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return tool.Result{}, err
		}
		sort.Strings(matches)
		return globResult(pattern, matches, limit)
	}

	root, relPattern := splitAbsoluteGlobPattern(pattern)
	if relPattern == "" {
		relPattern = filepath.Base(pattern)
	}
	excludeRules := append(workspaceExcludeRules(fsys, root), excludeRulesFromPatterns(exclude)...)
	if _, err := fsys.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return globResult(pattern, matches, limit)
		}
		return tool.Result{}, err
	}
	maxMatches := limit + 1
	err = walkDir(fsys, root, func(candidate string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d == nil {
			return nil
		}
		if candidate != root && shouldExcludePath(root, candidate, d.IsDir(), excludeRules) {
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
			if len(matches) >= maxMatches {
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return tool.Result{}, err
	}
	sort.Strings(matches)
	return globResult(pattern, matches, limit)
}

func globResult(pattern string, matches []string, limit int) (tool.Result, error) {
	truncated := len(matches) > limit
	visible := append([]string(nil), matches...)
	if truncated {
		visible = visible[:limit]
	}
	return toolutil.JSONResult(GlobToolName, map[string]any{
		"matches":   visible,
		"count":     len(visible),
		"truncated": truncated,
	}, map[string]any{
		"pattern":     pattern,
		"matches":     append([]string(nil), matches...),
		"total_count": len(matches),
	})
}

var _ tool.Tool = (*GlobTool)(nil)
