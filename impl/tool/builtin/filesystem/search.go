package filesystem

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/internal/toolutil"
	"github.com/OnslaughtSnail/caelis/impl/tool/internal/argparse"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const SearchToolName = "SEARCH"

var errSearchLimitReached = errors.New("search: limit reached")

type searchTerm struct {
	Raw   string
	Match string
	Regex *regexp.Regexp
}

type SearchTool struct {
	runtime sandbox.Runtime
}

func NewSearch(runtime sandbox.Runtime) (*SearchTool, error) {
	resolvedRuntime, err := runtimeOrDefault(runtime)
	if err != nil {
		return nil, err
	}
	return &SearchTool{runtime: resolvedRuntime}, nil
}

func (t *SearchTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        SearchToolName,
		Description: "Search text in one file or a directory tree.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":           map[string]any{"type": "string", "description": "Target file or directory path."},
				"query":          map[string]any{"type": "string", "description": "Search text. Separate alternatives with | for multi-keyword search."},
				"limit":          map[string]any{"type": "integer", "description": "Optional max results."},
				"case_sensitive": map[string]any{"type": "boolean", "description": "Set true for case-sensitive search."},
				"regex":          map[string]any{"type": "boolean", "description": "Treat query as one regular expression."},
				"exclude": map[string]any{
					"type":        "array",
					"description": "Optional relative path patterns to exclude after filtering.",
					"items":       map[string]any{"type": "string"},
				},
			},
			"required": []string{"path", "query"},
		},
	}
}

func (t *SearchTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return tool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return tool.Result{}, err
	}
	pathArg, err := argparse.String(args, "path", true)
	if err != nil {
		return tool.Result{}, err
	}
	query, err := argparse.String(args, "query", true)
	if err != nil {
		return tool.Result{}, err
	}
	limit, err := argparse.Int(args, "limit", 50)
	if err != nil {
		return tool.Result{}, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	caseSensitive, err := argparse.Bool(args, "case_sensitive", false)
	if err != nil {
		return tool.Result{}, err
	}
	regexMode, err := argparse.Bool(args, "regex", false)
	if err != nil {
		return tool.Result{}, err
	}
	terms, err := parseSearchTerms(query, caseSensitive, regexMode)
	if err != nil {
		return tool.Result{}, err
	}
	if len(terms) == 0 {
		return tool.Result{}, tool.NewError(tool.ErrorCodeInvalidInput, "SEARCH query must include at least one non-empty keyword")
	}
	exclude, err := parseStringSliceArg(args, "exclude")
	if err != nil {
		return tool.Result{}, err
	}
	fsys := fileSystemFromRuntime(t.runtime, call.Metadata)

	target, err := normalizePathWithFS(fsys, pathArg)
	if err != nil {
		return tool.Result{}, err
	}
	info, err := fsys.Stat(target)
	if err != nil {
		return tool.Result{}, err
	}

	results := make([]map[string]any, 0, limit)
	fullResults := make([]map[string]any, 0, limit)
	filesWithHits := map[string]struct{}{}
	truncated := false
	appendMatch := func(path string, lineNum, column int, match string, text string) bool {
		filesWithHits[path] = struct{}{}
		results = append(results, map[string]any{
			"path": path,
			"line": lineNum,
			"text": text,
		})
		fullResults = append(fullResults, map[string]any{
			"path":   path,
			"line":   lineNum,
			"column": column,
			"match":  match,
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
	excludeRules := append(gitignoreExcludePatterns(fsys, root), excludeRulesFromPatterns(exclude)...)
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
			_, stop := searchInFile(fsys, path, terms, caseSensitive, appendMatch)
			if stop {
				return errSearchLimitReached
			}
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, errSearchLimitReached) {
			return tool.Result{}, walkErr
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
		if _, stop := searchInFile(fsys, target, terms, caseSensitive, appendMatch); stop {
			truncated = true
		}
	}

	return toolutil.JSONResult(SearchToolName, map[string]any{
		"count":      len(results),
		"file_count": len(filesWithHits),
		"truncated":  truncated,
		"hits":       results,
	}, map[string]any{
		"path":  target,
		"query": query,
		"regex": regexMode,
		"terms": searchTermRawValues(terms),
		"hits":  fullResults,
	})
}

func searchInFile(fsys sandbox.FileSystem, path string, terms []searchTerm, caseSensitive bool, appendMatch func(string, int, int, string, string) bool) (bool, bool) {
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
		if !caseSensitive && !searchTermsUseRegex(terms) {
			candidate = strings.ToLower(candidate)
		}
		if match, column, ok := firstSearchMatch(candidate, terms); ok {
			matched = true
			if appendMatch(path, lineNum, column, match, text) {
				return true, true
			}
		}
	}
	return matched, false
}

func parseSearchTerms(query string, caseSensitive bool, regexMode bool) ([]searchTerm, error) {
	query = strings.TrimSpace(query)
	if regexMode {
		if query == "" {
			return nil, nil
		}
		pattern := query
		if !caseSensitive {
			pattern = "(?i:" + query + ")"
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, tool.WrapError(tool.ErrorCodeInvalidInput, err, "SEARCH query is not a valid regular expression")
		}
		return []searchTerm{{Raw: query, Regex: compiled}}, nil
	}
	parts := strings.Split(query, "|")
	out := make([]searchTerm, 0, len(parts))
	for _, part := range parts {
		raw := strings.TrimSpace(part)
		if raw == "" {
			continue
		}
		match := raw
		if !caseSensitive {
			match = strings.ToLower(raw)
		}
		out = append(out, searchTerm{Raw: raw, Match: match})
	}
	return out, nil
}

func firstSearchMatch(candidate string, terms []searchTerm) (string, int, bool) {
	bestColumn := 0
	bestMatch := ""
	for _, term := range terms {
		if term.Regex != nil {
			idx := term.Regex.FindStringIndex(candidate)
			if idx == nil {
				continue
			}
			column := idx[0] + 1
			match := candidate[idx[0]:idx[1]]
			if bestColumn == 0 || column < bestColumn {
				bestColumn = column
				bestMatch = match
			}
			continue
		}
		if term.Match == "" {
			continue
		}
		idx := strings.Index(candidate, term.Match)
		if idx < 0 {
			continue
		}
		column := idx + 1
		if bestColumn == 0 || column < bestColumn {
			bestColumn = column
			bestMatch = term.Raw
		}
	}
	return bestMatch, bestColumn, bestColumn > 0
}

func searchTermsUseRegex(terms []searchTerm) bool {
	for _, term := range terms {
		if term.Regex != nil {
			return true
		}
	}
	return false
}

func searchTermRawValues(terms []searchTerm) []string {
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		if term.Raw != "" {
			out = append(out, term.Raw)
		}
	}
	return out
}

var _ tool.Tool = (*SearchTool)(nil)
