package filesystem

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
	"github.com/OnslaughtSnail/caelis/sdk/tool/builtin/internal/toolutil"
	"github.com/OnslaughtSnail/caelis/sdk/tool/internal/argparse"
)

const SearchToolName = "SEARCH"

var errSearchLimitReached = errors.New("search: limit reached")

type SearchTool struct {
	runtime sdksandbox.Runtime
}

func NewSearch(runtime sdksandbox.Runtime) (*SearchTool, error) {
	resolvedRuntime, err := runtimeOrDefault(runtime)
	if err != nil {
		return nil, err
	}
	return &SearchTool{runtime: resolvedRuntime}, nil
}

func (t *SearchTool) Definition() sdktool.Definition {
	return sdktool.Definition{
		Name:        SearchToolName,
		Description: "Search text in one file or a directory tree.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":           map[string]any{"type": "string", "description": "Target file or directory path."},
				"query":          map[string]any{"type": "string", "description": "Search text."},
				"limit":          map[string]any{"type": "integer", "description": "Optional max results."},
				"case_sensitive": map[string]any{"type": "boolean", "description": "Set true for case-sensitive search."},
				"exclude": map[string]any{
					"type":        "array",
					"description": "Optional relative path patterns to exclude after filtering.",
					"items":       map[string]any{"type": "string"},
				},
				"respect_gitignore": map[string]any{"type": "boolean", "description": "When true, filter paths ignored by .gitignore at the search root."},
			},
			"required": []string{"path", "query"},
		},
	}
}

func (t *SearchTool) Call(ctx context.Context, call sdktool.Call) (sdktool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return sdktool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return sdktool.Result{}, err
	}
	pathArg, err := argparse.String(args, "path", true)
	if err != nil {
		return sdktool.Result{}, err
	}
	query, err := argparse.String(args, "query", true)
	if err != nil {
		return sdktool.Result{}, err
	}
	limit, err := argparse.Int(args, "limit", 50)
	if err != nil {
		return sdktool.Result{}, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	caseSensitive, err := argparse.Bool(args, "case_sensitive", false)
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

	target, err := normalizePathWithFS(fsys, pathArg)
	if err != nil {
		return sdktool.Result{}, err
	}
	info, err := fsys.Stat(target)
	if err != nil {
		return sdktool.Result{}, err
	}

	queryToMatch := query
	if !caseSensitive {
		queryToMatch = strings.ToLower(query)
	}
	results := make([]map[string]any, 0, limit)
	filesWithHits := map[string]struct{}{}
	truncated := false
	appendMatch := func(path string, lineNum, column int, text string) bool {
		filesWithHits[path] = struct{}{}
		results = append(results, map[string]any{
			"path":   path,
			"line":   lineNum,
			"column": column,
			"text":   text,
		})
		if len(results) >= limit {
			truncated = true
		}
		return len(results) >= limit
	}

	root := target
	if !info.IsDir() {
		root = filepath.Dir(target)
	}
	excludeRules := excludeRulesFromPatterns(exclude)
	if respectGitignore {
		excludeRules = append(gitignoreExcludePatterns(fsys, root), excludeRules...)
	}
	if info.IsDir() {
		walkErr := walkDir(fsys, target, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if path != target && shouldExcludePath(root, path, d != nil && d.IsDir(), excludeRules) {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d == nil || d.IsDir() {
				return nil
			}
			_, stop := searchInFile(fsys, path, queryToMatch, caseSensitive, appendMatch)
			if stop {
				return errSearchLimitReached
			}
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, errSearchLimitReached) {
			return sdktool.Result{}, walkErr
		}
	} else {
		if shouldExcludePath(root, target, false, excludeRules) {
			return toolutil.JSONResult(SearchToolName, map[string]any{
				"path":       target,
				"query":      query,
				"count":      0,
				"file_count": 0,
				"truncated":  false,
				"hits":       []map[string]any{},
			})
		}
		if _, stop := searchInFile(fsys, target, queryToMatch, caseSensitive, appendMatch); stop {
			truncated = true
		}
	}

	return toolutil.JSONResult(SearchToolName, map[string]any{
		"path":       target,
		"query":      query,
		"count":      len(results),
		"file_count": len(filesWithHits),
		"truncated":  truncated,
		"hits":       results,
	})
}

func searchInFile(fsys sdksandbox.FileSystem, path, query string, caseSensitive bool, appendMatch func(string, int, int, string) bool) (bool, bool) {
	file, err := fsys.Open(path)
	if err != nil {
		return false, false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	matched := false
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		text := scanner.Text()
		candidate := text
		if !caseSensitive {
			candidate = strings.ToLower(candidate)
		}
		if strings.Contains(candidate, query) {
			matched = true
			column := strings.Index(candidate, query) + 1
			if column <= 0 {
				column = 1
			}
			if appendMatch(path, lineNum, column, text) {
				return true, true
			}
		}
	}
	return matched, false
}

var _ sdktool.Tool = (*SearchTool)(nil)
