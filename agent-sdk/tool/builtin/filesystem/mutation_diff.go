package filesystem

import (
	"fmt"
	"strings"

	"github.com/aymanbagabas/go-udiff/lcs"
	"github.com/caelis-labs/caelis/agent-sdk/internal/toolpresentation"
)

const (
	mutationDiffContextLines = 2
	mutationDiffMaxHunks     = 64
	mutationDiffMaxLines     = 800
)

type lineDiff struct {
	before, after []string
	edits         []lcs.Diff
}

func compareLines(before, after string) lineDiff {
	if before == after {
		return lineDiff{}
	}
	oldLines, newLines := splitDiffLines(before), splitDiffLines(after)
	return lineDiff{before: oldLines, after: newLines, edits: lcs.DiffLines(oldLines, newLines)}
}

func splitDiffLines(text string) []string {
	if text == "" {
		return nil
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	return strings.Split(strings.TrimSuffix(normalized, "\n"), "\n")
}

func (plan fileMutationPlan) describeChange() toolpresentation.FileChange {
	diff := compareLines(plan.before, plan.after)
	change := toolpresentation.FileChange{
		Created: plan.created, PreviousEmpty: plan.before == "",
		Revision: textRevision(plan.after), Hunk: plan.hunk,
	}
	for _, edit := range diff.edits {
		change.RemovedLines += edit.End - edit.Start
		change.AddedLines += edit.ReplEnd - edit.ReplStart
	}
	change.Hunks, change.Truncated = diff.hunks(mutationDiffContextLines, mutationDiffMaxHunks, mutationDiffMaxLines)
	return change
}

// Adjacent edits share a hunk when their context overlaps or touches. Only
// lines that fit the display budget are copied, even for widely spaced edits.
func (d lineDiff) hunks(contextLines, maxHunks, maxLines int) ([]toolpresentation.DiffHunk, bool) {
	var hunks []toolpresentation.DiffHunk
	for edits := d.edits; len(edits) > 0; {
		if len(hunks) == maxHunks || maxLines <= 1 {
			return hunks, true
		}
		end := 1
		for end < len(edits) && edits[end].Start-edits[end-1].End <= 2*contextLines {
			end++
		}
		hunk, truncated := d.hunk(edits[:end], contextLines, maxLines-1)
		hunks = append(hunks, hunk)
		if truncated {
			return hunks, true
		}
		maxLines -= len(hunk.Lines) + 1
		edits = edits[end:]
	}
	return hunks, false
}

func (d lineDiff) hunk(edits []lcs.Diff, contextLines, budget int) (toolpresentation.DiffHunk, bool) {
	first, last := edits[0], edits[len(edits)-1]
	leading := min(contextLines, first.Start)
	trailing := min(contextLines, len(d.before)-last.End)
	oldStart, newStart := first.Start-leading, first.ReplStart-leading
	h := toolpresentation.DiffHunk{
		OldLines: last.End + trailing - oldStart,
		NewLines: last.ReplEnd + trailing - newStart,
	}
	if h.OldLines > 0 {
		h.OldStart = oldStart + 1
	}
	if h.NewLines > 0 {
		h.NewStart = newStart + 1
	}
	h.Header = buildDiffHunkHeader(h.OldStart, h.OldLines, h.NewStart, h.NewLines)

	removed, added := first.End-first.Start, first.ReplEnd-first.ReplStart
	truncated := false
	appendLines := func(prefix string, lines []string) {
		n := min(len(lines), budget-len(h.Lines))
		for _, line := range lines[:n] {
			h.Lines = append(h.Lines, prefix+line)
		}
		truncated = truncated || n < len(lines)
	}
	if len(d.edits) == 1 && removed+added > budget {
		// For one large replacement reserve space for both sides and the context.
		leading = min(leading, budget)
		trailing = min(trailing, budget-leading)
		if leading+trailing == budget {
			leading, trailing = 0, 0
		}
		changed := budget - leading - trailing
		showRemoved := min(removed, max(1, changed/2))
		showAdded := min(added, changed-showRemoved)
		showRemoved = min(removed, changed-showAdded)
		appendLines(" ", d.before[oldStart:oldStart+leading])
		appendLines("-", d.before[first.Start:first.Start+showRemoved])
		appendLines("+", d.after[first.ReplStart:first.ReplStart+showAdded])
		appendLines(" ", d.before[last.End:last.End+trailing])
		return h, true
	}
	cursor := oldStart
	for _, edit := range edits {
		appendLines(" ", d.before[cursor:edit.Start])
		appendLines("-", d.before[edit.Start:edit.End])
		appendLines("+", d.after[edit.ReplStart:edit.ReplEnd])
		cursor = edit.End
	}
	appendLines(" ", d.before[cursor:cursor+trailing])
	return h, truncated
}

func buildPatchHunk(lineStart, oldLines, newLines int) string {
	return buildDiffHunkHeader(lineStart, oldLines, lineStart, newLines)
}

func buildDiffHunkHeader(oldStart, oldLines, newStart, newLines int) string {
	if oldStart <= 0 && oldLines > 0 {
		oldStart = 1
	}
	if newStart <= 0 && newLines > 0 {
		newStart = 1
	}
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldLines, newStart, newLines)
}
