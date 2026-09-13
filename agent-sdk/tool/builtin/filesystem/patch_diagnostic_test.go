package filesystem

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestPatchDiagnosticNotFoundEmbedsCopyableActualText(t *testing.T) {
	content := "def enabled():\n    if ready:\n        return True\n    return False\n"
	old := "        return Tru\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "feature.py")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	patchTool := newTestPatchTool(t, dir)

	// The first failure must come from the production entry point, not a direct
	// diagnostic call, so this exercises the whole retry chain.
	firstErr := callPatch(patchTool, map[string]any{
		"path": "feature.py",
		"edits": []map[string]any{
			{"old": old, "new": "        return False\n"},
		},
	})
	requireToolErrorCode(t, firstErr, tool.ErrorCodeOldTextNotFound)

	toolErr := requireToolError(t, firstErr)
	if !toolErr.Retryable {
		t.Fatalf("Retryable = false, want true")
	}
	if !strings.Contains(toolErr.Message, "no edits written") {
		t.Fatalf("message = %q, want no-edit guarantee", toolErr.Message)
	}
	if strings.Contains(toolErr.Hint, "no edits written") || strings.Contains(toolErr.Hint, "no changes were written") {
		t.Fatalf("hint repeats the message guarantee: %q", toolErr.Hint)
	}
	if !strings.Contains(toolErr.Hint, "Possible match at line 3") {
		t.Fatalf("hint = %q, want possible-match location", toolErr.Hint)
	}
	if !strings.Contains(toolErr.Hint, patchDiagnosticIndentNote) {
		t.Fatalf("hint = %q, want indentation guidance next to echoed block", toolErr.Hint)
	}
	if got := readTestFile(t, path); got != content {
		t.Fatalf("failed PATCH changed the file: %q", got)
	}

	// The model only sees the serialized payload; the suggested old must come
	// from there and be usable exactly as-is in a strict retry.
	payload := tool.ErrorPayload(firstErr)
	visible, ok := payload["system_hint"].(string)
	if !ok || visible != toolErr.Hint {
		t.Fatalf("system_hint = %v, want %q", payload["system_hint"], toolErr.Hint)
	}
	suggested := patchDiagnosticSuggestedText(t, visible)
	if want := "        return True\n"; suggested != want {
		t.Fatalf("suggested old = %q, want exact actual block %q", suggested, want)
	}

	runPatch(t, patchTool, map[string]any{
		"path": "feature.py",
		"edits": []map[string]any{
			{"old": suggested, "new": "        return False\n"},
		},
	})
	if got, want := readTestFile(t, path), "def enabled():\n    if ready:\n        return False\n    return False\n"; got != want {
		t.Fatalf("patched content = %q, want %q", got, want)
	}
}

// A suggested block for an old without a final newline must not swallow the
// file's trailing newline, or the model's new would join the next line.
func TestPatchDiagnosticNotFoundKeepsTrailingLineBoundary(t *testing.T) {
	content := "first line\nsecond line\nthird line\n"
	old := "second lin"

	err := patchNotFoundError(content, old, 0)
	requireToolErrorCode(t, err, tool.ErrorCodeOldTextNotFound)

	toolErr := requireToolError(t, err)
	suggested := patchDiagnosticSuggestedText(t, toolErr.Hint)
	if want := "second line"; suggested != want {
		t.Fatalf("suggested old = %q, want %q (no trailing newline)", suggested, want)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "lines.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	patchTool := newTestPatchTool(t, dir)
	runPatch(t, patchTool, map[string]any{
		"path": "lines.txt",
		"edits": []map[string]any{
			{"old": suggested, "new": "SECOND line"},
		},
	})
	if got, want := readTestFile(t, path), "first line\nSECOND line\nthird line\n"; got != want {
		t.Fatalf("patched content = %q, want neighbors untouched: %q", got, want)
	}
}

func TestPatchDiagnosticNotFoundHandlesMissingSpace(t *testing.T) {
	content := "x = 1  # note\nnext line\n"
	old := "x = 1 # note\n"

	err := patchNotFoundError(content, old, 3)
	requireToolErrorCode(t, err, tool.ErrorCodeOldTextNotFound)

	toolErr := requireToolError(t, err)
	if got, want := patchDiagnosticSuggestedText(t, toolErr.Hint), "x = 1  # note\n"; got != want {
		t.Fatalf("suggested old = %q, want %q", got, want)
	}
}

func TestPatchDiagnosticNotFoundWithoutCandidateStaysShort(t *testing.T) {
	content := "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	old := "zzzzzzzzzzzzzzzzzzzz\n"

	err := patchNotFoundError(content, old, 0)
	requireToolErrorCode(t, err, tool.ErrorCodeOldTextNotFound)

	toolErr := requireToolError(t, err)
	if toolErr.Hint != patchDiagnosticReadAction {
		t.Fatalf("hint = %q, want read action", toolErr.Hint)
	}
	if len(toolErr.Hint) > 80 {
		t.Fatalf("hint bytes = %d, want <= 80", len(toolErr.Hint))
	}
	if !strings.Contains(toolErr.Message, "no edits written") {
		t.Fatalf("message = %q, want no-edit guarantee", toolErr.Message)
	}
}

// A tiny old cannot support a meaningful near-match, so no candidate is offered.
func TestPatchDiagnosticNotFoundIgnoresTooShortOld(t *testing.T) {
	err := patchNotFoundError("x\n", "y\n", 0)
	requireToolErrorCode(t, err, tool.ErrorCodeOldTextNotFound)

	toolErr := requireToolError(t, err)
	if toolErr.Hint != patchDiagnosticReadAction {
		t.Fatalf("hint = %q, want read action for a one-character old", toolErr.Hint)
	}
}

func TestPatchDiagnosticNotFoundKeepsLocationForOversizedBlock(t *testing.T) {
	var content strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&content, "item %02d: alpha beta gamma delta epsilon\n", i)
	}
	body := content.String()
	target := fmt.Sprintf("item %02d: alpha beta gamma delta epsilon\n", 20)
	old := strings.Replace(body, target, fmt.Sprintf("item %02d alpha beta gamma delta epsilon\n", 20), 1)

	err := patchNotFoundError(body, old, 1)
	requireToolErrorCode(t, err, tool.ErrorCodeOldTextNotFound)

	toolErr := requireToolError(t, err)
	if len(toolErr.Hint) > patchDiagnosticHintBudget {
		t.Fatalf("hint bytes = %d, want <= %d", len(toolErr.Hint), patchDiagnosticHintBudget)
	}
	if !strings.Contains(toolErr.Hint, "too large to embed") {
		t.Fatalf("hint = %q, want oversized-block disclosure", toolErr.Hint)
	}
	if !strings.Contains(toolErr.Hint, "line 21:") {
		t.Fatalf("hint = %q, want differing actual line", toolErr.Hint)
	}
	if strings.Contains(toolErr.Hint, patchDiagnosticQuote(body)) {
		t.Fatalf("hint embedded the whole oversized block")
	}
}

// Localization must compare raw line text, so a block whose only difference is
// whitespace still reports where it is, and an over-long line is marked as an
// excerpt rather than a complete copyable value.
func TestPatchDiagnosticDiffLinesReportWhitespaceOnlyChange(t *testing.T) {
	var content strings.Builder
	for i := 0; i < 40; i++ {
		if i == 20 {
			content.WriteString("  long " + strings.Repeat("word ", 60) + "tail   \n")
			continue
		}
		fmt.Fprintf(&content, "item %02d: alpha beta gamma delta\n", i)
	}
	body := content.String()
	old := strings.Replace(body, "tail   \n", "tail\n", 1)

	err := patchNotFoundError(body, old, 0)
	requireToolErrorCode(t, err, tool.ErrorCodeOldTextNotFound)

	toolErr := requireToolError(t, err)
	if !strings.Contains(toolErr.Hint, "line 21") {
		t.Fatalf("hint = %q, want whitespace-only difference localized", toolErr.Hint)
	}
	if !strings.Contains(toolErr.Hint, "truncated excerpt") {
		t.Fatalf("hint = %q, want truncated excerpt label", toolErr.Hint)
	}
	if strings.Contains(toolErr.Hint, patchDiagnosticQuote(body)) {
		t.Fatalf("hint embedded the whole oversized block")
	}
	if len(toolErr.Hint) > patchDiagnosticHintBudget {
		t.Fatalf("hint bytes = %d, want <= %d", len(toolErr.Hint), patchDiagnosticHintBudget)
	}
}

func TestPatchDiagnosticAmbiguousListsStartPositions(t *testing.T) {
	content := "x + x\nmid\nx\nx\n"
	matches := []patchMatchRange{
		{start: 0, end: 1},
		{start: 4, end: 5},
		{start: 10, end: 11},
		{start: 12, end: 13},
	}

	err := patchAmbiguousError(content, matches, 2)
	requireToolErrorCode(t, err, tool.ErrorCodeTooManyMatches)

	toolErr := requireToolError(t, err)
	if !toolErr.Retryable {
		t.Fatalf("Retryable = false, want true")
	}
	if got, want := toolErr.Message, "Patch edit 2 is ambiguous; no edits written"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
	if strings.Contains(toolErr.Message, "locations") {
		t.Fatalf("message = %q, must not claim a match total", toolErr.Message)
	}
	// Same-line candidates are listed as distinct rune columns, capped at three.
	hint := toolErr.Hint
	if want := "Matches at line:column 1:1, 1:5, 3:1; add distinguishing context to old."; hint != want {
		t.Fatalf("hint = %q, want %q", hint, want)
	}
	if strings.Contains(hint, "replace_all") {
		t.Fatalf("hint = %q, must not recommend replace_all", hint)
	}
	if len(hint) > 100 {
		t.Fatalf("hint bytes = %d, want <= 100", len(hint))
	}
}

func TestPatchDiagnosticLineColumn(t *testing.T) {
	content := "éx\r\n中\rz\n\nq"
	for _, tc := range []struct{ offset, line, column int }{
		{0, 1, 1}, {2, 1, 2}, {3, 1, 3}, {4, 1, 4},
		{5, 2, 1}, {8, 2, 2}, {9, 3, 1}, {10, 3, 2},
		{11, 4, 1}, {12, 5, 1},
	} {
		line, column := patchDiagnosticLineColumn(content, tc.offset)
		if line != tc.line || column != tc.column {
			t.Errorf("offset %d = %d:%d, want %d:%d", tc.offset, line, column, tc.line, tc.column)
		}
	}
}

func TestPatchDiagnosticLineColumnDoesNotAllocate(t *testing.T) {
	content := "x x\n" + strings.Repeat("\n", 32<<10)
	var line, column int
	allocs := testing.AllocsPerRun(3, func() {
		line, column = patchDiagnosticLineColumn(content, 2)
	})
	if line != 1 || column != 3 {
		t.Fatalf("position = %d:%d, want 1:3", line, column)
	}
	if allocs != 0 {
		t.Fatalf("position lookup allocated %.0f objects, want no line index", allocs)
	}
}

func BenchmarkPatchAmbiguousDiagnostic(b *testing.B) {
	for _, size := range []int{256 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			// Both anchors are on the first line. The unrelated tail must not
			// add diagnostic work beyond the tool's existing file read.
			content := "x x\n" + strings.Repeat("\n", size-4)
			dir := b.TempDir()
			path := filepath.Join(dir, "many-lines.txt")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				b.Fatal(err)
			}
			patchTool, err := NewPatch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
			if err != nil {
				b.Fatal(err)
			}
			args := map[string]any{
				"path":  "many-lines.txt",
				"edits": []map[string]any{{"old": "x", "new": "y"}},
			}
			b.ReportAllocs()
			for b.Loop() {
				err = callPatch(patchTool, args)
			}
			var toolErr *tool.ToolError
			if !errors.As(err, &toolErr) || toolErr.Code != tool.ErrorCodeTooManyMatches {
				b.Fatalf("error = %v, want ambiguous match", err)
			}
			if want := "Matches at line:column 1:1, 1:3; add distinguishing context to old."; toolErr.Hint != want {
				b.Fatalf("hint = %q, want %q", toolErr.Hint, want)
			}
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != content {
				b.Fatalf("ambiguous edit changed the file: %v", err)
			}
		})
	}
}

func TestPatchDiagnosticUnsafeEchoesExactBlock(t *testing.T) {
	content := "alpha\r\nbeta\r\ngamma\r\n"
	match := patchMatchRange{start: 0, end: 13, normalizedLineEndings: true}

	err := patchUnsafeMatchError(content, match, 1)
	requireToolErrorCode(t, err, tool.ErrorCodeOldTextNotFound)

	toolErr := requireToolError(t, err)
	if !toolErr.Retryable {
		t.Fatalf("Retryable = false, want true")
	}
	if !strings.Contains(toolErr.Message, "whitespace repair") || !strings.Contains(toolErr.Message, "no edits written") {
		t.Fatalf("message = %q, want repair reason and no-edit guarantee", toolErr.Message)
	}
	prefix := strings.SplitN(toolErr.Hint, "\n", 2)[0]
	if !strings.HasPrefix(prefix, "Possible match at lines 1-2") {
		t.Fatalf("hint prefix = %q, want location", prefix)
	}
	if len(prefix) > 120 {
		t.Fatalf("hint prefix bytes = %d, want <= 120", len(prefix))
	}
	if got, want := patchDiagnosticSuggestedText(t, toolErr.Hint), "alpha\r\nbeta\r\n"; got != want {
		t.Fatalf("echoed block = %q, want %q", got, want)
	}
}

func TestPatchDiagnosticCRLFAndUnicodePreserved(t *testing.T) {
	content := "héllo wörld\r\nsecond line ✓\r\nthird line\r\n"
	old := "héllo wörld\r\nsecond lin ✓\r\n"

	err := patchNotFoundError(content, old, 0)
	requireToolErrorCode(t, err, tool.ErrorCodeOldTextNotFound)

	toolErr := requireToolError(t, err)
	suggested := patchDiagnosticSuggestedText(t, toolErr.Hint)
	if want := "héllo wörld\r\nsecond line ✓\r\n"; suggested != want {
		t.Fatalf("suggested old = %q, want %q", suggested, want)
	}
	if !utf8.ValidString(suggested) {
		t.Fatalf("suggested old is not valid UTF-8: %q", suggested)
	}
	if !strings.Contains(suggested, "\r\n") {
		t.Fatalf("suggested old lost CRLF line endings: %q", suggested)
	}
}

func TestPatchDiagnosticHintsStayWithinBudget(t *testing.T) {
	cases := []struct {
		name    string
		content string
		old     string
	}{
		{"unicode-crlf", "héllo wörld\r\nsecond line ✓\r\nthird line\r\n", "héllo wörld\r\nsecond lin ✓\r\n"},
		{"many-lines", strings.Repeat("padding line with words\n", 400), "padding line wit words\n"},
		{"over-line-budget", strings.Repeat("x\n", 6000), "y\n"},
		{"over-old-budget", "candidate\n", strings.Repeat("candidate\n", patchDiagnosticMaxOldBytes)},
		{"escaped-long-line", strings.Repeat("\"\\", 600) + "x\n", strings.Repeat("\"\\", 600) + "y\n"},
		{"single-char", "x\n", "y\n"},
		{"unrelated", strings.Repeat("alpha beta gamma delta\n", 300), "zzzzzzzzzzzzzzzzzzzz\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unsafeEnd := min(4, len(tc.content))
			inputs := []error{
				patchNotFoundError(tc.content, tc.old, 0),
				patchAmbiguousError(tc.content, []patchMatchRange{{start: 0, end: 1}, {start: 0, end: 1}}, 0),
				patchUnsafeMatchError(tc.content, patchMatchRange{start: 0, end: unsafeEnd}, 0),
			}
			for index, err := range inputs {
				toolErr := requireToolError(t, err)
				if toolErr.Hint == "" {
					t.Fatalf("input %d: empty hint", index)
				}
				if len(toolErr.Hint) > patchDiagnosticHintBudget {
					t.Fatalf("input %d: hint bytes = %d, want <= %d: %q", index, len(toolErr.Hint), patchDiagnosticHintBudget, toolErr.Hint)
				}
				if !strings.Contains(toolErr.Message, "no edits written") {
					t.Fatalf("input %d: message = %q, want no-edit guarantee", index, toolErr.Message)
				}
				if !toolErr.Retryable {
					t.Fatalf("input %d: Retryable = false, want true", index)
				}
			}
		})
	}
}

func TestPatchDiagnosticDistanceIsBounded(t *testing.T) {
	cases := []struct {
		a, b  string
		limit int
		want  int
	}{
		{"abc", "abc", 2, 0},
		{"abc", "abd", 2, 1},
		{"abc", "ab", 2, 1},
		{"", "abc", 5, 3},
		{"abc", "", 5, 3},
		{"abcdef", "abcxef", 1, 1},
		{"abc", "xyz", 2, 3},
		{"a", "b", 0, 1},
		{"héllo", "hélao", 2, 1},
	}
	for _, tc := range cases {
		if got := patchDiagnosticDistance(tc.a, tc.b, tc.limit); got != tc.want {
			t.Fatalf("patchDiagnosticDistance(%q, %q, %d) = %d, want %d", tc.a, tc.b, tc.limit, got, tc.want)
		}
	}
}

// Golden check of the exact JSON the model sees for one failing edit.
func TestPatchDiagnosticErrorPayloadGolden(t *testing.T) {
	content := "def enabled():\n    if ready:\n        return True\n    return False\n"
	err := patchNotFoundError(content, "        return Tru\n", 0)

	encoded, marshalErr := json.Marshal(tool.ErrorPayload(err))
	if marshalErr != nil {
		t.Fatalf("Marshal() error = %v", marshalErr)
	}
	want := `{"error":"Patch edit 0 found no match for old; no edits written","error_code":"old_text_not_found","retryable":true,"system_hint":"Possible match at line 3; check it is the intended location:\n\"        return True\\n\"\nAlign unchanged whitespace in new with this old."}`
	if got := string(encoded); got != want {
		t.Fatalf("ErrorPayload = %s\nwant %s", got, want)
	}
}

func FuzzPatchDiagnosticDistance(f *testing.F) {
	f.Add("return Tru", "return True", uint8(2))
	f.Add("héllo", "hello", uint8(1))
	f.Add("abc", "xyz", uint8(0))
	f.Fuzz(func(t *testing.T, a, b string, limit uint8) {
		if len(a) > 32 || len(b) > 32 {
			return
		}
		bound := int(limit % 9)
		// An unbanded matrix is a small independent oracle for the diagnostic
		// algorithm's cutoff, especially insertion/deletion paths at band edges.
		matrix := make([][]int, len(a)+1)
		for i := range matrix {
			matrix[i] = make([]int, len(b)+1)
			matrix[i][0] = i
		}
		for j := range matrix[0] {
			matrix[0][j] = j
		}
		for i := 1; i <= len(a); i++ {
			for j := 1; j <= len(b); j++ {
				cost := 0
				if a[i-1] != b[j-1] {
					cost = 1
				}
				matrix[i][j] = min(matrix[i-1][j]+1, matrix[i][j-1]+1, matrix[i-1][j-1]+cost)
			}
		}
		want := min(matrix[len(a)][len(b)], bound+1)
		if got := patchDiagnosticDistance(a, b, bound); got != want {
			t.Fatalf("distance(%q, %q, %d) = %d, want %d", a, b, bound, got, want)
		}
	})
}

func requireToolError(t *testing.T, err error) *tool.ToolError {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want ToolError")
	}
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("error = %T %v, want ToolError", err, err)
	}
	return toolErr
}

// patchDiagnosticSuggestedText extracts the JSON-escaped actual text a hint
// tells the model to reuse as old, and proves it round-trips as a JSON string.
func patchDiagnosticSuggestedText(t *testing.T, hint string) string {
	t.Helper()
	for _, line := range strings.Split(hint, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "\"") {
			continue
		}
		var value string
		if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
			continue
		}
		return value
	}
	t.Fatalf("hint has no JSON-quoted actual text: %q", hint)
	return ""
}
