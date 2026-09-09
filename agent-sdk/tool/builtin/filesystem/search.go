package filesystem

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/argparse"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
)

const SearchToolName = "Grep"

const (
	binaryDetectionBytes = 8 * 1024
	searchResultLimit    = 100
	maxSearchLineRunes   = 2000
	maxSearchLineBytes   = 8 * 1024 * 1024
)

var errSearchLimitReached = errors.New("search: result limit reached")

type searchStats struct {
	filesSelected      int
	filesScanned       int
	binaryFilesSkipped int
}

type searchFileResult struct {
	stop    bool
	scanned bool
	binary  bool
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
		Description: "Search file contents with a regular expression. Matching is case-sensitive by default; use (?i) in pattern for case-insensitive search. Results are capped at 100 lines; a line over 8MiB fails the search rather than reporting no matches. Use Read for surrounding context or RunCommand with rg for advanced search.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "minLength": 1, "description": "Regular expression matched against file contents."},
				"path":    map[string]any{"type": "string", "minLength": 1, "description": "File or directory to scan. Defaults to cwd."},
				"include": map[string]any{"type": "string", "minLength": 1, "description": "Optional file glob relative to path, such as \"*.go\" or \"*.{go,sql}\"."},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
		Metadata:              toolutil.AnnotationMetadata(true, false, true, false),
		ExecutionRequirements: fileSystemExecutionRequirements(),
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
	if err := tool.RejectUnknownArgs(args, "pattern", "path", "include"); err != nil {
		return tool.Result{}, err
	}
	pattern, err := argparse.String(args, "pattern", true)
	if err != nil {
		return tool.Result{}, err
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		toolErr := tool.WrapError(tool.ErrorCodeInvalidInput, err, "Grep pattern is not a valid regular expression")
		toolErr.Hint = "Escape literal regular-expression characters, or use RunCommand with rg for advanced search."
		return tool.Result{}, toolErr
	}
	pathArg, err := argparse.String(args, "path", false)
	if err != nil {
		return tool.Result{}, err
	}
	if pathArg == "" {
		pathArg = "."
	}
	include, err := argparse.String(args, "include", false)
	if err != nil {
		return tool.Result{}, err
	}
	if include != "" {
		if err := validatePathGlobPattern(include, "Grep include"); err != nil {
			return tool.Result{}, err
		}
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

	hits := make([]map[string]any, 0, searchResultLimit+1)
	stats := searchStats{}
	appendMatch := func(path string, lineNum int, text string) bool {
		hits = append(hits, map[string]any{
			"path": path,
			"line": lineNum,
			"text": text,
		})
		return len(hits) >= searchResultLimit+1
	}

	root := target
	if !info.IsDir() {
		root = filepath.Dir(target)
	}
	excludeRules := workspaceExcludeRules(fsys, root)
	var includeRules []pathMatchRule
	if include != "" {
		includeRules = pathRulesFromPatterns([]string{include})
	}
	if info.IsDir() {
		walkErr := walkDir(fsys, target, func(path string, d fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				//nolint:nilerr // Search is best-effort across unreadable paths and returns accessible matches.
				return nil
			}
			if path != target && shouldExcludePath(root, path, d != nil && d.IsDir(), excludeRules) {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d == nil || d.IsDir() || !shouldIncludeFilePath(root, path, includeRules) {
				return nil
			}
			fileResult, err := searchSelectedFile(ctx, fsys, path, compiled, &hits, &stats, appendMatch)
			if err != nil {
				if isSkippableSearchError(err) {
					return nil
				}
				return err
			}
			if fileResult.stop {
				return errSearchLimitReached
			}
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, errSearchLimitReached) {
			return tool.Result{}, walkErr
		}
	} else {
		if shouldExcludePath(root, target, false, excludeRules) || !shouldIncludeFilePath(root, target, includeRules) {
			return newSearchResult(target, pattern, include, hits, stats)
		}
		if _, err := searchSelectedFile(ctx, fsys, target, compiled, &hits, &stats, appendMatch); err != nil {
			return tool.Result{}, err
		}
	}

	return newSearchResult(target, pattern, include, hits, stats)
}

func (s *searchStats) observe(result searchFileResult) {
	if result.scanned {
		s.filesScanned++
	}
	if result.binary {
		s.binaryFilesSkipped++
	}
}

func searchSelectedFile(
	ctx context.Context,
	fsys sandbox.FileSystem,
	path string,
	pattern *regexp.Regexp,
	hits *[]map[string]any,
	stats *searchStats,
	appendMatch func(string, int, string) bool,
) (searchFileResult, error) {
	stats.filesSelected++
	hitStart := len(*hits)
	result, err := searchInFile(ctx, fsys, path, pattern, appendMatch)
	if result.binary {
		*hits = (*hits)[:hitStart]
	}
	stats.observe(result)
	return result, err
}

func isSkippableSearchError(err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, errNotRegularFile), errors.Is(err, fs.ErrInvalid):
		return true
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrPermission):
		return true
	default:
		return false
	}
}

func newSearchResult(path string, pattern string, include string, hits []map[string]any, stats searchStats) (tool.Result, error) {
	truncated := len(hits) > searchResultLimit
	visible := make([]map[string]any, len(hits))
	copy(visible, hits)
	if truncated {
		visible = visible[:searchResultLimit]
	}
	filesWithHits := map[string]struct{}{}
	for _, hit := range visible {
		if hitPath, ok := hit["path"].(string); ok {
			filesWithHits[hitPath] = struct{}{}
		}
	}

	payload := map[string]any{
		"hits":      visible,
		"truncated": truncated,
	}
	if message := emptySearchMessage(len(visible), include, stats); message != "" {
		payload["message"] = message
	}
	meta := map[string]any{
		"path":                 path,
		"pattern":              pattern,
		"count":                len(visible),
		"file_count":           len(filesWithHits),
		"truncated":            truncated,
		"files_selected":       stats.filesSelected,
		"files_scanned":        stats.filesScanned,
		"binary_files_skipped": stats.binaryFilesSkipped,
	}
	if include != "" {
		meta["include"] = include
	}
	if message, ok := payload["message"]; ok {
		meta["message"] = message
	}
	return toolutil.JSONResult(SearchToolName, payload, meta)
}

func emptySearchMessage(hitCount int, include string, stats searchStats) string {
	if hitCount > 0 {
		return ""
	}
	switch {
	case include != "" && stats.filesSelected == 0:
		if suggestion := extensionGlobSuggestion(include); suggestion != "" {
			return fmt.Sprintf("No files matched include glob %q. It is treated literally; use %q to match file extensions.", include, suggestion)
		}
		return fmt.Sprintf("No files matched include glob %q.", include)
	case stats.filesSelected == 0:
		return "No searchable files found."
	case stats.filesScanned == 0 && stats.binaryFilesSkipped > 0:
		return "No searchable text files found; binary files are skipped."
	case stats.filesScanned == 0:
		return "No searchable files could be read."
	default:
		return "No content matches found in the selected text files."
	}
}

func extensionGlobSuggestion(include string) string {
	switch {
	case strings.HasPrefix(include, ".{") && strings.HasSuffix(include, "}"):
		return "*" + include
	case strings.HasPrefix(include, "{") && strings.HasSuffix(include, "}"):
		return "*." + include
	case strings.HasPrefix(include, ".") && !hasPathGlobMeta(include):
		return "*" + include
	default:
		return ""
	}
}

func searchInFile(ctx context.Context, fsys sandbox.FileSystem, path string, pattern *regexp.Regexp, appendMatch func(string, int, string) bool) (searchFileResult, error) {
	if err := ctx.Err(); err != nil {
		return searchFileResult{}, err
	}
	file, err := openRegularFile(fsys, path)
	if err != nil {
		return searchFileResult{}, err
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, binaryDetectionBytes)
	sample, err := reader.Peek(binaryDetectionBytes)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return searchFileResult{}, err
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return searchFileResult{binary: true}, nil
	}

	result := searchFileResult{scanned: true}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSearchLineBytes)
	lineNum := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return searchFileResult{}, err
		}
		lineNum++
		text := scanner.Text()
		if strings.IndexByte(text, 0) >= 0 {
			return searchFileResult{binary: true}, nil
		}
		match := pattern.FindStringIndex(text)
		if match == nil {
			continue
		}
		if appendMatch(path, lineNum, searchLineExcerpt(text, match[0], match[1])) {
			result.stop = true
			return result, nil
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return searchFileResult{}, searchLineTooLongError(path)
		}
		return searchFileResult{}, err
	}
	return result, nil
}

func searchLineTooLongError(path string) error {
	return tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("Grep path %q has a line that exceeds the 8MiB limit", path))
}

func searchLineExcerpt(text string, matchStart, matchEnd int) string {
	runes := []rune(text)
	if len(runes) <= maxSearchLineRunes {
		return string(runes)
	}
	matchStart = max(0, min(matchStart, len(text)))
	matchEnd = max(matchStart, min(matchEnd, len(text)))
	startRune, endRune := searchMatchRuneRange(text, matchStart, matchEnd, len(runes))

	matchRunes := endRune - startRune
	start := startRune
	if matchRunes < maxSearchLineRunes {
		start -= (maxSearchLineRunes - matchRunes) / 2
	}
	if start < 0 {
		start = 0
	}
	end := start + maxSearchLineRunes
	if end > len(runes) {
		end = len(runes)
		start = end - maxSearchLineRunes
	}
	excerpt := string(runes[start:end])
	if start > 0 {
		excerpt = "… [line truncated] " + excerpt
	}
	if end < len(runes) {
		excerpt += "… [line truncated]"
	}
	return excerpt
}

func searchMatchRuneRange(text string, matchStart, matchEnd int, runeCount int) (int, int) {
	startRune := runeCount
	endRune := runeCount
	runeIndex := 0
	for byteIndex := range text {
		if startRune == runeCount && byteIndex >= matchStart {
			startRune = runeIndex
		}
		if byteIndex >= matchEnd {
			endRune = runeIndex
			break
		}
		runeIndex++
	}
	return startRune, endRune
}

var _ tool.Tool = (*SearchTool)(nil)
