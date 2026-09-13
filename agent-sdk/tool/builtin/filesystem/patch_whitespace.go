package filesystem

import (
	"fmt"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const (
	patchWhitespaceMaxOldBytes  = 64 << 10
	patchWhitespaceMaxFileBytes = 1 << 20
	patchWhitespaceMatchBudget  = 8 << 20
)

func whitespacePatchReplacement(content string, edit patchEdit, editIndex int) (patchReplacement, error) {
	if edit.replaceAll {
		return patchReplacement{}, patchNotFoundError(content, edit.old, editIndex)
	}
	matches, complete := whitespacePatchMatchRanges(content, edit.old)
	if !complete {
		err := tool.NewError(tool.ErrorCodeOldTextNotFound, fmt.Sprintf("Patch edit %d exceeded the whitespace search limit; no edits written", editIndex))
		err.Hint = "Retry with exact old text, including whitespace."
		err.Retryable = true
		return patchReplacement{}, err
	}
	if len(matches) == 0 {
		return patchReplacement{}, patchNotFoundError(content, edit.old, editIndex)
	}
	if len(matches) > 1 {
		return patchReplacement{}, patchAmbiguousError(content, matches, editIndex)
	}
	match := matches[0]
	newValue, ok := preservePatchWhitespace(content[match.start:match.end], edit.old, edit.new)
	if !ok {
		return patchReplacement{}, patchUnsafeMatchError(content, match, editIndex)
	}
	return patchReplacement{start: match.start, end: match.end, new: newValue, editIndex: editIndex}, nil
}

// Whitespace matching uses complete lines, at least two nonblank lines, and one
// consistent indentation prefix added to or removed from every nonblank line.
// Only ASCII space/tab margins are ignored. Relative indentation, blank lines,
// line bodies, and the requested final newline remain significant.
//
// The file size limit bounds the line index and normalized search view as well
// as the scan. Two matches suffice to reject ambiguity. Exhausting the comparison budget
// discards even a sole candidate: an incomplete search cannot prove uniqueness.
func whitespacePatchMatchRanges(content, old string) ([]patchMatchRange, bool) {
	if len(old) > patchWhitespaceMaxOldBytes || len(content) > patchWhitespaceMaxFileBytes {
		return nil, false
	}
	oldLines := patchLines(old)
	nonblank := 0
	for _, line := range oldLines {
		if strings.Trim(line.text, " \t") != "" {
			nonblank++
		}
	}
	if nonblank < 2 {
		return nil, true
	}
	lines := patchLines(content)
	key, _ := patchWhitespaceKey(oldLines)
	source, starts := patchWhitespaceKey(lines)
	// Include the preceding line boundary in the search itself. Filtering after
	// Index would repeatedly compare long keys matched inside a line, outside
	// the candidate comparison budget. Prefixing both views also covers line 1.
	key, source = "\n"+key, "\n"+source
	var matches []patchMatchRange
	budget := patchWhitespaceMatchBudget
	for offset := 0; offset < len(source); {
		index := strings.Index(source[offset:], key)
		if index < 0 {
			break
		}
		index += offset
		offset = index + 1
		first := sort.SearchInts(starts, index)
		last := lines[first+len(oldLines)-1]
		end := last.end()
		if oldLines[len(oldLines)-1].ending == "" {
			end -= len(last.ending)
		} else if last.ending == "" {
			continue
		}
		budget -= len(old) + end - lines[first].start
		if budget < 0 {
			return nil, false
		}
		if !patchIndentationMatches(lines[first:first+len(oldLines)], oldLines) {
			continue
		}
		matches = append(matches, patchMatchRange{start: lines[first].start, end: end})
		if len(matches) == 2 {
			break
		}
	}
	return matches, true
}

func patchWhitespaceKey(lines []patchLine) (string, []int) {
	var key strings.Builder
	starts := make([]int, 0, len(lines))
	for _, line := range lines {
		starts = append(starts, key.Len())
		key.WriteString(strings.Trim(line.text, " \t"))
		key.WriteByte('\n')
	}
	return key.String(), starts
}

func patchIndentationMatches(actual, old []patchLine) bool {
	var prefix string
	var removing, initialized bool
	for i, line := range old {
		oldIndent, body, _ := patchLineMargins(line.text)
		if body == "" {
			continue
		}
		actualIndent, _, _ := patchLineMargins(actual[i].text)
		var delta string
		remove := false
		switch {
		case strings.HasSuffix(actualIndent, oldIndent):
			delta = actualIndent[:len(actualIndent)-len(oldIndent)]
		case strings.HasSuffix(oldIndent, actualIndent):
			delta = oldIndent[:len(oldIndent)-len(actualIndent)]
			remove = true
		default:
			return false
		}
		if initialized && (prefix != delta || removing != remove) {
			return false
		}
		prefix, removing, initialized = delta, remove, true
	}
	return true
}

// A tolerant match is not permission to overwrite its margins with model text.
// Line correspondence must be direct: old/new have the same line count, margin
// bytes and newline presence. Only nonblank line bodies may change; actual
// indentation, trailing whitespace and individual line endings are retained.
// Insertions, deletions and whitespace edits must use an exact old block.
func preservePatchWhitespace(actual, old, newValue string) (string, bool) {
	actualLines, oldLines, newLines := patchLines(actual), patchLines(old), patchLines(newValue)
	// A final whitespace-only pattern line without an ending may map to zero
	// bytes before an actual empty line's newline (which stays outside the
	// replacement range). Restore that logical line omitted by patchLines.
	if len(actualLines) > 0 && len(actualLines)+1 == len(oldLines) && actualLines[len(actualLines)-1].ending != "" {
		last := oldLines[len(oldLines)-1]
		if last.ending == "" && strings.Trim(last.text, " \t") == "" {
			actualLines = append(actualLines, patchLine{start: len(actual)})
		}
	}
	if len(oldLines) != len(newLines) || len(actualLines) != len(oldLines) {
		return "", false
	}
	var out strings.Builder
	changed := false
	for i, line := range oldLines {
		oldIndent, oldBody, oldTail := patchLineMargins(line.text)
		newIndent, newBody, newTail := patchLineMargins(newLines[i].text)
		if oldIndent != newIndent || oldTail != newTail || (line.ending == "") != (newLines[i].ending == "") {
			return "", false
		}
		if oldBody == newBody {
			out.WriteString(actualLines[i].text)
		} else {
			if oldBody == "" || newBody == "" {
				return "", false
			}
			indent, _, tail := patchLineMargins(actualLines[i].text)
			out.WriteString(indent)
			out.WriteString(newBody)
			out.WriteString(tail)
			changed = true
		}
		out.WriteString(actualLines[i].ending)
	}
	return out.String(), changed
}

func patchLineMargins(text string) (indent, body, tail string) {
	left := strings.TrimLeft(text, " \t")
	body = strings.TrimRight(left, " \t")
	return text[:len(text)-len(left)], body, left[len(body):]
}
