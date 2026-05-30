package filesystem

import "strings"

const maxDiffStatCells = 250000

type lineDiffStats struct {
	Added   int
	Removed int
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
