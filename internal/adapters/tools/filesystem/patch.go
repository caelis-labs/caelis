package filesystem

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

type PatchFileTool struct {
	Sandbox sandbox.Runtime
}

type patchFileInput struct {
	Path  string      `json:"path"`
	Edits []patchEdit `json:"edits"`
}

type patchEdit struct {
	Old           string `json:"old"`
	New           string `json:"new"`
	ReplaceAll    bool   `json:"replace_all,omitempty"`
	ExpectedCount int    `json:"expected_count,omitempty"`
}

type patchReplacement struct {
	start int
	end   int
	new   string
}

func NewPatchFileTool(runtime sandbox.Runtime) (*PatchFileTool, error) {
	if runtime == nil {
		return nil, fmt.Errorf("tools/filesystem: sandbox runtime is required")
	}
	return &PatchFileTool{Sandbox: runtime}, nil
}

func (t *PatchFileTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        PatchFileToolName,
		Description: "Apply exact text replacements to one existing text file.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "File path."},
				"edits": map[string]any{
					"type":        "array",
					"description": "Exact text replacements to apply atomically.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"old":            map[string]any{"type": "string"},
							"new":            map[string]any{"type": "string"},
							"replace_all":    map[string]any{"type": "boolean"},
							"expected_count": map[string]any{"type": "integer"},
						},
						"required":             []any{"old", "new"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []any{"path", "edits"},
			"additionalProperties": false,
		},
		Meta: map[string]any{
			"caelis.permission": "write",
			"caelis.kind":       "filesystem",
		},
	}
}

func (t *PatchFileTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := checkContext(ctx); err != nil {
		return tool.Result{}, err
	}
	var input patchFileInput
	if err := decodeInput(call, &input); err != nil {
		return tool.Result{}, err
	}
	if len(input.Edits) == 0 {
		return tool.Result{}, fmt.Errorf("tools/filesystem: edits are required")
	}
	fsys, err := runtimeFileSystem(t.Sandbox, call.Meta)
	if err != nil {
		return tool.Result{}, err
	}
	target, err := normalizePath(fsys, input.Path)
	if err != nil {
		return tool.Result{}, err
	}
	before, created, mode, err := readExistingFile(fsys, target)
	if err != nil {
		return tool.Result{}, err
	}
	if created {
		return tool.Result{}, fmt.Errorf("tools/filesystem: patch target does not exist: %s", target)
	}
	replacements, err := collectPatchReplacements(target, before, input.Edits)
	if err != nil {
		return tool.Result{}, err
	}
	after := applyPatchReplacements(before, replacements)
	if err := fsys.WriteFile(target, []byte(after), mode); err != nil {
		return tool.Result{}, err
	}
	stats := countLineDiff(before, after)
	return jsonResult(call, PatchFileToolName, map[string]any{
		"path":              target,
		"changed":           before != after,
		"edit_count":        len(input.Edits),
		"replacement_count": len(replacements),
		"added_lines":       stats.Added,
		"removed_lines":     stats.Removed,
		"summary":           mutationSummary(false, stats.Added, stats.Removed),
	}, map[string]any{
		"path":              target,
		"changed":           before != after,
		"edit_count":        len(input.Edits),
		"replacement_count": len(replacements),
		"added_lines":       stats.Added,
		"removed_lines":     stats.Removed,
	})
}

func collectPatchReplacements(path string, content string, edits []patchEdit) ([]patchReplacement, error) {
	replacements := make([]patchReplacement, 0, len(edits))
	for idx, edit := range edits {
		if edit.Old == "" {
			return nil, fmt.Errorf("tools/filesystem: edits[%d].old is required", idx)
		}
		if edit.ReplaceAll && edit.ExpectedCount <= 0 {
			return nil, fmt.Errorf("tools/filesystem: edits[%d].expected_count is required when replace_all is true", idx)
		}
		matches := exactMatchRanges(content, edit.Old)
		if edit.ExpectedCount > 0 && len(matches) != edit.ExpectedCount {
			return nil, fmt.Errorf("tools/filesystem: %s expected %d matches for edit %d, found %d", path, edit.ExpectedCount, idx, len(matches))
		}
		if !edit.ReplaceAll && len(matches) != 1 {
			return nil, fmt.Errorf("tools/filesystem: %s edit %d matched %d times; expected exactly 1", path, idx, len(matches))
		}
		for _, match := range matches {
			replacements = append(replacements, patchReplacement{
				start: match.start,
				end:   match.end,
				new:   preserveReplacementLineEndings(edit.New, content[match.start:match.end]),
			})
			if !edit.ReplaceAll {
				break
			}
		}
	}
	sort.Slice(replacements, func(i int, j int) bool {
		return replacements[i].start < replacements[j].start
	})
	for i := 1; i < len(replacements); i++ {
		if replacements[i].start < replacements[i-1].end {
			return nil, fmt.Errorf("tools/filesystem: patch edits overlap")
		}
	}
	return replacements, nil
}

type matchRange struct {
	start int
	end   int
}

func exactMatchRanges(content string, old string) []matchRange {
	if old == "" {
		return nil
	}
	ranges := make([]matchRange, 0, 1)
	offset := 0
	for {
		idx := strings.Index(content[offset:], old)
		if idx < 0 {
			return ranges
		}
		start := offset + idx
		end := start + len(old)
		ranges = append(ranges, matchRange{start: start, end: end})
		offset = end
	}
}

func preserveReplacementLineEndings(replacement string, matched string) string {
	if strings.Contains(matched, "\r\n") && !strings.Contains(replacement, "\r\n") {
		return strings.ReplaceAll(strings.ReplaceAll(replacement, "\r\n", "\n"), "\n", "\r\n")
	}
	return replacement
}

func applyPatchReplacements(content string, replacements []patchReplacement) string {
	if len(replacements) == 0 {
		return content
	}
	var out strings.Builder
	cursor := 0
	for _, replacement := range replacements {
		out.WriteString(content[cursor:replacement.start])
		out.WriteString(replacement.new)
		cursor = replacement.end
	}
	out.WriteString(content[cursor:])
	return out.String()
}

var _ tool.Tool = (*PatchFileTool)(nil)
