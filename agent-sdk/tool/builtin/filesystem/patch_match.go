package filesystem

import "strings"

type patchMatchRange struct {
	start                 int
	end                   int
	normalizedLineEndings bool
}

// Exact matches take precedence over line-ending equivalents. replaceAll retains
// non-overlapping replacement semantics; single edits count overlapping anchors
// as distinct locations too.
func patchMatchRanges(content, oldValue string, replaceAll bool) []patchMatchRange {
	if matches := exactPatchMatchRanges(content, oldValue, replaceAll); len(matches) > 0 {
		return matches
	}
	normalizedContent, offsets := normalizePatchLineEndingsWithOffsets(content)
	normalizedOld := normalizePatchLineEndings(oldValue)
	if normalizedContent == content && normalizedOld == oldValue {
		return nil
	}
	normalizedMatches := exactPatchMatchRanges(normalizedContent, normalizedOld, replaceAll)
	ranges := make([]patchMatchRange, 0, len(normalizedMatches))
	for _, match := range normalizedMatches {
		ranges = append(ranges, patchMatchRange{
			start:                 offsets[match.start],
			end:                   offsets[match.end],
			normalizedLineEndings: true,
		})
	}
	return ranges
}

func exactPatchMatchRanges(content, oldValue string, replaceAll bool) []patchMatchRange {
	var ranges []patchMatchRange
	for offset := 0; offset <= len(content); {
		index := strings.Index(content[offset:], oldValue)
		if index < 0 {
			break
		}
		start := offset + index
		end := start + len(oldValue)
		offset = start + 1
		if patchRangeSplitsCRLF(content, start, end) {
			continue
		}
		ranges = append(ranges, patchMatchRange{start: start, end: end})
		if !replaceAll && len(ranges) == 2 {
			break
		}
		if replaceAll {
			offset = end
		}
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

// patchLines retains byte offsets and each original line ending. A final newline
// belongs to its preceding line, not to an extra empty line.
type patchLine struct {
	start  int
	text   string
	ending string
}

func (line patchLine) end() int {
	return line.start + len(line.text) + len(line.ending)
}

func patchLines(text string) []patchLine {
	var lines []patchLine
	for start := 0; start < len(text); {
		end := start + strings.IndexAny(text[start:], "\r\n")
		if end < start {
			lines = append(lines, patchLine{start: start, text: text[start:]})
			break
		}
		next := end + 1
		if text[end] == '\r' && next < len(text) && text[next] == '\n' {
			next++
		}
		lines = append(lines, patchLine{start: start, text: text[start:end], ending: text[end:next]})
		start = next
	}
	return lines
}
