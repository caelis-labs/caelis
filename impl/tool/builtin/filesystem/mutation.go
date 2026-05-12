package filesystem

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/tool/internal/argparse"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
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
		return fileMutationPlan{}, fmt.Errorf("tool: missing required arg %q", "content")
	}
	content, ok := rawContent.(string)
	if !ok {
		return fileMutationPlan{}, fmt.Errorf("tool: arg %q must be string", "content")
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
			return fileMutationPlan{}, fmt.Errorf("tool: target %q is directory", target)
		}
		mode = info.Mode()
		raw, err := fsys.ReadFile(target)
		if err != nil {
			return fileMutationPlan{}, err
		}
		before = string(raw)
	case errors.Is(statErr, os.ErrNotExist):
		created = true
	default:
		return fileMutationPlan{}, statErr
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
		return fileMutationPlan{}, fmt.Errorf("tool: missing required arg %q", "new")
	}
	newValue, ok := rawNew.(string)
	if !ok {
		return fileMutationPlan{}, fmt.Errorf("tool: arg %q must be string", "new")
	}
	replaceAll, _ := argparse.Bool(args, "replace_all", false)

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
			return fileMutationPlan{}, fmt.Errorf("tool: target %q is directory", target)
		}
		mode = fileInfo.Mode()
	}

	if !fileExists {
		if oldValue != "" {
			return fileMutationPlan{}, fmt.Errorf("tool: PATCH target %q does not exist; set %q to empty string to create file", target, "old")
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
	if oldValue == "" {
		if content != "" {
			return fileMutationPlan{}, fmt.Errorf("tool: PATCH arg %q can be empty only when target file is empty", "old")
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
		return fileMutationPlan{}, fmt.Errorf("tool: PATCH target %q did not contain an exact match for \"old\"", target)
	}
	if !replaceAll && count != 1 {
		return fileMutationPlan{}, fmt.Errorf("tool: PATCH requires exact single match, found %d; set replace_all=true to replace all", count)
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

func patchLineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}
