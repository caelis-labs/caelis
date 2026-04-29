package filesystem

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
	"github.com/OnslaughtSnail/caelis/sdk/tool/builtin/internal/toolutil"
	"github.com/OnslaughtSnail/caelis/sdk/tool/internal/argparse"
)

const GlobToolName = "GLOB"

type GlobTool struct {
	runtime sdksandbox.Runtime
}

func NewGlob(runtime sdksandbox.Runtime) (*GlobTool, error) {
	resolvedRuntime, err := runtimeOrDefault(runtime)
	if err != nil {
		return nil, err
	}
	return &GlobTool{runtime: resolvedRuntime}, nil
}

func (t *GlobTool) Definition() sdktool.Definition {
	return sdktool.Definition{
		Name:        GlobToolName,
		Description: "Match files by glob pattern.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "glob pattern"},
				"exclude": map[string]any{
					"type":        "array",
					"description": "Optional relative path patterns to exclude after filtering.",
					"items":       map[string]any{"type": "string"},
				},
				"respect_gitignore": map[string]any{"type": "boolean", "description": "When true, filter matches ignored by .gitignore at the search root."},
			},
			"required": []string{"pattern"},
		},
	}
}

func (t *GlobTool) Call(ctx context.Context, call sdktool.Call) (sdktool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return sdktool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return sdktool.Result{}, err
	}
	pattern, err := argparse.String(args, "pattern", true)
	if err != nil {
		return sdktool.Result{}, err
	}
	exclude, err := parseStringSliceArg(args, "exclude")
	if err != nil {
		return sdktool.Result{}, err
	}
	respectGitignore, err := argparse.Bool(args, "respect_gitignore", false)
	if err != nil {
		return sdktool.Result{}, err
	}
	fsys := fileSystemFromRuntime(t.runtime, call.Metadata)
	if !filepath.IsAbs(pattern) {
		wd, err := fsys.Getwd()
		if err != nil {
			return sdktool.Result{}, err
		}
		pattern = filepath.Join(wd, pattern)
	}
	pattern = filepath.Clean(pattern)

	matches := make([]string, 0, 16)
	if !hasPathGlobMeta(filepath.ToSlash(pattern)) {
		if info, err := fsys.Stat(pattern); err == nil {
			root := filepath.Dir(pattern)
			excludeRules := excludeRulesFromPatterns(exclude)
			if respectGitignore {
				excludeRules = append(gitignoreExcludePatterns(fsys, root), excludeRules...)
			}
			if !shouldExcludePath(root, pattern, info.IsDir(), excludeRules) {
				matches = append(matches, pattern)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return sdktool.Result{}, err
		}
		sort.Strings(matches)
		return toolutil.JSONResult(GlobToolName, map[string]any{
			"pattern": pattern,
			"matches": matches,
			"count":   len(matches),
		})
	}

	root, relPattern := splitAbsoluteGlobPattern(pattern)
	if relPattern == "" {
		relPattern = filepath.Base(pattern)
	}
	excludeRules := excludeRulesFromPatterns(exclude)
	if respectGitignore {
		excludeRules = append(gitignoreExcludePatterns(fsys, root), excludeRules...)
	}
	if _, err := fsys.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return toolutil.JSONResult(GlobToolName, map[string]any{
				"pattern": pattern,
				"matches": matches,
				"count":   0,
			})
		}
		return sdktool.Result{}, err
	}
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
		}
		return nil
	})
	if err != nil {
		return sdktool.Result{}, err
	}
	sort.Strings(matches)
	return toolutil.JSONResult(GlobToolName, map[string]any{
		"pattern": pattern,
		"matches": matches,
		"count":   len(matches),
	})
}

var _ sdktool.Tool = (*GlobTool)(nil)
