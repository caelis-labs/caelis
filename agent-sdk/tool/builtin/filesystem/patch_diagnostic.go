package filesystem

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Patch mismatch diagnostics spare the model a second Read: a hint carries a
// possible match as a JSON-escaped, directly copyable old value, or a read
// action. The reason and no-edit guarantee live in the message; hints never
// repeat them, include the path, or echo the caller's old/new. All output is
// byte-bounded so a failed Patch stays cheap to feed back to the model.
const (
	patchDiagnosticHintBudget     = 1200
	patchDiagnosticMaxSearchBytes = 256 << 10
	patchDiagnosticMaxSearchLines = 5000
	patchDiagnosticMaxOldBytes    = 8 << 10
	patchDiagnosticMinMatchBytes  = 8
	patchDiagnosticMaxCandidates  = 6
	patchDiagnosticMaxAmbiguous   = 3
	patchDiagnosticMaxDiffLines   = 6
	patchDiagnosticDiffLineBytes  = 240
	patchDiagnosticScanLineBytes  = 1024

	patchDiagnosticReadAction = "Read the file and retry with the exact old text."
	patchDiagnosticIndentNote = "Align unchanged whitespace in new with this old."
)

// patchNotFoundError reports a retryable old_text_not_found for an edit that
// matched nothing, even after whitespace-tolerant matching.
func patchNotFoundError(content, old string, editIndex int) error {
	return patchDiagnosticError(tool.ErrorCodeOldTextNotFound,
		fmt.Sprintf("Patch edit %d found no match for old; no edits written", editIndex),
		patchNotFoundHint(content, old))
}

// patchAmbiguousError reports a retryable too_many_matches without a total,
// since the whitespace matcher stops at the second match.
func patchAmbiguousError(content string, matches []patchMatchRange, editIndex int) error {
	return patchDiagnosticError(tool.ErrorCodeTooManyMatches,
		fmt.Sprintf("Patch edit %d is ambiguous; no edits written", editIndex),
		patchAmbiguousHint(content, matches))
}

// patchUnsafeMatchError reports a retryable old_text_not_found for a unique
// whitespace-repaired match whose new cannot be mapped back safely.
func patchUnsafeMatchError(content string, match patchMatchRange, editIndex int) error {
	return patchDiagnosticError(tool.ErrorCodeOldTextNotFound,
		fmt.Sprintf("Patch edit %d matched only after whitespace repair; no edits written", editIndex),
		patchUnsafeMatchHint(content, match))
}

func patchDiagnosticError(code tool.ErrorCode, message, hint string) error {
	err := tool.NewError(code, message)
	err.Hint = hint
	err.Retryable = true
	return err
}

func patchNotFoundHint(content, old string) string {
	candidate, ok := patchPossibleMatch(content, old)
	if !ok {
		return patchDiagnosticReadAction
	}
	location := patchDiagnosticLinePhrase(candidate.startLine, candidate.endLine)
	if head := fmt.Sprintf("Possible match at %s; check it is the intended location:\n%s\n%s", location, patchDiagnosticQuote(candidate.block), patchDiagnosticIndentNote); len(head) <= patchDiagnosticHintBudget {
		return head
	}
	if hint, ok := patchDiagnosticFitDiffLines(fmt.Sprintf("Possible match at %s (block too large to embed); differing lines (partial):", location), candidate.diffLines); ok {
		return hint
	}
	return fmt.Sprintf("Possible match at %s; the block is too large to embed, so read those lines and retry.", location)
}

func patchAmbiguousHint(content string, matches []patchMatchRange) string {
	limit := min(len(matches), patchDiagnosticMaxAmbiguous)
	positions := make([]string, 0, limit)
	for _, match := range matches[:limit] {
		line, column := patchDiagnosticLineColumn(content, match.start)
		positions = append(positions, fmt.Sprintf("%d:%d", line, column))
	}
	return fmt.Sprintf("Matches at line:column %s; add distinguishing context to old.", strings.Join(positions, ", "))
}

func patchUnsafeMatchHint(content string, match patchMatchRange) string {
	location := patchDiagnosticLinePhrase(patchDiagnosticLineNumber(content, match.start), patchDiagnosticLineNumber(content, match.end-1))
	prefix := fmt.Sprintf("Possible match at %s; align unchanged whitespace in new with this old:", location)
	if hint := prefix + "\n" + patchDiagnosticQuote(content[match.start:match.end]); len(hint) <= patchDiagnosticHintBudget {
		return hint
	}
	return fmt.Sprintf("Possible match at %s; whitespace differs from old, so read those lines and retry with the exact old text.", location)
}

type patchDiagnosticCandidate struct {
	startLine int
	endLine   int
	block     string
	diffLines []patchDiagnosticDiffLine
}

type patchDiagnosticDiffLine struct {
	number int
	raw    string
}

// patchPossibleMatch reports the whole-line run that looks most like old. It is
// a bounded anchor scan over candidate windows, not a general fuzzy matcher, so
// a hit is offered as possible and callers echo actual bytes to keep retries
// exact.
func patchPossibleMatch(content, old string) (patchDiagnosticCandidate, bool) {
	if len(old) > patchDiagnosticMaxOldBytes || len(strings.TrimSpace(old)) < patchDiagnosticMinMatchBytes || len(content) > patchDiagnosticMaxSearchBytes {
		return patchDiagnosticCandidate{}, false
	}
	contentLines, oldLines := patchLines(content), patchLines(old)
	if len(oldLines) > len(contentLines) || len(contentLines) > patchDiagnosticMaxSearchLines {
		return patchDiagnosticCandidate{}, false
	}

	anchorIndex := patchDiagnosticAnchor(oldLines)
	anchorKey := patchDiagnosticLineKey(oldLines[anchorIndex])
	anchorLimit := patchDiagnosticLineDistanceLimit(len(anchorKey))
	anchorScan := patchDiagnosticTruncate(anchorKey, patchDiagnosticScanLineBytes)

	type scanHit struct {
		line     int
		distance int
	}
	var hits []scanHit
	for index := range contentLines {
		key := patchDiagnosticLineKey(contentLines[index])
		if len(key)-len(anchorKey) > anchorLimit || len(anchorKey)-len(key) > anchorLimit {
			continue
		}
		if distance := patchDiagnosticDistance(anchorScan, patchDiagnosticTruncate(key, len(anchorScan)), anchorLimit); distance <= anchorLimit {
			hits = append(hits, scanHit{line: index, distance: distance})
		}
	}
	if len(hits) == 0 {
		return patchDiagnosticCandidate{}, false
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].distance < hits[j].distance })
	hits = hits[:min(len(hits), patchDiagnosticMaxCandidates)]

	best, bestDistance := -1, 0
	for _, hit := range hits {
		start := hit.line - anchorIndex
		if start < 0 || start+len(oldLines) > len(contentLines) {
			continue
		}
		total, reference, ok := patchWindowDistance(oldLines, contentLines[start:start+len(oldLines)])
		if !ok || !patchDiagnosticTrusted(total, reference) {
			continue
		}
		if best < 0 || total < bestDistance {
			best, bestDistance = start, total
		}
	}
	if best < 0 {
		return patchDiagnosticCandidate{}, false
	}
	return patchDiagnosticCandidateAt(content, contentLines, oldLines, best), true
}

// patchWindowDistance sums per-line distances, but rejects the window unless
// every line is within its own limit: a saturated sentinel must never be
// accumulated into a total that then reads as a small average.
func patchWindowDistance(oldLines, actualLines []patchLine) (int, int, bool) {
	total, reference := 0, 0
	for index, oldLine := range oldLines {
		oldKey := patchDiagnosticLineKey(oldLine)
		actualKey := patchDiagnosticLineKey(actualLines[index])
		limit := patchDiagnosticLineDistanceLimit(max(len(oldKey), len(actualKey)))
		distance := patchDiagnosticDistance(oldKey, actualKey, limit)
		if distance > limit {
			return 0, 0, false
		}
		total += distance
		reference += max(len(oldKey), len(actualKey))
	}
	return total, reference, true
}

func patchDiagnosticCandidateAt(content string, contentLines, oldLines []patchLine, start int) patchDiagnosticCandidate {
	first := contentLines[start]
	last := contentLines[start+len(oldLines)-1]
	end := last.end()
	// An old without a final newline must not swallow the file's actual trailing
	// newline, or the model would join the next line.
	if oldLines[len(oldLines)-1].ending == "" {
		end -= len(last.ending)
	}
	candidate := patchDiagnosticCandidate{
		startLine: start + 1,
		endLine:   start + len(oldLines),
		block:     content[first.start:end],
	}
	for index, oldLine := range oldLines {
		line := contentLines[start+index]
		if line.text != oldLine.text {
			candidate.diffLines = append(candidate.diffLines, patchDiagnosticDiffLine{
				number: start + index + 1,
				raw:    content[line.start:line.end()],
			})
		}
	}
	return candidate
}

// patchDiagnosticLineKey compares lines without edge whitespace so indentation
// and stray-space mismatches still surface, while a single "\n" marker keeps
// LF/CRLF/CR topology differences from reading as text differences.
func patchDiagnosticLineKey(line patchLine) string {
	key := strings.TrimSpace(line.text)
	if line.ending != "" {
		key += "\n"
	}
	return key
}

func patchDiagnosticAnchor(lines []patchLine) int {
	best := 0
	bestLength := len(patchDiagnosticLineKey(lines[0]))
	for index := 1; index < len(lines); index++ {
		if length := len(patchDiagnosticLineKey(lines[index])); length > bestLength {
			best, bestLength = index, length
		}
	}
	return best
}

func patchDiagnosticLineDistanceLimit(length int) int {
	return min(max(length/4, 1), 8)
}

// Suggestions require small measured distances; similarity never authorizes a
// write or proves that the suggested location is the intended target.
func patchDiagnosticTrusted(distance, reference int) bool {
	return distance <= 1 || distance*5 <= reference
}

// patchDiagnosticFitDiffLines appends real differing lines under head. An
// over-long line is quoted as a labelled excerpt, never a complete old value.
func patchDiagnosticFitDiffLines(head string, diffLines []patchDiagnosticDiffLine) (string, bool) {
	var builder strings.Builder
	builder.WriteString(head)
	added := 0
	for _, line := range diffLines {
		if added >= patchDiagnosticMaxDiffLines {
			break
		}
		var entry string
		if len(line.raw) <= patchDiagnosticDiffLineBytes {
			entry = fmt.Sprintf("\nline %d: %s", line.number, patchDiagnosticQuote(line.raw))
		} else {
			entry = fmt.Sprintf("\nline %d (truncated excerpt): %s", line.number, patchDiagnosticQuote(patchDiagnosticTruncate(line.raw, patchDiagnosticDiffLineBytes)))
		}
		if builder.Len()+len(entry) > patchDiagnosticHintBudget {
			break
		}
		builder.WriteString(entry)
		added++
	}
	if added == 0 {
		return "", false
	}
	return builder.String(), true
}

// patchDiagnosticLineColumn maps a source byte offset to its 1-based line and
// rune column. Scan only the prefix: early matches must not index the file tail.
func patchDiagnosticLineColumn(content string, offset int) (int, int) {
	line, lineStart := 1, 0
	for i := 0; i < offset; i++ {
		// CRLF counts as one break, only after its LF has been consumed.
		if content[i] == '\n' || content[i] == '\r' && (i+1 == len(content) || content[i+1] != '\n') {
			line, lineStart = line+1, i+1
		}
	}
	return line, utf8.RuneCountInString(content[lineStart:offset]) + 1
}

func patchDiagnosticLinePhrase(start, end int) string {
	if start == end {
		return fmt.Sprintf("line %d", start)
	}
	return fmt.Sprintf("lines %d-%d", start, end)
}

// patchDiagnosticLineNumber maps a source byte offset to its 1-based line,
// counting LF, CRLF and lone CR as single line breaks.
func patchDiagnosticLineNumber(content string, offset int) int {
	line, _ := patchDiagnosticLineColumn(content, offset)
	return line
}

func patchDiagnosticQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func patchDiagnosticTruncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "")
}

// patchDiagnosticDistance is a distance-limited Levenshtein distance: it
// returns limit+1 once the limit is exceeded. A fixed-width band bounds time to
// O(input length * limit), with linear memory.
func patchDiagnosticDistance(a, b string, limit int) int {
	if a == b {
		return 0
	}
	if len(a) < len(b) {
		a, b = b, a
	}
	if len(a)-len(b) > limit {
		return limit + 1
	}
	if len(b) == 0 {
		return min(len(a), limit+1)
	}
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		lo, hi := max(1, i-limit), min(len(b), i+limit)
		current[0] = i
		if lo > 1 {
			current[lo-1] = limit + 1
		}
		rowMin := limit + 1
		for j := lo; j <= hi; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			value := min(previous[j-1]+cost, previous[j]+1, current[j-1]+1)
			current[j] = value
			rowMin = min(rowMin, value)
		}
		if hi < len(b) {
			current[hi+1] = limit + 1
		}
		if rowMin > limit {
			return limit + 1
		}
		previous, current = current, previous
	}
	return min(previous[len(b)], limit+1)
}
