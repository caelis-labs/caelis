package filesystem

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/tool/internal/argparse"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

type fileMutationPlan struct {
	tool      string
	path      string
	created   bool
	before    string
	after     string
	hunk      string
	mode      os.FileMode
	replaced  int
	oldCount  int
	editCount int
}

type patchEdit struct {
	old              string
	new              string
	replaceAll       bool
	expected         int
	expectedProvided bool
}

type patchReplacement struct {
	start     int
	end       int
	new       string
	editIndex int
}

func planWriteMutation(fsys sandbox.FileSystem, args map[string]any) (fileMutationPlan, error) {
	pathArg, err := argparse.String(args, "path", true)
	if err != nil {
		return fileMutationPlan{}, err
	}
	rawContent, exists := args["content"]
	if !exists {
		return fileMutationPlan{}, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: missing required arg %q", "content"))
	}
	content, ok := rawContent.(string)
	if !ok {
		return fileMutationPlan{}, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: arg %q must be string", "content"))
	}
	ifRevision, err := argparse.String(args, "if_revision", false)
	if err != nil {
		return fileMutationPlan{}, err
	}

	target, err := normalizePathWithFS(fsys, pathArg)
	if err != nil {
		return fileMutationPlan{}, err
	}

	info, statErr := fsys.Stat(target)
	created := false
	mode := os.FileMode(0o644)
	before := ""
	switch {
	case statErr == nil:
		if info.IsDir() {
			return fileMutationPlan{}, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: target %q is directory", target))
		}
		mode = info.Mode()
		raw, err := fsys.ReadFile(target)
		if err != nil {
			return fileMutationPlan{}, err
		}
		before = string(raw)
	case errors.Is(statErr, os.ErrNotExist):
		if ifRevision != "" {
			return fileMutationPlan{}, staleRevisionError(target)
		}
		created = true
	default:
		return fileMutationPlan{}, statErr
	}
	if !revisionsMatchFile(ifRevision, before, info) {
		return fileMutationPlan{}, staleRevisionError(target)
	}

	return fileMutationPlan{
		tool:    WriteToolName,
		path:    target,
		created: created,
		before:  before,
		after:   content,
		mode:    mode,
	}, nil
}

func planPatchMutation(fsys sandbox.FileSystem, args map[string]any) (fileMutationPlan, error) {
	pathArg, err := argparse.String(args, "path", true)
	if err != nil {
		return fileMutationPlan{}, err
	}
	edits, err := parsePatchEdits(args)
	if err != nil {
		return fileMutationPlan{}, err
	}
	ifRevision, err := argparse.String(args, "if_revision", false)
	if err != nil {
		return fileMutationPlan{}, err
	}

	target, err := normalizePathWithFS(fsys, pathArg)
	if err != nil {
		return fileMutationPlan{}, err
	}
	fileInfo, statErr := fsys.Stat(target)
	fileExists := statErr == nil
	mode := os.FileMode(0o644)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fileMutationPlan{}, statErr
	}
	if fileExists {
		if fileInfo.IsDir() {
			return fileMutationPlan{}, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: target %q is directory", target))
		}
		mode = fileInfo.Mode()
	}

	if !fileExists {
		if ifRevision != "" {
			return fileMutationPlan{}, staleRevisionError(target)
		}
		err := tool.NewError(tool.ErrorCodeNotFound, fmt.Sprintf("tool: PATCH target %q does not exist; use WRITE to create files", target))
		err.Retryable = true
		return fileMutationPlan{}, err
	}

	contentRaw, err := fsys.ReadFile(target)
	if err != nil {
		return fileMutationPlan{}, err
	}
	content := string(contentRaw)
	if !revisionsMatchFile(ifRevision, content, fileInfo) {
		return fileMutationPlan{}, staleRevisionError(target)
	}

	replacements, err := collectPatchReplacements(target, content, edits)
	if err != nil {
		return fileMutationPlan{}, err
	}
	after := applyPatchReplacements(content, replacements)
	hunk := ""
	if len(replacements) == 1 {
		only := replacements[0]
		lineStart := 1 + strings.Count(content[:only.start], "\n")
		hunk = buildPatchHunk(lineStart, patchLineCount(content[only.start:only.end]), patchLineCount(only.new))
	}
	return fileMutationPlan{
		tool:      PatchToolName,
		path:      target,
		before:    content,
		after:     after,
		hunk:      hunk,
		mode:      mode,
		replaced:  len(replacements),
		oldCount:  len(replacements),
		editCount: len(edits),
	}, nil
}

func parsePatchEdits(args map[string]any) ([]patchEdit, error) {
	raw, ok := args["edits"]
	if !ok || raw == nil {
		return nil, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: missing required arg %q", "edits"))
	}
	rawItems, ok := raw.([]any)
	if !ok {
		return nil, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: arg %q must be an array", "edits"))
	}
	if len(rawItems) == 0 {
		return nil, tool.NewError(tool.ErrorCodeInvalidInput, "tool: arg \"edits\" must include at least one edit")
	}
	edits := make([]patchEdit, 0, len(rawItems))
	for idx, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: arg \"edits[%d]\" must be an object", idx))
		}
		oldValue, err := requiredPatchEditString(item, idx, "old")
		if err != nil {
			return nil, err
		}
		if oldValue == "" {
			return nil, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: arg \"edits[%d].old\" must be non-empty", idx))
		}
		newValue, err := requiredPatchEditString(item, idx, "new")
		if err != nil {
			return nil, err
		}
		replaceAll, err := argparse.Bool(item, "replace_all", false)
		if err != nil {
			return nil, err
		}
		expectedProvided := false
		expected := 1
		if rawExpected, ok := item["expected_replacements"]; ok && rawExpected != nil {
			expectedProvided = true
			expected, err = argparse.Int(item, "expected_replacements", 1)
			if err != nil {
				return nil, err
			}
			if expected < 1 {
				return nil, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: arg \"edits[%d].expected_replacements\" must be >= 1", idx))
			}
		}
		if replaceAll && !expectedProvided {
			return nil, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: arg \"edits[%d].expected_replacements\" is required when replace_all is true", idx))
		}
		edits = append(edits, patchEdit{
			old:              oldValue,
			new:              newValue,
			replaceAll:       replaceAll,
			expected:         expected,
			expectedProvided: expectedProvided,
		})
	}
	return edits, nil
}

func requiredPatchEditString(args map[string]any, editIndex int, key string) (string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return "", tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: missing required arg \"edits[%d].%s\"", editIndex, key))
	}
	value, ok := raw.(string)
	if !ok {
		return "", tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: arg \"edits[%d].%s\" must be string", editIndex, key))
	}
	return value, nil
}

func collectPatchReplacements(path string, content string, edits []patchEdit) ([]patchReplacement, error) {
	replacements := make([]patchReplacement, 0, len(edits))
	for idx, edit := range edits {
		matches := patchMatchRanges(content, edit.old)
		count := len(matches)
		if count == 0 {
			err := tool.NewError(tool.ErrorCodeOldTextNotFound, fmt.Sprintf("tool: PATCH target %q edit %d did not contain an exact match for old", path, idx))
			err.Hint = "READ the file again and retry PATCH with the current text."
			err.Retryable = true
			return nil, err
		}
		if edit.expectedProvided && count != edit.expected {
			return nil, replacementCountError(path, idx, edit.expected, count)
		}
		if !edit.replaceAll && count != 1 {
			err := tool.NewError(tool.ErrorCodeTooManyMatches, fmt.Sprintf("tool: PATCH target %q edit %d requires exact single match, found %d", path, idx, count))
			err.Hint = "Use a more specific old text or set replace_all with expected_replacements."
			err.Retryable = true
			return nil, err
		}
		for _, match := range matches {
			newValue := edit.new
			if match.normalizedLineEndings {
				newValue = normalizePatchReplacementLineEndings(edit.new, content[match.start:match.end])
			}
			replacements = append(replacements, patchReplacement{
				start:     match.start,
				end:       match.end,
				new:       newValue,
				editIndex: idx,
			})
			if !edit.replaceAll {
				break
			}
		}
	}
	if err := validatePatchReplacementRanges(path, replacements); err != nil {
		return nil, err
	}
	return replacements, nil
}

type patchMatchRange struct {
	start                 int
	end                   int
	normalizedLineEndings bool
}

func patchMatchRanges(content string, oldValue string) []patchMatchRange {
	if matches := exactPatchMatchRanges(content, oldValue); len(matches) > 0 {
		return matches
	}
	normalizedContent, offsets := normalizePatchLineEndingsWithOffsets(content)
	normalizedOld := normalizePatchLineEndings(oldValue)
	if normalizedContent == content && normalizedOld == oldValue {
		return nil
	}
	normalizedMatches := exactPatchMatchRanges(normalizedContent, normalizedOld)
	if len(normalizedMatches) == 0 {
		return nil
	}
	ranges := make([]patchMatchRange, 0, len(normalizedMatches))
	for _, match := range normalizedMatches {
		if match.start < 0 || match.start >= len(offsets) || match.end < 0 || match.end >= len(offsets) {
			continue
		}
		start := offsets[match.start]
		end := offsets[match.end]
		ranges = append(ranges, patchMatchRange{
			start:                 start,
			end:                   end,
			normalizedLineEndings: true,
		})
	}
	return ranges
}

func exactPatchMatchRanges(content string, oldValue string) []patchMatchRange {
	var ranges []patchMatchRange
	offset := 0
	for offset <= len(content) {
		index := strings.Index(content[offset:], oldValue)
		if index < 0 {
			break
		}
		start := offset + index
		end := start + len(oldValue)
		if patchRangeSplitsCRLF(content, start, end) {
			offset = start + 1
			continue
		}
		ranges = append(ranges, patchMatchRange{start: start, end: end})
		offset = end
	}
	return ranges
}

func patchRangeSplitsCRLF(content string, start int, end int) bool {
	if start > 0 && start < len(content) && content[start-1] == '\r' && content[start] == '\n' {
		return true
	}
	if end > 0 && end < len(content) && content[end-1] == '\r' && content[end] == '\n' {
		return true
	}
	return false
}

func normalizePatchLineEndingsWithOffsets(text string) (string, []int) {
	var out strings.Builder
	offsets := []int{0}
	for i := 0; i < len(text); {
		switch text[i] {
		case '\r':
			if i+1 < len(text) && text[i+1] == '\n' {
				i += 2
			} else {
				i++
			}
			out.WriteByte('\n')
			offsets = append(offsets, i)
		default:
			out.WriteByte(text[i])
			i++
			offsets = append(offsets, i)
		}
	}
	return out.String(), offsets
}

func normalizePatchLineEndings(text string) string {
	normalized, _ := normalizePatchLineEndingsWithOffsets(text)
	return normalized
}

func normalizePatchReplacementLineEndings(value string, matchedContent string) string {
	eol := dominantPatchLineEnding(matchedContent)
	if eol == "" || eol == "\n" {
		return normalizePatchLineEndings(value)
	}
	return strings.ReplaceAll(normalizePatchLineEndings(value), "\n", eol)
}

func dominantPatchLineEnding(text string) string {
	crlf := 0
	lf := 0
	cr := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\r':
			if i+1 < len(text) && text[i+1] == '\n' {
				crlf++
				i++
			} else {
				cr++
			}
		case '\n':
			lf++
		}
	}
	if crlf == 0 && lf == 0 && cr == 0 {
		return ""
	}
	if crlf >= lf && crlf >= cr {
		return "\r\n"
	}
	if lf >= cr {
		return "\n"
	}
	return "\r"
}

func validatePatchReplacementRanges(path string, replacements []patchReplacement) error {
	sort.Slice(replacements, func(i, j int) bool {
		if replacements[i].start == replacements[j].start {
			return replacements[i].end < replacements[j].end
		}
		return replacements[i].start < replacements[j].start
	})
	for idx := 1; idx < len(replacements); idx++ {
		prev := replacements[idx-1]
		next := replacements[idx]
		if next.start < prev.end {
			err := tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: PATCH target %q has overlapping edits %d and %d", path, prev.editIndex, next.editIndex))
			err.Hint = "Use non-overlapping old text in each edit."
			err.Retryable = true
			return err
		}
	}
	return nil
}

func applyPatchReplacements(content string, replacements []patchReplacement) string {
	sort.Slice(replacements, func(i, j int) bool {
		if replacements[i].start == replacements[j].start {
			return replacements[i].end > replacements[j].end
		}
		return replacements[i].start > replacements[j].start
	})
	out := content
	for _, replacement := range replacements {
		out = out[:replacement.start] + replacement.new + out[replacement.end:]
	}
	return out
}

func replacementCountError(path string, editIndex int, expected int, actual int) error {
	err := tool.NewError(tool.ErrorCodeUnexpectedMatchCount, fmt.Sprintf("tool: PATCH target %q edit %d expected %d replacement(s), found %d", path, editIndex, expected, actual))
	err.Hint = "READ the file again and retry PATCH with the current expected_replacements value."
	err.Retryable = true
	return err
}

func patchLineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}
