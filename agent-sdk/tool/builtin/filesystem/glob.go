package filesystem

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/argparse"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
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
		Description: "Find filesystem paths matching a glob pattern under a search directory.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "minLength": 1, "description": "Path glob to match, relative to path."},
				"path":    map[string]any{"type": "string", "minLength": 1, "description": "Directory to search. Defaults to cwd."},
				"exclude": stringOrStringArraySchema("File glob or globs to exclude, relative to path."),
				"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": maxGlobLimit, "description": "Max matches."},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
		Metadata: toolutil.AnnotationMetadata(true, false, true, false),
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
	if err := tool.RejectUnknownArgs(args, "pattern", "path", "exclude", "limit"); err != nil {
		return tool.Result{}, err
	}
	pattern, err := argparse.String(args, "pattern", true)
	if err != nil {
		return tool.Result{}, err
	}
	pathArg, err := argparse.String(args, "path", false)
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
	searchRoot, err := globSearchRoot(fsys, pathArg)
	if err != nil {
		return tool.Result{}, err
	}
	resolvedPattern := pattern
	if filepath.IsAbs(pattern) {
		if searchRoot != "" {
			err := tool.NewError(tool.ErrorCodeInvalidInput, "GLOB pattern must be relative when path is provided")
			err.Hint = "Use path for the search directory and pattern for a relative glob such as \"*.py\" or \"**/*.py\"."
			return tool.Result{}, err
		}
	} else {
		if searchRoot == "" {
			searchRoot, err = fsys.Getwd()
			if err != nil {
				return tool.Result{}, err
			}
			searchRoot = filepath.Clean(searchRoot)
		}
		resolvedPattern = filepath.Join(searchRoot, pattern)
	}
	resolvedPattern = filepath.Clean(resolvedPattern)
	if pathArg != "" {
		if err := validateGlobPatternUnderSearchRoot(searchRoot, resolvedPattern); err != nil {
			return tool.Result{}, err
		}
	}

	matches := make([]string, 0, 16)
	if !hasPathGlobMeta(filepath.ToSlash(resolvedPattern)) {
		if info, err := fsys.Stat(resolvedPattern); err == nil {
			root := filepath.Dir(resolvedPattern)
			excludeRules := append(workspaceExcludeRules(fsys, root), excludeRulesFromPatterns(exclude)...)
			if !shouldExcludePath(root, resolvedPattern, info.IsDir(), excludeRules) {
				matches = append(matches, resolvedPattern)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return tool.Result{}, err
		}
		sort.Strings(matches)
		return globResult(pattern, searchRoot, resolvedPattern, matches, limit)
	}

	root, relPattern := splitAbsoluteGlobPattern(resolvedPattern)
	if relPattern == "" {
		relPattern = filepath.Base(resolvedPattern)
	}
	excludeRules := append(workspaceExcludeRules(fsys, root), excludeRulesFromPatterns(exclude)...)
	if _, err := fsys.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return globResult(pattern, searchRoot, resolvedPattern, matches, limit)
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
	return globResult(pattern, searchRoot, resolvedPattern, matches, limit)
}

func globSearchRoot(fsys sandbox.FileSystem, pathArg string) (string, error) {
	if pathArg == "" {
		return "", nil
	}
	root, err := normalizePathWithFS(fsys, pathArg)
	if err != nil {
		return "", err
	}
	info, err := fsys.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", tool.NewError(tool.ErrorCodeNotFound, "GLOB path does not exist: "+root)
		}
		return "", err
	}
	if !info.IsDir() {
		return "", tool.NewError(tool.ErrorCodeInvalidInput, "GLOB path must be a directory: "+root)
	}
	return root, nil
}

func validateGlobPatternUnderSearchRoot(searchRoot string, resolvedPattern string) error {
	rel, err := filepath.Rel(searchRoot, resolvedPattern)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		toolErr := tool.NewError(tool.ErrorCodeInvalidInput, "GLOB pattern must stay under path: "+searchRoot)
		toolErr.Hint = "Use path for the search directory and a pattern inside it, such as \"*.py\" or \"**/*.py\"."
		return toolErr
	}
	return nil
}

func globResult(pattern string, searchRoot string, resolvedPattern string, matches []string, limit int) (tool.Result, error) {
	truncated := len(matches) > limit
	visible := append([]string(nil), matches...)
	if truncated {
		visible = visible[:limit]
	}
	payload := map[string]any{
		"pattern":   pattern,
		"matches":   visible,
		"count":     len(visible),
		"truncated": truncated,
	}
	meta := map[string]any{
		"pattern":          pattern,
		"resolved_pattern": resolvedPattern,
		"matches":          append([]string(nil), matches...),
		"total_count":      len(matches),
	}
	if searchRoot != "" {
		payload["path"] = searchRoot
		meta["path"] = searchRoot
	}
	return toolutil.JSONResult(GlobToolName, payload, meta)
}

var _ tool.Tool = (*GlobTool)(nil)
