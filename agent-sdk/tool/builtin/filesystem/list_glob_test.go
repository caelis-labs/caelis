package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestGlobToolUsesFixedResultLimitAndMinimalPayload(t *testing.T) {
	dir := t.TempDir()
	for i := range globResultLimit + 1 {
		name := fmt.Sprintf("%03d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	globTool, err := NewGlob(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewGlob() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{"pattern": "*.txt"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	result, err := globTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	if got, _ := payload["truncated"].(bool); !got {
		t.Fatalf("truncated = %v, want true", payload["truncated"])
	}
	if matches := stringSlicePayloadValue(t, payload["matches"]); len(matches) != globResultLimit {
		t.Fatalf("len(matches) = %d, want %d", len(matches), globResultLimit)
	}
	assertPayloadKeys(t, payload, "matches", "truncated")
}

func TestGlobToolSupportsPathForRelativePattern(t *testing.T) {
	dir := t.TempDir()
	demoDir := filepath.Join(dir, "demo")
	otherDir := filepath.Join(dir, "other")
	if err := os.MkdirAll(filepath.Join(demoDir, "nested"), 0o755); err != nil {
		t.Fatalf("MkdirAll(demo nested) error = %v", err)
	}
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(other) error = %v", err)
	}
	for _, name := range []string{
		filepath.Join(demoDir, "main.py"),
		filepath.Join(demoDir, "nested", "deep.py"),
		filepath.Join(otherDir, "main.py"),
	} {
		if err := os.WriteFile(name, []byte("print('ok')\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	globTool, err := NewGlob(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewGlob() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"pattern": "*.py",
		"path":    demoDir,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	result, err := globTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	assertPayloadKeys(t, payload, "matches", "truncated")
	matches := stringSlicePayloadValue(t, payload["matches"])
	if len(matches) != 1 || matches[0] != filepath.Join(demoDir, "main.py") {
		t.Fatalf("matches = %#v, want only demo/main.py", matches)
	}
	meta := filesystemToolMeta(t, result)
	if got, _ := meta["pattern"].(string); got != "*.py" {
		t.Fatalf("metadata pattern = %q, want original pattern", got)
	}
	if got, _ := meta["path"].(string); got != demoDir {
		t.Fatalf("metadata path = %q, want search directory", got)
	}
	if got, _ := meta["resolved_pattern"].(string); got != filepath.Join(demoDir, "*.py") {
		t.Fatalf("metadata resolved_pattern = %q, want path-resolved pattern", got)
	}
}

func TestGlobToolRejectsAbsolutePattern(t *testing.T) {
	dir := t.TempDir()
	globTool, err := NewGlob(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewGlob() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"pattern": filepath.Join(dir, "*.py"),
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	_, err = globTool.Call(context.Background(), tool.Call{Input: input})
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("Call() error = %T %v, want ToolError", err, err)
	}
	if toolErr.Code != tool.ErrorCodeInvalidInput {
		t.Fatalf("error code = %s, want %s: %v", toolErr.Code, tool.ErrorCodeInvalidInput, err)
	}
}

func TestGlobToolRejectsPatternEscapingPath(t *testing.T) {
	dir := t.TempDir()
	demoDir := filepath.Join(dir, "demo")
	if err := os.MkdirAll(demoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(demo) error = %v", err)
	}
	globTool, err := NewGlob(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewGlob() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"pattern": "../*.py",
		"path":    demoDir,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	_, err = globTool.Call(context.Background(), tool.Call{Input: input})
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("Call() error = %T %v, want ToolError", err, err)
	}
	if toolErr.Code != tool.ErrorCodeInvalidInput {
		t.Fatalf("error code = %s, want %s: %v", toolErr.Code, tool.ErrorCodeInvalidInput, err)
	}
}

func TestGlobToolRejectsUnsupportedExclude(t *testing.T) {
	dir := t.TempDir()
	globTool, err := NewGlob(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewGlob() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"pattern": "**/*.go",
		"exclude": "*_test.go",
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	_, err = globTool.Call(context.Background(), tool.Call{Input: input})
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != tool.ErrorCodeInvalidInput {
		t.Fatalf("Call() error = %T %v, want invalid_input", err, err)
	}
}

func TestGlobToolSupportsBraceExpansion(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"main.go", "main.md", "main.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	globTool, err := NewGlob(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewGlob() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"pattern": "main.{go,md}",
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	result, err := globTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	matches := stringSlicePayloadValue(t, payload["matches"])
	if len(matches) != 2 {
		t.Fatalf("matches = %#v, want go and md files", matches)
	}
	for _, match := range matches {
		switch filepath.Base(match) {
		case "main.go", "main.md":
		default:
			t.Fatalf("unexpected brace expansion match: %#v", matches)
		}
	}
}

func TestGlobToolMatchesRecursiveExtensionPattern(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatalf("MkdirAll(docs) error = %v", err)
	}
	for _, name := range []string{"root.txt", filepath.Join("docs", "nested.txt"), filepath.Join("docs", "nested.md")} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	globTool, err := NewGlob(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewGlob() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"pattern": "**/*.txt",
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	result, err := globTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	matches := stringSlicePayloadValue(t, payload["matches"])
	if len(matches) != 2 {
		t.Fatalf("matches = %#v, want root and nested txt files", matches)
	}
}

func TestGlobToolReturnsFilesOnlyAndExplainsEmptyResult(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("MkdirAll(nested) error = %v", err)
	}
	globTool, err := NewGlob(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewGlob() error = %v", err)
	}

	input, _ := json.Marshal(map[string]any{"pattern": "*"})
	result, err := globTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call(*) error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	if _, ok := payload["matches"].([]any); !ok {
		t.Fatalf("matches = %#v, want an empty JSON array", payload["matches"])
	}
	if matches := stringSlicePayloadValue(t, payload["matches"]); len(matches) != 0 {
		t.Fatalf("matches = %#v, want directories omitted", matches)
	}
	if got, _ := payload["message"].(string); got != `No files matched glob "*".` {
		t.Fatalf("message = %q, want explicit empty result", got)
	}
	assertPayloadKeys(t, payload, "matches", "truncated", "message")
}

func TestGlobToolRejectsMalformedPattern(t *testing.T) {
	dir := t.TempDir()
	globTool, err := NewGlob(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewGlob() error = %v", err)
	}
	input, _ := json.Marshal(map[string]any{"pattern": "[*.go"})

	_, err = globTool.Call(context.Background(), tool.Call{Input: input})
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != tool.ErrorCodeInvalidInput {
		t.Fatalf("Call() error = %T %v, want invalid_input", err, err)
	}
}

func TestSearchToolAlwaysTreatsPatternAsRegex(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"notes.txt": "plain content\n",
		"data.md":   "artifact.txt\nartifact.md\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	searchTool, err := NewSearch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}

	input, err := json.Marshal(map[string]any{
		"path":    ".",
		"pattern": ".txt$",
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	result, err := searchTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	assertPayloadKeys(t, payload, "hits", "truncated")
	hits, _ := payload["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("hits = %#v, want one regex hit", payload["hits"])
	}
	hit, _ := hits[0].(map[string]any)
	if got, _ := hit["path"].(string); filepath.Base(got) != "data.md" {
		t.Fatalf("hit path = %q, want data.md content match", got)
	}
	assertPayloadKeys(t, hit, "path", "line", "text")
	meta := filesystemToolMeta(t, result)
	if got, _ := meta["pattern"].(string); got != ".txt$" {
		t.Fatalf("metadata pattern = %q, want .txt$", got)
	}
	if got := numericMetaValue(meta["count"]); got != 1 {
		t.Fatalf("metadata count = %v, want 1", meta["count"])
	}
}

func TestSearchToolIncludeFiltersScannedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatalf("MkdirAll(docs) error = %v", err)
	}
	files := map[string]string{
		"notes.txt":                    "needle\n",
		filepath.Join("docs", "a.md"):  "needle\n",
		filepath.Join("docs", "b.txt"): "needle\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	searchTool, err := NewSearch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"path":    ".",
		"pattern": "needle",
		"include": "**/*.txt",
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	result, err := searchTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	hits, _ := payload["hits"].([]any)
	if len(hits) != 2 {
		t.Fatalf("hits = %#v, want two txt hits", payload["hits"])
	}
	for _, rawHit := range hits {
		hit, _ := rawHit.(map[string]any)
		path, _ := hit["path"].(string)
		if filepath.Ext(path) != ".txt" {
			t.Fatalf("SEARCH include returned non-txt hit: %#v", payload["hits"])
		}
	}
}

func TestSearchToolDistinguishesIncludeFilterEmptyFromContentEmpty(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"main.go":   "package main\nconst serviceTag = \"present\"\n",
		"query.sql": "select 'service_tag';\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	searchTool, err := NewSearch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}

	for _, test := range []struct {
		include    string
		suggestion string
	}{
		{include: ".go", suggestion: "*.go"},
		{include: "{go,sql}", suggestion: "*.{go,sql}"},
		{include: ".{go,sql}", suggestion: "*.{go,sql}"},
	} {
		t.Run(test.include, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{
				"path":    ".",
				"pattern": "service",
				"include": test.include,
			})
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			result, err := searchTool.Call(context.Background(), tool.Call{Input: input})
			if err != nil {
				t.Fatalf("Call() error = %v", err)
			}
			payload := filesystemToolPayload(t, result)
			if _, ok := payload["hits"].([]any); !ok {
				t.Fatalf("hits = %#v, want an empty JSON array", payload["hits"])
			}
			message, _ := payload["message"].(string)
			if !strings.Contains(message, test.suggestion) {
				t.Fatalf("message = %q, want suggestion %q", message, test.suggestion)
			}
			assertPayloadKeys(t, payload, "hits", "truncated", "message")
		})
	}

	input, err := json.Marshal(map[string]any{
		"path":    ".",
		"pattern": "missing",
		"include": "*.go",
	})
	if err != nil {
		t.Fatalf("Marshal(content empty) error = %v", err)
	}
	result, err := searchTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call(content empty) error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	if got, _ := payload["message"].(string); got != "No content matches found in the selected text files." {
		t.Fatalf("message = %q, want content-empty explanation", got)
	}

	input, err = json.Marshal(map[string]any{
		"path":    ".",
		"pattern": "service",
		"include": "*.go",
	})
	if err != nil {
		t.Fatalf("Marshal(valid extension glob) error = %v", err)
	}
	result, err = searchTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call(valid extension glob) error = %v", err)
	}
	payload = filesystemToolPayload(t, result)
	if hits, _ := payload["hits"].([]any); len(hits) != 1 {
		t.Fatalf("hits = %#v, want one Go hit", payload["hits"])
	}
	if _, ok := payload["message"]; ok {
		t.Fatalf("successful payload must not contain message: %#v", payload)
	}

	input, err = json.Marshal(map[string]any{
		"path":    ".",
		"pattern": "service",
		"include": "*.{go,sql}",
	})
	if err != nil {
		t.Fatalf("Marshal(valid brace glob) error = %v", err)
	}
	result, err = searchTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call(valid brace glob) error = %v", err)
	}
	payload = filesystemToolPayload(t, result)
	if hits, _ := payload["hits"].([]any); len(hits) != 2 {
		t.Fatalf("hits = %#v, want Go and SQL hits", payload["hits"])
	}
}

func TestSearchToolSkipsBinaryFilesAndLocalCrushCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".crush"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.crush) error = %v", err)
	}
	files := map[string][]byte{
		"main.go":                           []byte("package main\nconst needle = true\n"),
		"artifact.bin":                      []byte("needle\x00binary payload\n"),
		filepath.Join(".crush", "crush.db"): []byte("needle in local database cache\n"),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	searchTool, err := NewSearch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"path":    ".",
		"pattern": "needle",
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	result, err := searchTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	assertPayloadKeys(t, payload, "hits", "truncated")
	hits, _ := payload["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("hits = %#v, want one text-file hit", payload["hits"])
	}
	hit, _ := hits[0].(map[string]any)
	if got, _ := hit["path"].(string); filepath.Base(got) != "main.go" {
		t.Fatalf("hit path = %q, want main.go", got)
	}
	meta := filesystemToolMeta(t, result)
	if got := numericMetaValue(meta["binary_files_skipped"]); got != 1 {
		t.Fatalf("binary_files_skipped = %v, want 1", meta["binary_files_skipped"])
	}
	if got := numericMetaValue(meta["files_selected"]); got != 2 {
		t.Fatalf("files_selected = %v, want main.go and artifact.bin only", meta["files_selected"])
	}
}

func TestSearchToolCompletesBinaryDetectionAfterResultLimit(t *testing.T) {
	dir := t.TempDir()
	binaryLines := make([]string, searchResultLimit+1)
	for i := range binaryLines {
		binaryLines[i] = strings.Repeat("x", 100) + " needle"
	}
	binaryPrefix := strings.Join(binaryLines, "\n") + "\n"
	if len(binaryPrefix) <= binaryDetectionBytes {
		t.Fatalf("binary test prefix = %d bytes, want more than detection sample %d", len(binaryPrefix), binaryDetectionBytes)
	}
	binaryContent := append([]byte(binaryPrefix), 0)
	binaryContent = append(binaryContent, []byte("binary tail")...)
	if err := os.WriteFile(filepath.Join(dir, "a-artifact.bin"), binaryContent, 0o644); err != nil {
		t.Fatalf("WriteFile(a-artifact.bin) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "z-source.go"), []byte("const needle = true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(z-source.go) error = %v", err)
	}
	searchTool, err := NewSearch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}
	input, _ := json.Marshal(map[string]any{"pattern": "needle"})

	result, err := searchTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	hits, _ := payload["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("hits = %#v, want only the text-file hit", payload["hits"])
	}
	hit, _ := hits[0].(map[string]any)
	if got, _ := hit["path"].(string); filepath.Base(got) != "z-source.go" {
		t.Fatalf("hit path = %q, want z-source.go", got)
	}
	if truncated, _ := payload["truncated"].(bool); truncated {
		t.Fatalf("truncated = true, want binary hits rolled back before result limiting")
	}
	meta := filesystemToolMeta(t, result)
	if got := numericMetaValue(meta["binary_files_skipped"]); got != 1 {
		t.Fatalf("binary_files_skipped = %v, want 1", meta["binary_files_skipped"])
	}
}

func TestSearchToolRejectsUnsupportedArguments(t *testing.T) {
	dir := t.TempDir()
	searchTool, err := NewSearch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}
	for _, extra := range []map[string]any{
		{"exclude": "*_test.go"},
		{"limit": 5},
		{"regex": true},
		{"case_sensitive": false},
	} {
		input := map[string]any{"pattern": "needle"}
		for key, value := range extra {
			input[key] = value
		}
		raw, _ := json.Marshal(input)
		_, err := searchTool.Call(context.Background(), tool.Call{Input: raw})
		var toolErr *tool.ToolError
		if !errors.As(err, &toolErr) || toolErr.Code != tool.ErrorCodeInvalidInput {
			t.Fatalf("Call(%v) error = %T %v, want invalid_input", extra, err, err)
		}
	}
}

func TestSearchToolDefaultsToCWDAndSupportsInlineCaseFlag(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("const Needle = true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(main.go) error = %v", err)
	}
	searchTool, err := NewSearch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}
	input, _ := json.Marshal(map[string]any{
		"pattern": "(?i)needle",
		"include": "*.go",
	})

	result, err := searchTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	if hits, _ := payload["hits"].([]any); len(hits) != 1 {
		t.Fatalf("hits = %#v, want one cwd hit", payload["hits"])
	}
	if got, _ := filesystemToolMeta(t, result)["path"].(string); got != dir {
		t.Fatalf("metadata path = %q, want cwd %q", got, dir)
	}
}

func TestSearchToolRejectsMalformedRegexAndIncludeGlob(t *testing.T) {
	dir := t.TempDir()
	searchTool, err := NewSearch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}
	for _, input := range []map[string]any{
		{"pattern": "["},
		{"pattern": "needle", "include": "[*.go"},
	} {
		raw, _ := json.Marshal(input)
		_, err := searchTool.Call(context.Background(), tool.Call{Input: raw})
		var toolErr *tool.ToolError
		if !errors.As(err, &toolErr) || toolErr.Code != tool.ErrorCodeInvalidInput {
			t.Fatalf("Call(%v) error = %T %v, want invalid_input", input, err, err)
		}
	}
}

func TestSearchToolUsesFixedResultAndLineLimits(t *testing.T) {
	dir := t.TempDir()
	lines := make([]string, searchResultLimit+1)
	longLine := strings.Repeat("x", maxSearchLineRunes+50) + " needle"
	for i := range lines {
		lines[i] = longLine
	}
	if err := os.WriteFile(filepath.Join(dir, "large.txt"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("WriteFile(large.txt) error = %v", err)
	}
	searchTool, err := NewSearch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}
	input, _ := json.Marshal(map[string]any{"pattern": "needle"})

	result, err := searchTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	hits, _ := payload["hits"].([]any)
	if len(hits) != searchResultLimit {
		t.Fatalf("len(hits) = %d, want %d", len(hits), searchResultLimit)
	}
	if truncated, _ := payload["truncated"].(bool); !truncated {
		t.Fatalf("truncated = %v, want true", payload["truncated"])
	}
	first, _ := hits[0].(map[string]any)
	text, _ := first["text"].(string)
	if !strings.Contains(text, "needle") || !strings.Contains(text, "[line truncated]") {
		t.Fatalf("bounded hit text omitted match or truncation marker: %q", text)
	}
}

func TestSearchLineExcerptClampsBoundsAndNormalizesInvalidUTF8(t *testing.T) {
	invalid := string([]byte{0xff, 'a'})
	if got := searchLineExcerpt(invalid, -10, len(invalid)+10); !utf8.ValidString(got) || got != "\uFFFDa" {
		t.Fatalf("invalid UTF-8 excerpt = %q, want normalized replacement rune", got)
	}

	long := strings.Repeat("前", maxSearchLineRunes+20)
	got := searchLineExcerpt(long, -10, len(long)+10)
	if !utf8.ValidString(got) || !strings.Contains(got, "[line truncated]") {
		t.Fatalf("clamped long excerpt = %q, want valid bounded text with truncation marker", got)
	}
}

func TestSearchAndGlobDefinitionsStaySmall(t *testing.T) {
	searchTool, err := NewSearch(fakeRuntime{defaultFS: hostFileSystem{cwd: t.TempDir()}})
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}
	globTool, err := NewGlob(fakeRuntime{defaultFS: hostFileSystem{cwd: t.TempDir()}})
	if err != nil {
		t.Fatalf("NewGlob() error = %v", err)
	}
	assertSchemaProperties(t, searchTool.Definition().InputSchema, "pattern", "path", "include")
	assertSchemaProperties(t, globTool.Definition().InputSchema, "pattern", "path")
}

func filesystemToolPayload(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	if len(result.Content) != 1 || result.Content[0].Kind != model.PartKindJSON {
		t.Fatalf("result.Content = %+v, want json", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSONValue(), &payload); err != nil {
		t.Fatalf("Unmarshal(payload) error = %v", err)
	}
	return payload
}

func filesystemToolMeta(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	caelis, _ := result.Metadata["caelis"].(map[string]any)
	runtime, _ := caelis["runtime"].(map[string]any)
	meta, _ := runtime["tool"].(map[string]any)
	if meta == nil {
		t.Fatalf("missing tool metadata: %#v", result.Metadata)
	}
	return meta
}

func stringSlicePayloadValue(t *testing.T, value any) []string {
	t.Helper()
	raw, _ := value.([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("payload value contains non-string item: %#v", value)
		}
		out = append(out, text)
	}
	return out
}

func numericMetaValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func assertPayloadKeys(t *testing.T, payload map[string]any, want ...string) {
	t.Helper()
	if len(payload) != len(want) {
		t.Fatalf("payload keys = %#v, want %v", payload, want)
	}
	for _, key := range want {
		if _, ok := payload[key]; !ok {
			t.Fatalf("payload missing key %q: %#v", key, payload)
		}
	}
}

func assertSchemaProperties(t *testing.T, schema map[string]any, want ...string) {
	t.Helper()
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) != len(want) {
		t.Fatalf("schema properties = %#v, want %v", properties, want)
	}
	for _, key := range want {
		if _, ok := properties[key]; !ok {
			t.Fatalf("schema missing property %q: %#v", key, properties)
		}
	}
}
