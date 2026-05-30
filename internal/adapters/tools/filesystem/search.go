package filesystem

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

const (
	defaultSearchLimit = 50
	maxSearchLimit     = 200
)

var errSearchLimitReached = errors.New("search limit reached")

type SearchFilesTool struct {
	Sandbox sandbox.Runtime
}

type searchFilesInput struct {
	Path          string   `json:"path"`
	Query         string   `json:"query"`
	Limit         int      `json:"limit,omitempty"`
	CaseSensitive bool     `json:"case_sensitive,omitempty"`
	Regex         bool     `json:"regex,omitempty"`
	Exclude       []string `json:"exclude,omitempty"`
}

type searchTerm struct {
	Raw   string
	Match string
	Regex *regexp.Regexp
}

func NewSearchFilesTool(runtime sandbox.Runtime) (*SearchFilesTool, error) {
	if runtime == nil {
		return nil, fmt.Errorf("tools/filesystem: sandbox runtime is required")
	}
	return &SearchFilesTool{Sandbox: runtime}, nil
}

func (t *SearchFilesTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        SearchFilesToolName,
		Description: "Search text in files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":           map[string]any{"type": "string", "description": "File or directory path."},
				"query":          map[string]any{"type": "string", "description": "Text or regex query. Use | for alternatives unless regex=true."},
				"limit":          map[string]any{"type": "integer", "description": "Maximum number of hits."},
				"case_sensitive": map[string]any{"type": "boolean", "description": "Use case-sensitive matching."},
				"regex":          map[string]any{"type": "boolean", "description": "Treat query as a regular expression."},
				"exclude": map[string]any{
					"type":        "array",
					"description": "Relative exclude glob patterns.",
					"items":       map[string]any{"type": "string"},
				},
			},
			"required":             []any{"path", "query"},
			"additionalProperties": false,
		},
	}
}

func (t *SearchFilesTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := checkContext(ctx); err != nil {
		return tool.Result{}, err
	}
	var input searchFilesInput
	if err := decodeInput(call, &input); err != nil {
		return tool.Result{}, err
	}
	limit := clampLimit(input.Limit, defaultSearchLimit, maxSearchLimit)
	terms, err := parseSearchTerms(input.Query, input.CaseSensitive, input.Regex)
	if err != nil {
		return tool.Result{}, err
	}
	if len(terms) == 0 {
		return tool.Result{}, fmt.Errorf("tools/filesystem: query must include at least one non-empty term")
	}
	fsys, err := runtimeFileSystem(t.Sandbox, call.Meta)
	if err != nil {
		return tool.Result{}, err
	}
	target, err := normalizePath(fsys, input.Path)
	if err != nil {
		return tool.Result{}, err
	}
	info, err := fsys.Stat(target)
	if err != nil {
		return tool.Result{}, err
	}
	root := target
	if !info.IsDir() {
		root = filepath.Dir(target)
	}
	rules := append(workspaceExcludeRules(fsys, root), parseExclude(input.Exclude)...)
	hits := make([]map[string]any, 0, limit)
	files := map[string]struct{}{}
	truncated := false
	appendMatch := func(path string, line int, column int, match string, text string) bool {
		files[path] = struct{}{}
		hits = append(hits, map[string]any{
			"path":   path,
			"line":   line,
			"column": column,
			"match":  match,
			"text":   text,
		})
		truncated = len(hits) >= limit
		return truncated
	}
	if info.IsDir() {
		walkErr := walkDir(fsys, target, func(path string, d fs.DirEntry, walkErr error) error {
			if err := checkContext(ctx); err != nil {
				return err
			}
			if walkErr != nil || d == nil {
				return nil
			}
			if path != target && shouldExclude(root, path, d.IsDir(), rules) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if _, stop := searchOneFile(fsys, path, terms, input.CaseSensitive, appendMatch); stop {
				return errSearchLimitReached
			}
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, errSearchLimitReached) {
			return tool.Result{}, walkErr
		}
	} else if !shouldExclude(root, target, false, rules) {
		_, truncated = searchOneFile(fsys, target, terms, input.CaseSensitive, appendMatch)
	}
	return jsonResult(call, SearchFilesToolName, map[string]any{
		"path":       target,
		"query":      strings.TrimSpace(input.Query),
		"count":      len(hits),
		"file_count": len(files),
		"truncated":  truncated,
		"hits":       hits,
	}, map[string]any{
		"path":  target,
		"query": strings.TrimSpace(input.Query),
		"regex": input.Regex,
		"terms": searchTermRawValues(terms),
	})
}

func searchOneFile(fsys sandbox.FileSystem, target string, terms []searchTerm, caseSensitive bool, appendMatch func(string, int, int, string, string) bool) (bool, bool) {
	file, err := fsys.Open(target)
	if err != nil {
		return false, false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	matched := false
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		candidate := text
		if !caseSensitive && !searchTermsUseRegex(terms) {
			candidate = strings.ToLower(candidate)
		}
		if match, column, ok := firstSearchMatch(candidate, terms); ok {
			matched = true
			if appendMatch(target, line, column, match, text) {
				return true, true
			}
		}
	}
	return matched, false
}

func parseSearchTerms(query string, caseSensitive bool, regexMode bool) ([]searchTerm, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if regexMode {
		pattern := query
		if !caseSensitive {
			pattern = "(?i:" + query + ")"
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("tools/filesystem: invalid search regex: %w", err)
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

var _ tool.Tool = (*SearchFilesTool)(nil)
