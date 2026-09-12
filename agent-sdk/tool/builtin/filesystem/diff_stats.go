package filesystem

import (
	"strings"

	"github.com/aymanbagabas/go-udiff/lcs"
)

type LineDiffStats struct {
	Added   int
	Removed int
}

func CountLineDiff(oldText, newText string) LineDiffStats {
	oldLines := splitDiffLines(oldText)
	newLines := splitDiffLines(newText)

	stats := LineDiffStats{}
	for _, edit := range lcs.DiffLines(oldLines, newLines) {
		stats.Removed += edit.End - edit.Start
		stats.Added += edit.ReplEnd - edit.ReplStart
	}
	return stats
}

func splitDiffLines(text string) []string {
	if text == "" {
		return nil
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	return strings.Split(normalized, "\n")
}
