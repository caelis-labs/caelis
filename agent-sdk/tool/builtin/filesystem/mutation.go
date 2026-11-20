package filesystem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/argparse"
)

// Retired Patch arguments remain accepted only so recoverable invocations
// issued under the former schema can still execute. They are intentionally
// absent from the definition and ignored. Remove these allowances once that
// recovery window falls below the supported upgrade floor.
const (
	retiredPatchIfRevisionArg      = "if_revision"
	retiredExpectedReplacementsArg = "expected_replacements"
)

type fileMutationPlan struct {
	path      string
	created   bool
	before    string
	after     string
	hunk      string
	mode      os.FileMode
	replaced  int
	editCount int
}

type patchEdit struct {
	old        string
	new        string
	replaceAll bool
}

type patchReplacement struct {
	start     int
	end       int
	new       string
	editIndex int
}

// File tools can be bound by multiple runtimes in the same process. Hold the
// target through planning and writing so a sibling sees the committed bytes.
type activeMutation struct {
	info os.FileInfo
	done chan struct{}
}

var fileMutations = struct {
	sync.Mutex
	active map[string]activeMutation
}{active: make(map[string]activeMutation)}

func lockMutation(ctx context.Context, fsys sandbox.FileSystem, path string) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, _ := fsys.Stat(path)
		fileMutations.Lock()
		active, busy := fileMutations.active[path]
		if !busy && info != nil {
			// Existing symlinks and hard links can name the same target.
			for _, other := range fileMutations.active {
				if other.info != nil && os.SameFile(info, other.info) {
					active, busy = other, true
					break
				}
			}
		}
		if !busy {
			fileMutations.active[path] = activeMutation{info: info, done: make(chan struct{})}
			fileMutations.Unlock()
			return func() {
				fileMutations.Lock()
				close(fileMutations.active[path].done)
				delete(fileMutations.active, path)
				fileMutations.Unlock()
			}, nil
		}
		fileMutations.Unlock()
		select {
		case <-active.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func applyFileMutation(ctx context.Context, fsys sandbox.FileSystem, args map[string]any, planMutation func(sandbox.FileSystem, string, map[string]any) (fileMutationPlan, error)) (fileMutationPlan, error) {
	pathArg, err := argparse.String(args, "path", true)
	if err != nil {
		return fileMutationPlan{}, err
	}
	path, err := normalizePathWithFS(fsys, pathArg)
	if err != nil {
		return fileMutationPlan{}, err
	}
	unlock, err := lockMutation(ctx, fsys, path)
	if err != nil {
		return fileMutationPlan{}, err
	}
	defer unlock()
	plan, err := planMutation(fsys, path, args)
	if err != nil {
		return fileMutationPlan{}, err
	}
	if plan.created {
		if err := createWorkspaceParentDirs(fsys, path); err != nil {
			return fileMutationPlan{}, err
		}
	}
	return plan, fsys.WriteFile(path, []byte(plan.after), plan.mode)
}

func planWriteMutation(fsys sandbox.FileSystem, target string, args map[string]any) (fileMutationPlan, error) {
	rawContent, exists := args["content"]
	if !exists {
		return fileMutationPlan{}, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("arg %q is required", "content"))
	}
	content, ok := rawContent.(string)
	if !ok {
		return fileMutationPlan{}, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("arg %q must be string", "content"))
	}
	ifRevision, err := argparse.String(args, "if_revision", false)
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
			return fileMutationPlan{}, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("target %q is a directory", target))
		}
		mode = info.Mode()
		raw, err := fsys.ReadFile(target)
		if err != nil {
			return fileMutationPlan{}, err
		}
		before = string(raw)
	case errors.Is(statErr, os.ErrNotExist):
		if ifRevision != "" {
			return fileMutationPlan{}, missingWriteRevisionTargetError()
		}
		created = true
	default:
		return fileMutationPlan{}, statErr
	}
	if !revisionsMatchFile(ifRevision, before, info) {
		return fileMutationPlan{}, staleRevisionError()
	}
	return fileMutationPlan{
		path:    target,
		created: created,
		before:  before,
		after:   content,
		mode:    mode,
	}, nil
}

func planPatchMutation(fsys sandbox.FileSystem, target string, args map[string]any) (fileMutationPlan, error) {
	edits, err := parsePatchEdits(args)
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
			return fileMutationPlan{}, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("target %q is a directory", target))
		}
		mode = fileInfo.Mode()
	}

	if !fileExists {
		err := tool.NewError(tool.ErrorCodeNotFound, "Patch target does not exist; use Write to create files")
		err.Retryable = true
		return fileMutationPlan{}, err
	}

	contentRaw, err := fsys.ReadFile(target)
	if err != nil {
		return fileMutationPlan{}, err
	}
	content := string(contentRaw)
	replacements, err := collectPatchReplacements(content, edits)
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
		path:      target,
		before:    content,
		after:     after,
		hunk:      hunk,
		mode:      mode,
		replaced:  len(replacements),
		editCount: len(edits),
	}, nil
}

func parsePatchEdits(args map[string]any) ([]patchEdit, error) {
	raw, ok := args["edits"]
	if !ok || raw == nil {
		return nil, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("arg %q is required", "edits"))
	}
	rawItems, ok := raw.([]any)
	if !ok {
		return nil, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("arg %q must be an array", "edits"))
	}
	if len(rawItems) == 0 {
		return nil, tool.NewError(tool.ErrorCodeInvalidInput, "arg \"edits\" must include at least one edit")
	}
	edits := make([]patchEdit, 0, len(rawItems))
	for idx, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("arg \"edits[%d]\" must be an object", idx))
		}
		if err := tool.RejectUnknownArgs(item, "old", "new", "replace_all", retiredExpectedReplacementsArg); err != nil {
			return nil, fmt.Errorf("patch edits[%d]: %w", idx, err)
		}
		oldValue, err := requiredPatchEditString(item, idx, "old")
		if err != nil {
			return nil, err
		}
		if oldValue == "" {
			return nil, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("arg \"edits[%d].old\" must be non-empty", idx))
		}
		newValue, err := requiredPatchEditString(item, idx, "new")
		if err != nil {
			return nil, err
		}
		replaceAll, err := argparse.Bool(item, "replace_all", false)
		if err != nil {
			return nil, err
		}
		edits = append(edits, patchEdit{
			old:        oldValue,
			new:        newValue,
			replaceAll: replaceAll,
		})
	}
	return edits, nil
}

func requiredPatchEditString(args map[string]any, editIndex int, key string) (string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return "", tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("arg \"edits[%d].%s\" is required", editIndex, key))
	}
	value, ok := raw.(string)
	if !ok {
		return "", tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("arg \"edits[%d].%s\" must be string", editIndex, key))
	}
	return value, nil
}

func collectPatchReplacements(content string, edits []patchEdit) ([]patchReplacement, error) {
	replacements := make([]patchReplacement, 0, len(edits))
	for idx, edit := range edits {
		matches := patchMatchRanges(content, edit.old, edit.replaceAll)
		if len(matches) == 0 {
			replacement, err := whitespacePatchReplacement(content, edit, idx)
			if err != nil {
				return nil, err
			}
			replacements = append(replacements, replacement)
			continue
		}
		if !edit.replaceAll && len(matches) != 1 {
			return nil, patchAmbiguousError(content, matches, idx)
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
	if err := validatePatchReplacementRanges(replacements); err != nil {
		return nil, err
	}
	return replacements, nil
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

func validatePatchReplacementRanges(replacements []patchReplacement) error {
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
			err := tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("Patch has overlapping edits %d and %d", prev.editIndex, next.editIndex))
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

func patchLineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}
