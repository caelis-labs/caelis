package filesystem

import (
	"fmt"
	"strings"
)

const maxDiffStatCells = 250000

const (
	unifiedDiffContext      = 2
	maxUnifiedDiffCells     = 250000
	maxUnifiedDiffHunks     = 8
	maxUnifiedDiffLines     = 160
	maxUnifiedDiffLineChars = 500
)

type lineDiffStats struct {
	Added   int
	Removed int
}

type unifiedDiff struct {
	Hunks     []unifiedDiffHunk
	Truncated bool
}

type unifiedDiffHunk struct {
	Header string   `json:"header"`
	Lines  []string `json:"lines"`
}

type diffOpKind int

const (
	diffOpEqual diffOpKind = iota
	diffOpDelete
	diffOpInsert
)

type positionedDiffOp struct {
	kind      diffOpKind
	line      string
	oldLine   int
	newLine   int
	oldBefore int
	newBefore int
}

func countLineDiff(oldText string, newText string) lineDiffStats {
	oldLines := splitDiffLines(oldText)
	newLines := splitDiffLines(newText)
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	oldLines = oldLines[prefix:]
	newLines = newLines[prefix:]
	suffix := 0
	for suffix < len(oldLines) && suffix < len(newLines) &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	if suffix > 0 {
		oldLines = oldLines[:len(oldLines)-suffix]
		newLines = newLines[:len(newLines)-suffix]
	}
	if len(oldLines) == 0 || len(newLines) == 0 {
		return lineDiffStats{Added: len(newLines), Removed: len(oldLines)}
	}
	if len(oldLines)*len(newLines) > maxDiffStatCells {
		return lineDiffStats{Added: len(newLines), Removed: len(oldLines)}
	}
	lcs := lcsLineCount(oldLines, newLines)
	return lineDiffStats{
		Added:   len(newLines) - lcs,
		Removed: len(oldLines) - lcs,
	}
}

func splitDiffLines(text string) []string {
	if text == "" {
		return nil
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	return strings.Split(normalized, "\n")
}

func lcsLineCount(a []string, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if len(a) < len(b) {
		return lcsLineCountShort(a, b)
	}
	return lcsLineCountShort(b, a)
}

func lcsLineCountShort(shorter []string, longer []string) int {
	prev := make([]int, len(shorter)+1)
	curr := make([]int, len(shorter)+1)
	for _, longLine := range longer {
		for i, shortLine := range shorter {
			switch {
			case shortLine == longLine:
				curr[i+1] = prev[i] + 1
			case curr[i] > prev[i+1]:
				curr[i+1] = curr[i]
			default:
				curr[i+1] = prev[i+1]
			}
		}
		prev, curr = curr, prev
		clear(curr)
	}
	return prev[len(shorter)]
}

func buildUnifiedDiff(oldText string, newText string) unifiedDiff {
	if oldText == newText {
		return unifiedDiff{}
	}
	oldLines := splitDiffLines(oldText)
	newLines := splitDiffLines(newText)
	ops, ok := lineDiffOps(oldLines, newLines)
	if !ok {
		return fallbackUnifiedDiff(oldLines, newLines)
	}
	return unifiedDiffFromOps(ops)
}

func addUnifiedDiffMetadata(meta map[string]any, diff unifiedDiff) {
	if len(diff.Hunks) == 0 {
		return
	}
	meta["diff_hunks"] = diff.Hunks
	if diff.Truncated {
		meta["diff_truncated"] = true
	}
}

func lineDiffOps(oldLines []string, newLines []string) ([]positionedDiffOp, bool) {
	if len(oldLines)*len(newLines) > maxUnifiedDiffCells {
		return nil, false
	}
	width := len(newLines) + 1
	dp := make([]int, (len(oldLines)+1)*width)
	cell := func(i int, j int) int {
		return i*width + j
	}
	for i := len(oldLines) - 1; i >= 0; i-- {
		for j := len(newLines) - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[cell(i, j)] = dp[cell(i+1, j+1)] + 1
				continue
			}
			if dp[cell(i+1, j)] >= dp[cell(i, j+1)] {
				dp[cell(i, j)] = dp[cell(i+1, j)]
			} else {
				dp[cell(i, j)] = dp[cell(i, j+1)]
			}
		}
	}
	ops := make([]positionedDiffOp, 0, len(oldLines)+len(newLines))
	oldPos := 1
	newPos := 1
	i := 0
	j := 0
	for i < len(oldLines) || j < len(newLines) {
		switch {
		case i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j]:
			ops = append(ops, positionedDiffOp{
				kind:      diffOpEqual,
				line:      oldLines[i],
				oldLine:   oldPos,
				newLine:   newPos,
				oldBefore: oldPos,
				newBefore: newPos,
			})
			i++
			j++
			oldPos++
			newPos++
		case i < len(oldLines) && (j >= len(newLines) || dp[cell(i+1, j)] >= dp[cell(i, j+1)]):
			ops = append(ops, positionedDiffOp{
				kind:      diffOpDelete,
				line:      oldLines[i],
				oldLine:   oldPos,
				oldBefore: oldPos,
				newBefore: newPos,
			})
			i++
			oldPos++
		default:
			ops = append(ops, positionedDiffOp{
				kind:      diffOpInsert,
				line:      newLines[j],
				newLine:   newPos,
				oldBefore: oldPos,
				newBefore: newPos,
			})
			j++
			newPos++
		}
	}
	return ops, true
}

func unifiedDiffFromOps(ops []positionedDiffOp) unifiedDiff {
	groups := diffChangeGroups(ops)
	if len(groups) == 0 {
		return unifiedDiff{}
	}
	out := unifiedDiff{Hunks: make([]unifiedDiffHunk, 0, minInt(len(groups), maxUnifiedDiffHunks))}
	totalLines := 0
	for _, group := range groups {
		if len(out.Hunks) >= maxUnifiedDiffHunks {
			out.Truncated = true
			break
		}
		hunk := diffHunkFromOps(ops[group.start:group.end])
		if len(hunk.Lines) == 0 {
			continue
		}
		if totalLines+len(hunk.Lines) > maxUnifiedDiffLines {
			allowed := maxUnifiedDiffLines - totalLines
			if allowed <= 0 {
				out.Truncated = true
				break
			}
			hunk.Lines = hunk.Lines[:allowed]
			out.Truncated = true
			out.Hunks = append(out.Hunks, hunk)
			break
		}
		totalLines += len(hunk.Lines)
		out.Hunks = append(out.Hunks, hunk)
	}
	if len(out.Hunks) == 0 && !out.Truncated {
		return unifiedDiff{}
	}
	return out
}

type diffChangeGroup struct {
	start int
	end   int
}

func diffChangeGroups(ops []positionedDiffOp) []diffChangeGroup {
	groups := make([]diffChangeGroup, 0, 1)
	firstChange := -1
	lastChange := -1
	prevChange := -1
	for idx, op := range ops {
		if op.kind == diffOpEqual {
			continue
		}
		if firstChange < 0 {
			firstChange = idx
			lastChange = idx
			prevChange = idx
			continue
		}
		if idx-prevChange-1 <= unifiedDiffContext*2 {
			lastChange = idx
			prevChange = idx
			continue
		}
		groups = append(groups, diffChangeGroup{
			start: maxInt(firstChange-unifiedDiffContext, 0),
			end:   minInt(lastChange+unifiedDiffContext+1, len(ops)),
		})
		firstChange = idx
		lastChange = idx
		prevChange = idx
	}
	if firstChange >= 0 {
		groups = append(groups, diffChangeGroup{
			start: maxInt(firstChange-unifiedDiffContext, 0),
			end:   minInt(lastChange+unifiedDiffContext+1, len(ops)),
		})
	}
	return groups
}

func diffHunkFromOps(ops []positionedDiffOp) unifiedDiffHunk {
	oldStart, oldLen := diffOldRange(ops)
	newStart, newLen := diffNewRange(ops)
	lines := make([]string, 0, len(ops))
	for _, op := range ops {
		prefix := " "
		switch op.kind {
		case diffOpDelete:
			prefix = "-"
		case diffOpInsert:
			prefix = "+"
		}
		lines = append(lines, prefix+truncateDiffLine(op.line))
	}
	return unifiedDiffHunk{
		Header: fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldLen, newStart, newLen),
		Lines:  lines,
	}
}

func diffOldRange(ops []positionedDiffOp) (int, int) {
	if len(ops) == 0 {
		return 0, 0
	}
	start := 0
	count := 0
	fallback := ops[0].oldBefore
	for _, op := range ops {
		if op.kind != diffOpEqual && op.kind != diffOpDelete {
			continue
		}
		if start == 0 {
			start = op.oldLine
		}
		count++
	}
	if count == 0 {
		return maxInt(fallback-1, 0), 0
	}
	return start, count
}

func diffNewRange(ops []positionedDiffOp) (int, int) {
	if len(ops) == 0 {
		return 0, 0
	}
	start := 0
	count := 0
	fallback := ops[0].newBefore
	for _, op := range ops {
		if op.kind != diffOpEqual && op.kind != diffOpInsert {
			continue
		}
		if start == 0 {
			start = op.newLine
		}
		count++
	}
	if count == 0 {
		return maxInt(fallback-1, 0), 0
	}
	return start, count
}

func fallbackUnifiedDiff(oldLines []string, newLines []string) unifiedDiff {
	oldStart, oldChanged, newStart, newChanged := trimCommonDiffEdges(oldLines, newLines)
	lines := make([]string, 0, minInt(len(oldChanged)+len(newChanged), maxUnifiedDiffLines))
	for _, line := range oldChanged {
		if len(lines) >= maxUnifiedDiffLines {
			return unifiedDiff{Hunks: []unifiedDiffHunk{{Header: fallbackDiffHeader(oldStart, oldChanged, newStart, newChanged), Lines: lines}}, Truncated: true}
		}
		lines = append(lines, "-"+truncateDiffLine(line))
	}
	for _, line := range newChanged {
		if len(lines) >= maxUnifiedDiffLines {
			return unifiedDiff{Hunks: []unifiedDiffHunk{{Header: fallbackDiffHeader(oldStart, oldChanged, newStart, newChanged), Lines: lines}}, Truncated: true}
		}
		lines = append(lines, "+"+truncateDiffLine(line))
	}
	return unifiedDiff{
		Hunks:     []unifiedDiffHunk{{Header: fallbackDiffHeader(oldStart, oldChanged, newStart, newChanged), Lines: lines}},
		Truncated: true,
	}
}

func trimCommonDiffEdges(oldLines []string, newLines []string) (int, []string, int, []string) {
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	oldTail := len(oldLines)
	newTail := len(newLines)
	for oldTail > prefix && newTail > prefix && oldLines[oldTail-1] == newLines[newTail-1] {
		oldTail--
		newTail--
	}
	oldStart := prefix + 1
	newStart := prefix + 1
	if oldTail == prefix {
		oldStart = prefix
	}
	if newTail == prefix {
		newStart = prefix
	}
	return oldStart, oldLines[prefix:oldTail], newStart, newLines[prefix:newTail]
}

func fallbackDiffHeader(oldStart int, oldLines []string, newStart int, newLines []string) string {
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, len(oldLines), newStart, len(newLines))
}

func truncateDiffLine(line string) string {
	runes := []rune(line)
	if len(runes) <= maxUnifiedDiffLineChars {
		return line
	}
	return string(runes[:maxUnifiedDiffLineChars]) + "...(truncated)"
}

func mutationSummary(created bool, added int, removed int) string {
	switch {
	case created:
		return "created file"
	case added == 0 && removed == 0:
		return "file unchanged"
	case added > 0 && removed > 0:
		return "updated file"
	case added > 0:
		return "added lines"
	default:
		return "removed lines"
	}
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}
