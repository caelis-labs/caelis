package filesystem

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestPatchToolWhitespaceFallback(t *testing.T) {
	tests := []struct {
		name, before, old, new, want string
	}{
		{
			name:   "missing indentation preserves Python context",
			before: "def enabled():\n    if ready:\n        return True\n    return False\n",
			old:    "   if ready:\n       return True\n", new: "   if ready:\n       return False\n",
			want: "def enabled():\n    if ready:\n        return False\n    return False\n",
		},
		{
			name:   "extra indentation",
			before: "if ready:\n    return True\n", old: "  if ready:\n      return True\n",
			new: "  if ready:\n      return False\n", want: "if ready:\n    return False\n",
		},
		{
			name:   "trailing whitespace and untouched Unicode",
			before: "title = '“世界”'  \nif ready: \t\n    return True\t \n# untouched  \n",
			old:    "if ready:\n    return True\n", new: "if ready:\n    return False\n",
			want: "title = '“世界”'  \nif ready: \t\n    return False\t \n# untouched  \n",
		},
		{
			name:   "mixed endings and whitespace-only context",
			before: "HEAD\r\n    if ready:\r\n \t\r        return True  \nTAIL\r",
			old:    "   if ready:\n\n       return True\n", new: "   if ready:\n\n       return False\n",
			want: "HEAD\r\n    if ready:\r\n \t\r        return False  \nTAIL\r",
		},
		{
			name:   "no final newline",
			before: "    if ready:\n        return True  ",
			old:    "   if ready:\n       return True", new: "   if ready:\n       return False",
			want: "    if ready:\n        return False  ",
		},
		{
			name:   "block without terminal newline leaves file newline outside range",
			before: "    if ready:\r\n        return True\r\nTAIL\r\n",
			old:    "   if ready:\n       return True", new: "   if ready:\n       return False",
			want: "    if ready:\r\n        return False\r\nTAIL\r\n",
		},
		{
			name:   "literal tab prefix",
			before: "\tif ready:\n\t\treturn True\n", old: "if ready:\n\treturn True\n",
			new: "if ready:\n\treturn False\n", want: "\tif ready:\n\t\treturn False\n",
		},
		{
			name:   "final whitespace-only pattern line maps to an empty physical line",
			before: " a\n b\n\n", old: "a\nb\n ", new: "a\nB\n ", want: " a\n B\n\n",
		},
		{
			name:   "exact match wins over whitespace alternatives",
			before: "if ready:\n    return True\nif ready:  \n    return True \n",
			old:    "if ready:\n    return True\n", new: "if ready:\n    return False\n",
			want: "if ready:\n    return False\nif ready:  \n    return True \n",
		},
		{
			name:   "line-ending match wins over whitespace alternatives",
			before: "if ready:\r\n    return True\r\nif ready:  \n    return True \n",
			old:    "if ready:\n    return True\n", new: "if ready:\n    return False\n",
			want: "if ready:\r\n    return False\r\nif ready:  \n    return True \n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "target.txt")
			if err := os.WriteFile(path, []byte(test.before), 0o644); err != nil {
				t.Fatal(err)
			}
			payload := runPatch(t, newTestPatchTool(t, dir), map[string]any{
				"path": "target.txt", "edits": []map[string]any{{"old": test.old, "new": test.new}},
			})
			if got := readTestFile(t, path); got != test.want {
				t.Fatalf("content = %q, want %q", got, test.want)
			}
			if payload["replacements"] != float64(1) || payload["changed"] != true {
				t.Fatalf("payload = %#v", payload)
			}
		})
	}
}

func TestPatchToolWhitespaceFallbackRejectsUnsafeEdits(t *testing.T) {
	const old = "   if ready:\n       return True\n"
	const next = "   if ready:\n       return False\n"
	tests := []struct {
		name, before, old, new string
		replaceAll             bool
		code                   tool.ErrorCode
	}{
		{name: "different indentation candidates", before: "    if ready:\n        return True\n  if ready:\n      return True\n", old: old, new: next, code: tool.ErrorCodeTooManyMatches},
		{name: "trailing whitespace candidates", before: "   if ready: \n       return True\n   if ready:\n       return True  \n", old: old, new: next, code: tool.ErrorCodeTooManyMatches},
		{name: "inconsistent indentation", before: "    if ready:\n         return True\n", old: old, new: next},
		{name: "tab is not spaces", before: "\tif ready:\n\t    return True\n", old: old, new: next},
		{name: "inline string whitespace", before: "    if ready:\n        return 'a  b'\n", old: "   if ready:\n       return 'a b'\n", new: next},
		{name: "Unicode quote", before: "    if ready:\n        return '“yes”'\n", old: "   if ready:\n       return '\"yes\"'\n", new: next},
		{name: "missing operator", before: "    if !ready:\n        return True\n", old: old, new: next},
		{name: "blank line removed", before: "    if ready:\n\n        return True\n", old: old, new: next},
		{name: "partial last line", before: "    if ready:\n        return TrueValue\n", old: old, new: next},
		{name: "partial first line", before: "prefix if ready:\n        return True\n", old: old, new: next},
		{name: "missing file final newline", before: "    if ready:\n        return True", old: old, new: next},
		{name: "single nonblank line", before: "\n    return True  \n", old: "\n   return True\n", new: "\n   return False\n"},
		{name: "replace_all does not enable fallback", before: "    if ready:\n        return True\n", old: old, new: next, replaceAll: true},
		{name: "new changes indentation", before: "    if ready:\n        return True\n", old: old, new: "   if ready:\n        return False\n"},
		{name: "new changes trailing spaces", before: "    if ready:\n        return True\n", old: old, new: "   if ready: \n       return False\n"},
		{name: "new inserts line", before: "    if ready:\n        return True\n", old: old, new: "   if ready:\n       log()\n       return True\n"},
		{name: "new removes line", before: "    if ready:\n        return True\n", old: old, new: "   if ready:\n"},
		{name: "new removes final newline", before: "    if ready:\n        return True\n", old: old, new: strings.TrimSuffix(next, "\n")},
		{name: "new empties line", before: "    if ready:\n        return True\n", old: old, new: "   if ready:\n       \n"},
		{name: "new equals old", before: "    if ready:\n        return True\n", old: old, new: old},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "target.txt")
			if err := os.WriteFile(path, []byte(test.before), 0o644); err != nil {
				t.Fatal(err)
			}
			err := callPatch(newTestPatchTool(t, dir), map[string]any{
				"path": "target.txt", "edits": []map[string]any{{"old": test.old, "new": test.new, "replace_all": test.replaceAll}},
			})
			code := test.code
			if code == "" {
				code = tool.ErrorCodeOldTextNotFound
			}
			requireToolErrorCode(t, err, code)
			if got := readTestFile(t, path); got != test.before {
				t.Fatalf("content changed after failure: %q", got)
			}
		})
	}
}

func TestPatchToolWhitespaceFallbackBatch(t *testing.T) {
	const before = "HEADER\n    if ready:\n        return True\nTAIL\n"
	for _, test := range []struct {
		name, otherOld, want string
		code                 tool.ErrorCode
	}{
		{name: "success", otherOld: "HEADER", want: "TITLE\n    if ready:\n        return False\nTAIL\n"},
		{name: "not found rolls back entire batch", otherOld: "MISSING", code: tool.ErrorCodeOldTextNotFound},
		{name: "normalized overlap rolls back entire batch", otherOld: "return True", code: tool.ErrorCodeInvalidInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "target.txt")
			if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
				t.Fatal(err)
			}
			err := callPatch(newTestPatchTool(t, dir), map[string]any{
				"path": "target.txt", "edits": []map[string]any{
					{"old": test.otherOld, "new": "TITLE"},
					{"old": "   if ready:\n       return True\n", "new": "   if ready:\n       return False\n"},
				},
			})
			want := test.want
			if test.code != "" {
				requireToolErrorCode(t, err, test.code)
				want = before
			} else if err != nil {
				t.Fatal(err)
			}
			if got := readTestFile(t, path); got != want {
				t.Fatalf("content = %q, want %q", got, want)
			}
		})
	}
}

func TestPatchToolRejectsOverlappingOccurrences(t *testing.T) {
	for _, replaceAll := range []bool{false, true} {
		dir := t.TempDir()
		path := filepath.Join(dir, "target.txt")
		if err := os.WriteFile(path, []byte("ababa"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := callPatch(newTestPatchTool(t, dir), map[string]any{
			"path": "target.txt", "edits": []map[string]any{{"old": "aba", "new": "X", "replace_all": replaceAll}},
		})
		want := "ababa"
		if replaceAll {
			if err != nil {
				t.Fatal(err)
			}
			want = "Xba"
		} else {
			requireToolErrorCode(t, err, tool.ErrorCodeTooManyMatches)
		}
		if got := readTestFile(t, path); got != want {
			t.Fatalf("replace_all=%v: content = %q, want %q", replaceAll, got, want)
		}
	}
}

func TestWhitespacePatchSearchBudgetDoesNotAcceptPartialSearch(t *testing.T) {
	old := strings.Repeat("alpha\n", 101)
	invalid := strings.Repeat(" alpha\n", 100) + "  alpha\n"
	valid := strings.Repeat(" alpha\n", 101)
	content := valid + "separator\n" + strings.Repeat(invalid, 1000)
	if len(content) > patchWhitespaceMaxFileBytes {
		t.Fatal("fixture must exercise comparison budget, not file size limit")
	}
	matches, complete := whitespacePatchMatchRanges(content, old)
	if complete || len(matches) != 0 {
		t.Fatalf("matches = %#v, complete = %v; incomplete search must discard its candidate", matches, complete)
	}
}

func TestPatchWhitespaceFileLimitKeepsExactAndLineEndingMatches(t *testing.T) {
	prefix := strings.Repeat("padding\n", patchWhitespaceMaxFileBytes/8+1)
	content := prefix + "alpha\r\nbeta\r\n"
	for _, old := range []string{"alpha\r\nbeta\r\n", "alpha\nbeta\n"} {
		replacements, err := collectPatchReplacements(content, []patchEdit{{old: old, new: "one\r\ntwo\r\n"}})
		if err != nil {
			t.Fatal(err)
		}
		if got := applyPatchReplacements(content, replacements); got != prefix+"one\r\ntwo\r\n" {
			t.Fatal("size limit changed existing exact or line-ending behavior")
		}
	}
	_, err := collectPatchReplacements(content, []patchEdit{{old: " alpha\n beta\n", new: " one\n two\n"}})
	requireToolErrorCode(t, err, tool.ErrorCodeOldTextNotFound)
}

func BenchmarkWhitespacePatchLineBoundaries(b *testing.B) {
	content := strings.Repeat(" ba\n", patchWhitespaceMaxFileBytes/4)
	for _, length := range []int{1000, 20000} {
		old := "a\n" + strings.Repeat("ba\n", length)
		b.Run(fmt.Sprintf("old-lines-%d", length), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				matches, complete := whitespacePatchMatchRanges(content, old)
				if !complete || len(matches) != 0 {
					b.Fatalf("inline suffix must not be a candidate: %#v, %v", matches, complete)
				}
			}
		})
	}
}

func FuzzPatchWhitespacePreservesOriginalMargins(f *testing.F) {
	f.Add("世界", "new", "  ", " \t", "\r\n")
	f.Add("old", "next", "\t", " ", "\n")
	f.Fuzz(func(t *testing.T, oldBody, newBody, prefix, tail, eol string) {
		if oldBody == newBody || oldBody == "" || newBody == "" || len(oldBody)+len(newBody) > 4096 ||
			strings.ContainsAny(oldBody+newBody, "\r\n") || strings.Trim(oldBody, " \t") != oldBody || strings.Trim(newBody, " \t") != newBody ||
			strings.Trim(prefix+tail, " \t") != "" || len(prefix+tail) > 32 || (eol != "\r\n" && eol != "\n" && eol != "\r") {
			return
		}
		old := "if ready:\n    " + oldBody + "\n"
		next := "if ready:\n    " + newBody + "\n"
		actual := prefix + "if ready:" + tail + eol + prefix + "    " + oldBody + tail + eol
		want := prefix + "if ready:" + tail + eol + prefix + "    " + newBody + tail + eol
		got, ok := preservePatchWhitespace(actual, old, next)
		if !ok || got != want {
			t.Fatalf("preserved = %q, %v; want %q", got, ok, want)
		}
	})
}

func TestPatchToolWhitespaceDiffUsesActualBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feature.py")
	const before = "def enabled():\n    if ready:\n        return True  \n    return False\n"
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	input := `{"path":"feature.py","edits":[{"old":"   if ready:\n       return True\n","new":"   if ready:\n       return False\n"}]}`
	result, err := newTestPatchTool(t, dir).Call(context.Background(), tool.Call{Input: json.RawMessage(input)})
	if err != nil {
		t.Fatal(err)
	}
	hunks := mutationToolMetadata(result.Metadata)["diff_hunks"]
	encoded, err := json.Marshal(hunks)
	if err != nil {
		t.Fatal(err)
	}
	const want = `[{"header":"@@ -1,4 +1,4 @@","old_start":1,"old_lines":4,"new_start":1,"new_lines":4,"lines":[" def enabled():","     if ready:","-        return True  ","+        return False  ","     return False"]}]`
	if string(encoded) != want {
		t.Fatalf("diff_hunks = %s, want %s", encoded, want)
	}
}

func FuzzWhitespacePatchRanges(f *testing.F) {
	f.Add("    if ready:\r\n        return True  \n", "   if ready:\n       return True\n")
	f.Add("a \nb \na \nb \n", "a\nb")
	f.Add("a\r\n \t\rb\n", " a\n\n b\n")
	f.Add(" a\n b\n\n", "a\nb\n ")
	f.Fuzz(func(t *testing.T, content, old string) {
		if len(content)+len(old) > 4096 {
			return
		}
		matches, complete := whitespacePatchMatchRanges(content, old)
		if !complete {
			t.Fatal("small search exceeded budget")
		}
		for _, match := range matches {
			if match.start < 0 || match.end > len(content) || match.start >= match.end || patchRangeSplitsCRLF(content, match.start, match.end) {
				t.Fatalf("invalid range: %#v", match)
			}
			actualLines, oldLines := patchLines(content[match.start:match.end]), patchLines(old)
			if len(actualLines)+1 == len(oldLines) && match.end < len(content) && strings.ContainsAny(content[match.end:match.end+1], "\r\n") {
				actualLines = append(actualLines, patchLine{})
			}
			if len(actualLines) != len(oldLines) {
				t.Fatalf("line count changed: %#v", match)
			}
			for i, actual := range actualLines {
				if strings.Trim(actual.text, " \t") != strings.Trim(oldLines[i].text, " \t") || (actual.ending == "") != (oldLines[i].ending == "") {
					t.Fatalf("match changed line body or newline topology: %#v", match)
				}
			}
		}
	})
}

func TestPatchWhitespaceErrorPayloadFitsDiagnosticBudget(t *testing.T) {
	_, err := whitespacePatchReplacement("value\n", patchEdit{old: strings.Repeat("x", patchWhitespaceMaxOldBytes+1), new: "next"}, 2)
	requireToolErrorCode(t, err, tool.ErrorCodeOldTextNotFound)
	payload, marshalErr := json.Marshal(tool.ErrorPayload(err))
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	const want = `{"error":"Patch edit 2 exceeded the whitespace search limit; no edits written","error_code":"old_text_not_found","retryable":true,"system_hint":"Retry with exact old text, including whitespace."}`
	if string(payload) != want {
		t.Fatalf("payload = %s, want %s", payload, want)
	}
}
