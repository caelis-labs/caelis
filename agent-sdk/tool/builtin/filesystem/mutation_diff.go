package filesystem

import (
	"fmt"
)

const (
	mutationDiffContextLines = 2
	mutationDiffMaxHunks     = 64
	mutationDiffMaxLines     = 800
	mutationDiffMaxCells     = maxDiffStatCells
)

type MutationDiffHunk struct {
	Header   string   `json:"header"`
	OldStart int      `json:"old_start"`
	OldLines int      `json:"old_lines"`
	NewStart int      `json:"new_start"`
	NewLines int      `json:"new_lines"`
	Lines    []string `json:"lines"`
}

type mutationDiffRow struct {
	kind    byte
	oldLine int
	newLine int
	text    string
}

type mutationDiffLinePair struct {
	oldIndex int
	newIndex int
}

func mutationDiffResultMeta(before, after string) ([]MutationDiffHunk, bool) {
	return BuildMutationDiffHunks(before, after, mutationDiffContextLines, mutationDiffMaxHunks, mutationDiffMaxLines)
}

func attachMutationDiffMeta(meta map[string]any, before, after, fallbackHunk string) {
	if meta == nil {
		return
	}
	toolMeta := mutationToolMetadata(meta)
	hunks, truncated := mutationDiffResultMeta(before, after)
	if len(hunks) == 0 {
		return
	}
	toolMeta["diff_hunks"] = hunks
	if truncated {
		toolMeta["diff_truncated"] = true
	}
	if fallbackHunk == "" {
		toolMeta["hunk"] = hunks[0].Header
	}
}

func mutationToolMetadata(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	if caelis == nil {
		caelis = map[string]any{}
		meta["caelis"] = caelis
	}
	if _, ok := caelis["version"]; !ok {
		caelis["version"] = 1
	}
	runtime, _ := caelis["runtime"].(map[string]any)
	if runtime == nil {
		runtime = map[string]any{}
		caelis["runtime"] = runtime
	}
	toolMeta, _ := runtime["tool"].(map[string]any)
	if toolMeta == nil {
		toolMeta = map[string]any{}
		runtime["tool"] = toolMeta
	}
	return toolMeta
}

func BuildMutationDiffHunks(before, after string, contextLines, maxHunks, maxLines int) ([]MutationDiffHunk, bool) {
	oldLines := splitDiffLines(before)
	newLines := splitDiffLines(after)
	if linesEqual(oldLines, newLines) {
		return nil, false
	}
	if contextLines < 0 {
		contextLines = 0
	}
	if maxHunks <= 0 {
		maxHunks = mutationDiffMaxHunks
	}
	if maxLines <= 0 {
		maxLines = mutationDiffMaxLines
	}
	prefix, suffix := commonMutationLineAffixCounts(oldLines, newLines)
	oldCoreLen := len(oldLines) - prefix - suffix
	newCoreLen := len(newLines) - prefix - suffix
	if diffMatrixTooLarge(oldCoreLen, newCoreLen) {
		return buildLargeMutationDiffHunk(oldLines, newLines, prefix, suffix, contextLines, maxLines)
	}
	rows := buildMutationDiffRows(oldLines, newLines)
	return buildMutationDiffHunksFromRows(rows, contextLines, maxHunks, maxLines)
}

func buildMutationDiffRows(oldLines, newLines []string) []mutationDiffRow {
	prefix, suffix := commonMutationLineAffixCounts(oldLines, newLines)
	pairs := mutationLinePairs(oldLines[prefix:len(oldLines)-suffix], newLines[prefix:len(newLines)-suffix], prefix, prefix)
	rows := make([]mutationDiffRow, 0, len(oldLines)+len(newLines))
	for idx := 0; idx < prefix; idx++ {
		rows = append(rows, mutationDiffRow{kind: ' ', oldLine: idx + 1, newLine: idx + 1, text: oldLines[idx]})
	}

	oldCursor := prefix
	newCursor := prefix
	for _, pair := range pairs {
		rows = appendMutationChangedRows(rows, oldLines, newLines, oldCursor, pair.oldIndex, newCursor, pair.newIndex)
		rows = append(rows, mutationDiffRow{
			kind:    ' ',
			oldLine: pair.oldIndex + 1,
			newLine: pair.newIndex + 1,
			text:    oldLines[pair.oldIndex],
		})
		oldCursor = pair.oldIndex + 1
		newCursor = pair.newIndex + 1
	}

	oldCoreEnd := len(oldLines) - suffix
	newCoreEnd := len(newLines) - suffix
	rows = appendMutationChangedRows(rows, oldLines, newLines, oldCursor, oldCoreEnd, newCursor, newCoreEnd)
	for offset := 0; offset < suffix; offset++ {
		oldIdx := oldCoreEnd + offset
		newIdx := newCoreEnd + offset
		rows = append(rows, mutationDiffRow{kind: ' ', oldLine: oldIdx + 1, newLine: newIdx + 1, text: oldLines[oldIdx]})
	}
	return rows
}

func appendMutationChangedRows(rows []mutationDiffRow, oldLines, newLines []string, oldStart, oldEnd, newStart, newEnd int) []mutationDiffRow {
	for idx := oldStart; idx < oldEnd; idx++ {
		rows = append(rows, mutationDiffRow{kind: '-', oldLine: idx + 1, text: oldLines[idx]})
	}
	for idx := newStart; idx < newEnd; idx++ {
		rows = append(rows, mutationDiffRow{kind: '+', newLine: idx + 1, text: newLines[idx]})
	}
	return rows
}

func mutationLinePairs(oldLines, newLines []string, oldBase, newBase int) []mutationDiffLinePair {
	if len(oldLines) == 0 || len(newLines) == 0 {
		return nil
	}
	if len(oldLines)*len(newLines) > mutationDiffMaxCells {
		return nil
	}
	dp := make([][]int, len(oldLines)+1)
	for i := range dp {
		dp[i] = make([]int, len(newLines)+1)
	}
	for i := len(oldLines) - 1; i >= 0; i-- {
		for j := len(newLines) - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	pairs := make([]mutationDiffLinePair, 0, dp[0][0])
	for oldIdx, newIdx := 0, 0; oldIdx < len(oldLines) && newIdx < len(newLines); {
		switch {
		case oldLines[oldIdx] == newLines[newIdx]:
			pairs = append(pairs, mutationDiffLinePair{oldIndex: oldBase + oldIdx, newIndex: newBase + newIdx})
			oldIdx++
			newIdx++
		case dp[oldIdx+1][newIdx] >= dp[oldIdx][newIdx+1]:
			oldIdx++
		default:
			newIdx++
		}
	}
	return pairs
}

func buildMutationDiffHunksFromRows(rows []mutationDiffRow, contextLines, maxHunks, maxLines int) ([]MutationDiffHunk, bool) {
	var ranges [][2]int
	for idx, row := range rows {
		if row.kind == ' ' {
			continue
		}
		start := maxInt(0, idx-contextLines)
		end := minInt(len(rows)-1, idx+contextLines)
		if len(ranges) > 0 && start <= ranges[len(ranges)-1][1]+1 {
			if end > ranges[len(ranges)-1][1] {
				ranges[len(ranges)-1][1] = end
			}
			continue
		}
		ranges = append(ranges, [2]int{start, end})
	}
	if len(ranges) == 0 {
		return nil, false
	}

	hunks := make([]MutationDiffHunk, 0, minInt(len(ranges), maxHunks))
	truncated := false
	lineBudget := maxLines
	for _, one := range ranges {
		if len(hunks) >= maxHunks {
			truncated = true
			break
		}
		availableLines := lineBudget - 1
		if availableLines <= 0 {
			truncated = true
			break
		}
		hunk, hunkTruncated := buildMutationDiffHunk(rows[one[0]:one[1]+1], availableLines)
		cost := len(hunk.Lines) + 1
		if len(hunk.Lines) == 0 {
			truncated = true
			break
		}
		hunks = append(hunks, hunk)
		lineBudget -= cost
		if hunkTruncated {
			truncated = true
			break
		}
	}
	return hunks, truncated
}

func buildMutationDiffHunk(rows []mutationDiffRow, maxLines int) (MutationDiffHunk, bool) {
	hunk := MutationDiffHunk{}
	truncated := false
	for idx, row := range rows {
		if row.oldLine > 0 && hunk.OldStart == 0 {
			hunk.OldStart = row.oldLine
		}
		if row.newLine > 0 && hunk.NewStart == 0 {
			hunk.NewStart = row.newLine
		}
		switch row.kind {
		case '+':
			hunk.NewLines++
		case '-':
			hunk.OldLines++
		default:
			hunk.OldLines++
			hunk.NewLines++
		}
		if idx < maxLines {
			hunk.Lines = append(hunk.Lines, string(row.kind)+row.text)
		} else {
			truncated = true
		}
	}
	hunk.Header = buildDiffHunkHeader(hunk.OldStart, hunk.OldLines, hunk.NewStart, hunk.NewLines)
	return hunk, truncated
}

func buildLargeMutationDiffHunk(oldLines, newLines []string, prefix, suffix, contextLines, maxLines int) ([]MutationDiffHunk, bool) {
	oldCoreEnd := len(oldLines) - suffix
	newCoreEnd := len(newLines) - suffix
	oldStart := maxInt(0, prefix-contextLines)
	newStart := maxInt(0, prefix-contextLines)
	oldEnd := minInt(len(oldLines), oldCoreEnd+contextLines)
	newEnd := minInt(len(newLines), newCoreEnd+contextLines)
	oldCount := maxInt(0, oldEnd-oldStart)
	newCount := maxInt(0, newEnd-newStart)

	hunk := MutationDiffHunk{
		OldStart: hunkStartLine(oldStart, oldCount),
		OldLines: oldCount,
		NewStart: hunkStartLine(newStart, newCount),
		NewLines: newCount,
	}
	hunk.Header = buildDiffHunkHeader(hunk.OldStart, hunk.OldLines, hunk.NewStart, hunk.NewLines)
	lineBudget := maxLines - 1
	if lineBudget <= 0 {
		return nil, true
	}
	truncated := appendLargeMutationDiffLines(&hunk, oldLines, newLines, oldStart, prefix, oldCoreEnd, newCoreEnd, oldEnd, newEnd, lineBudget)
	if len(hunk.Lines) == 0 {
		return nil, true
	}
	return []MutationDiffHunk{hunk}, truncated
}

func appendLargeMutationDiffLines(hunk *MutationDiffHunk, oldLines, newLines []string, oldStart, prefix, oldCoreEnd, newCoreEnd, oldEnd, newEnd, lineBudget int) bool {
	prefixContext := maxInt(0, prefix-oldStart)
	suffixContext := maxInt(0, minInt(oldEnd-oldCoreEnd, newEnd-newCoreEnd))
	removed := maxInt(0, oldCoreEnd-prefix)
	added := maxInt(0, newCoreEnd-prefix)
	total := prefixContext + removed + added + suffixContext
	if total <= lineBudget {
		appendDiffContextLines(hunk, oldLines, oldStart, prefix)
		appendDiffChangedLines(hunk, "-", oldLines, prefix, oldCoreEnd)
		appendDiffChangedLines(hunk, "+", newLines, prefix, newCoreEnd)
		appendDiffContextLines(hunk, oldLines, oldCoreEnd, oldEnd)
		return false
	}

	truncated := true
	prefixBudget := minInt(prefixContext, lineBudget)
	suffixBudget := 0
	if remaining := lineBudget - prefixBudget; remaining > 0 {
		suffixBudget = minInt(suffixContext, remaining)
	}
	changedBudget := lineBudget - prefixBudget - suffixBudget
	if changedBudget <= 0 && removed+added > 0 {
		shift := minInt(prefixBudget+suffixBudget, removed+added)
		changedBudget += shift
		if suffixBudget >= shift {
			suffixBudget -= shift
		} else {
			shift -= suffixBudget
			suffixBudget = 0
			prefixBudget = maxInt(0, prefixBudget-shift)
		}
	}
	removedBudget, addedBudget := splitChangedDiffBudget(removed, added, changedBudget)

	appendDiffContextLines(hunk, oldLines, oldStart, oldStart+prefixBudget)
	appendDiffChangedLines(hunk, "-", oldLines, prefix, prefix+removedBudget)
	appendDiffChangedLines(hunk, "+", newLines, prefix, prefix+addedBudget)
	appendDiffContextLines(hunk, oldLines, oldCoreEnd, oldCoreEnd+suffixBudget)
	return truncated
}

func splitChangedDiffBudget(removed, added, budget int) (int, int) {
	if budget <= 0 {
		return 0, 0
	}
	if removed <= 0 {
		return 0, minInt(added, budget)
	}
	if added <= 0 {
		return minInt(removed, budget), 0
	}
	if budget == 1 {
		return 1, 0
	}
	removedBudget := minInt(removed, maxInt(1, budget/2))
	addedBudget := minInt(added, maxInt(1, budget-removedBudget))
	if unused := budget - removedBudget - addedBudget; unused > 0 {
		if removedBudget < removed {
			extra := minInt(unused, removed-removedBudget)
			removedBudget += extra
			unused -= extra
		}
		if unused > 0 && addedBudget < added {
			addedBudget += minInt(unused, added-addedBudget)
		}
	}
	return removedBudget, addedBudget
}

func appendDiffContextLines(hunk *MutationDiffHunk, lines []string, start, end int) {
	for idx := start; idx < end && idx < len(lines); idx++ {
		hunk.Lines = append(hunk.Lines, " "+lines[idx])
	}
}

func appendDiffChangedLines(hunk *MutationDiffHunk, prefix string, lines []string, start, end int) {
	for idx := start; idx < end && idx < len(lines); idx++ {
		hunk.Lines = append(hunk.Lines, prefix+lines[idx])
	}
}

func hunkStartLine(startIndex, lineCount int) int {
	if lineCount <= 0 {
		return 0
	}
	return startIndex + 1
}

func commonMutationLineAffixCounts(oldLines, newLines []string) (int, int) {
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for prefix+suffix < len(oldLines) &&
		prefix+suffix < len(newLines) &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	return prefix, suffix
}

func linesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for idx := range left {
		if left[idx] != right[idx] {
			return false
		}
	}
	return true
}

func diffMatrixTooLarge(oldLen, newLen int) bool {
	if oldLen <= 0 || newLen <= 0 {
		return false
	}
	return oldLen > mutationDiffMaxCells/newLen
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
