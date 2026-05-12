package filesystem

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/tool/internal/argparse"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

type fileMutationPlan struct {
	tool     string
	path     string
	created  bool
	before   string
	after    string
	hunk     string
	mode     os.FileMode
	replaced int
	oldCount int
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
		if strings.TrimSpace(ifRevision) != "" {
			return fileMutationPlan{}, staleRevisionError(target)
		}
		created = true
	default:
		return fileMutationPlan{}, statErr
	}
	if !revisionsMatch(ifRevision, textRevision(before)) {
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
	oldValue, err := argparse.String(args, "old", false)
	if err != nil {
		return fileMutationPlan{}, err
	}
	rawNew, exists := args["new"]
	if !exists {
		return fileMutationPlan{}, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: missing required arg %q", "new"))
	}
	newValue, ok := rawNew.(string)
	if !ok {
		return fileMutationPlan{}, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: arg %q must be string", "new"))
	}
	replaceAll, _ := argparse.Bool(args, "replace_all", false)
	expectedReplacements, err := argparse.Int(args, "expected_replacements", 0)
	if err != nil {
		return fileMutationPlan{}, err
	}
	if expectedReplacements < 0 {
		return fileMutationPlan{}, tool.NewError(tool.ErrorCodeInvalidInput, "tool: arg \"expected_replacements\" must be >= 0")
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
		if strings.TrimSpace(ifRevision) != "" {
			return fileMutationPlan{}, staleRevisionError(target)
		}
		if oldValue != "" {
			err := tool.NewError(tool.ErrorCodeNotFound, fmt.Sprintf("tool: PATCH target %q does not exist; set %q to empty string to create file", target, "old"))
			err.Retryable = true
			return fileMutationPlan{}, err
		}
		if expectedReplacements > 0 && expectedReplacements != 1 {
			return fileMutationPlan{}, replacementCountError(target, expectedReplacements, 1)
		}
		return fileMutationPlan{
			tool:     PatchToolName,
			path:     target,
			created:  true,
			before:   "",
			after:    newValue,
			hunk:     buildPatchHunk(1, 0, patchLineCount(newValue)),
			mode:     mode,
			replaced: 1,
			oldCount: 1,
		}, nil
	}

	contentRaw, err := fsys.ReadFile(target)
	if err != nil {
		return fileMutationPlan{}, err
	}
	content := string(contentRaw)
	if !revisionsMatch(ifRevision, textRevision(content)) {
		return fileMutationPlan{}, staleRevisionError(target)
	}
	if oldValue == "" {
		if content != "" {
			return fileMutationPlan{}, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: PATCH arg %q can be empty only when target file is empty", "old"))
		}
		if expectedReplacements > 0 && expectedReplacements != 1 {
			return fileMutationPlan{}, replacementCountError(target, expectedReplacements, 1)
		}
		return fileMutationPlan{
			tool:     PatchToolName,
			path:     target,
			before:   "",
			after:    newValue,
			hunk:     buildPatchHunk(1, 0, patchLineCount(newValue)),
			mode:     mode,
			replaced: 1,
			oldCount: 1,
		}, nil
	}

	count := strings.Count(content, oldValue)
	if count == 0 {
		err := tool.NewError(tool.ErrorCodeOldTextNotFound, fmt.Sprintf("tool: PATCH target %q did not contain an exact match for \"old\"", target))
		err.Hint = "READ the file again and retry PATCH with the current text."
		err.Retryable = true
		return fileMutationPlan{}, err
	}
	if expectedReplacements > 0 && count != expectedReplacements {
		return fileMutationPlan{}, replacementCountError(target, expectedReplacements, count)
	}
	if !replaceAll && count != 1 {
		err := tool.NewError(tool.ErrorCodeTooManyMatches, fmt.Sprintf("tool: PATCH requires exact single match, found %d; set replace_all=true with expected_replacements=%d to replace all", count, count))
		err.Hint = "Use a more specific old text or set replace_all with expected_replacements."
		err.Retryable = true
		return fileMutationPlan{}, err
	}
	if replaceAll {
		return fileMutationPlan{
			tool:     PatchToolName,
			path:     target,
			before:   content,
			after:    strings.ReplaceAll(content, oldValue, newValue),
			mode:     mode,
			replaced: count,
			oldCount: count,
		}, nil
	}

	index := strings.Index(content, oldValue)
	lineStart := 1
	if index >= 0 {
		lineStart = 1 + strings.Count(content[:index], "\n")
	}
	oldLines := patchLineCount(oldValue)
	newLines := patchLineCount(newValue)
	return fileMutationPlan{
		tool:     PatchToolName,
		path:     target,
		before:   content,
		after:    strings.Replace(content, oldValue, newValue, 1),
		hunk:     buildPatchHunk(lineStart, oldLines, newLines),
		mode:     mode,
		replaced: 1,
		oldCount: count,
	}, nil
}

func replacementCountError(path string, expected int, actual int) error {
	code := tool.ErrorCodeTooManyMatches
	if actual < expected {
		code = tool.ErrorCodeOldTextNotFound
	}
	err := tool.NewError(code, fmt.Sprintf("tool: PATCH target %q expected %d replacement(s), found %d", path, expected, actual))
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
