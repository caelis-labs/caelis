package filesystem

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/argparse"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
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
		Description: "Search file contents for text or regex matches in one file or directory.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":           map[string]any{"type": "string", "minLength": 1, "description": "File or directory to scan."},
				"pattern":        map[string]any{"type": "string", "minLength": 1, "description": "Text or regex pattern to find inside files."},
				"limit":          map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Max results."},
				"case_sensitive": map[string]any{"type": "boolean", "description": "Case-sensitive match."},
				"regex":          map[string]any{"type": "boolean", "description": "Treat pattern as regex."},
				"include":        stringOrStringArraySchema("File glob or globs to include, relative to path."),
				"exclude":        stringOrStringArraySchema("File glob or globs to exclude, relative to path."),
			},
			"required":             []string{"path", "pattern"},
			"additionalProperties": false,
		},
		Metadata: toolutil.AnnotationMetadata(true, false, true, false),
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
	if err := tool.RejectUnknownArgs(args, "path", "pattern", "limit", "case_sensitive", "regex", "include", "exclude"); err != nil {
		return tool.Result{}, err
	}
	pathArg, err := argparse.String(args, "path", true)
	if err != nil {
		return tool.Result{}, err
	}
	pattern, err := argparse.String(args, "pattern", true)
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
	terms, err := parseSearchTerms(pattern, caseSensitive, regexMode)
	if err != nil {
		return tool.Result{}, err
	}
	if len(terms) == 0 {
		return tool.Result{}, tool.NewError(tool.ErrorCodeInvalidInput, "SEARCH pattern must include at least one non-empty keyword")
	}
	exclude, err := parseStringSliceArg(args, "exclude")
	if err != nil {
		return tool.Result{}, err
	}
	include, err := parseStringSliceArg(args, "include")
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

	hits := make([]map[string]any, 0, limit)
	filesWithHits := map[string]struct{}{}
	truncated := false
	appendMatch := func(path string, lineNum, column int, match string, text string) bool {
		filesWithHits[path] = struct{}{}
		result := map[string]any{
			"path":   path,
			"line":   lineNum,
			"column": column,
			"match":  match,
			"text":   text,
		}
		hits = append(hits, result)
		if len(hits) >= limit {
			truncated = true
		}
		return len(hits) >= limit
	}

	root := target
	if !info.IsDir() {
		root = filepath.Dir(target)
	}
	excludeRules := append(workspaceExcludeRules(fsys, root), excludeRulesFromPatterns(exclude)...)
	includeRules := pathRulesFromPatterns(include)
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
			if !shouldIncludeFilePath(root, path, includeRules) {
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
		if shouldExcludePath(root, target, false, excludeRules) || !shouldIncludeFilePath(root, target, includeRules) {
			return toolutil.JSONResult(SearchToolName, map[string]any{
				"path":       target,
				"pattern":    pattern,
				"regex":      regexMode,
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
		"path":       target,
		"pattern":    pattern,
		"regex":      regexMode,
		"count":      len(hits),
		"file_count": len(filesWithHits),
		"truncated":  truncated,
		"hits":       hits,
	}, map[string]any{
		"path":    target,
		"pattern": pattern,
		"regex":   regexMode,
		"terms":   searchTermRawValues(terms),
		"hits":    hits,
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

func parseSearchTerms(pattern string, caseSensitive bool, regexMode bool) ([]searchTerm, error) {
	pattern = strings.TrimSpace(pattern)
	if regexMode {
		if pattern == "" {
			return nil, nil
		}
		regexPattern := pattern
		if !caseSensitive {
			regexPattern = "(?i:" + pattern + ")"
		}
		compiled, err := regexp.Compile(regexPattern)
		if err != nil {
			return nil, tool.WrapError(tool.ErrorCodeInvalidInput, err, "SEARCH pattern is not a valid regular expression")
		}
		return []searchTerm{{Raw: pattern, Regex: compiled}}, nil
	}
	parts := strings.Split(pattern, "|")
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
