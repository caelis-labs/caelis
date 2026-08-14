package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/argparse"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

const GlobToolName = names.Glob

const globResultLimit = 100

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
		Description: "Find files matching a glob pattern. Use * for direct children and **/* for recursive discovery. Results are capped at 100 files; use RunCommand with rg --files for advanced discovery.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "minLength": 1, "description": "File glob to match, relative to path."},
				"path":    map[string]any{"type": "string", "minLength": 1, "description": "Directory to search. Defaults to cwd."},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
		Metadata:              toolutil.AnnotationMetadata(true, false, true, false),
		ExecutionRequirements: fileSystemExecutionRequirements(),
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
	if err := tool.RejectUnknownArgs(args, "pattern", "path"); err != nil {
		return tool.Result{}, err
	}
	pattern, err := argparse.String(args, "pattern", true)
	if err != nil {
		return tool.Result{}, err
	}
	if filepath.IsAbs(pattern) {
		toolErr := tool.NewError(tool.ErrorCodeInvalidInput, "Glob pattern must be relative")
		toolErr.Hint = "Put the search directory in path and use a relative pattern such as \"*.py\" or \"**/*.py\"."
		return tool.Result{}, toolErr
	}
	if err := validatePathGlobPattern(pattern, "Glob pattern"); err != nil {
		return tool.Result{}, err
	}
	pathArg, err := argparse.String(args, "path", false)
	if err != nil {
		return tool.Result{}, err
	}
	if pathArg == "" {
		pathArg = "."
	}
	fsys := fileSystemFromRuntime(t.runtime, call.Metadata)
	searchRoot, err := globSearchRoot(fsys, pathArg)
	if err != nil {
		return tool.Result{}, err
	}
	resolvedPattern := filepath.Clean(filepath.Join(searchRoot, pattern))
	if err := validateGlobPatternUnderSearchRoot(searchRoot, resolvedPattern); err != nil {
		return tool.Result{}, err
	}

	matches := make([]string, 0, 16)
	if !hasPathGlobMeta(filepath.ToSlash(resolvedPattern)) {
		if info, err := fsys.Stat(resolvedPattern); err == nil {
			root := filepath.Dir(resolvedPattern)
			excludeRules := workspaceExcludeRules(fsys, root)
			if !info.IsDir() && !shouldExcludePath(root, resolvedPattern, false, excludeRules) {
				matches = append(matches, resolvedPattern)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return tool.Result{}, err
		}
		sort.Strings(matches)
		return globResult(pattern, searchRoot, resolvedPattern, matches)
	}

	root, relPattern := splitAbsoluteGlobPattern(resolvedPattern)
	if relPattern == "" {
		relPattern = filepath.Base(resolvedPattern)
	}
	excludeRules := workspaceExcludeRules(fsys, root)
	if _, err := fsys.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return globResult(pattern, searchRoot, resolvedPattern, matches)
		}
		return tool.Result{}, err
	}
	maxMatches := globResultLimit + 1
	err = walkDir(fsys, root, func(candidate string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d == nil {
			//nolint:nilerr // Glob is best-effort across unreadable entries and returns the remaining matches.
			return nil
		}
		if candidate != root && shouldExcludePath(root, candidate, d.IsDir(), excludeRules) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, candidate)
		if err != nil || rel == "." {
			//nolint:nilerr // A candidate that cannot be made relative to the walk root is not a match.
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
	return globResult(pattern, searchRoot, resolvedPattern, matches)
}

func globSearchRoot(fsys sandbox.FileSystem, pathArg string) (string, error) {
	root, err := normalizePathWithFS(fsys, pathArg)
	if err != nil {
		return "", err
	}
	info, err := fsys.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", tool.NewError(tool.ErrorCodeNotFound, "Glob path does not exist: "+root)
		}
		return "", err
	}
	if !info.IsDir() {
		return "", tool.NewError(tool.ErrorCodeInvalidInput, "Glob path must be a directory: "+root)
	}
	return root, nil
}

func validateGlobPatternUnderSearchRoot(searchRoot string, resolvedPattern string) error {
	rel, err := filepath.Rel(searchRoot, resolvedPattern)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		toolErr := tool.NewError(tool.ErrorCodeInvalidInput, "Glob pattern must stay under path: "+searchRoot)
		toolErr.Hint = "Use path for the search directory and a pattern inside it, such as \"*.py\" or \"**/*.py\"."
		return toolErr
	}
	return nil
}

func globResult(pattern string, searchRoot string, resolvedPattern string, matches []string) (tool.Result, error) {
	truncated := len(matches) > globResultLimit
	visible := make([]string, len(matches))
	copy(visible, matches)
	if truncated {
		visible = visible[:globResultLimit]
	}
	payload := map[string]any{
		"matches":   visible,
		"truncated": truncated,
	}
	if len(visible) == 0 {
		payload["message"] = fmt.Sprintf("No files matched glob %q.", pattern)
	}
	meta := map[string]any{
		"pattern":          pattern,
		"resolved_pattern": resolvedPattern,
		"count":            len(visible),
		"truncated":        truncated,
		"path":             searchRoot,
	}
	if message, ok := payload["message"]; ok {
		meta["message"] = message
	}
	return toolutil.JSONResult(GlobToolName, payload, meta)
}

var _ tool.Tool = (*GlobTool)(nil)
