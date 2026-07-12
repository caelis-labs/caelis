package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestGlobToolStopsAfterLimitPlusOneMatches(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	globTool, err := NewGlob(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewGlob() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"pattern": "*.txt",
		"limit":   2,
	})
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
	meta := filesystemToolMeta(t, result)
	if got := numericMetaValue(meta["total_count"]); got != 3 {
		t.Fatalf("total_count = %v, want limit+1 sentinel count 3", meta["total_count"])
	}
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
	if got, _ := payload["pattern"].(string); got != "*.py" {
		t.Fatalf("payload pattern = %q, want original pattern", got)
	}
	if got, _ := payload["path"].(string); got != demoDir {
		t.Fatalf("payload path = %q, want search directory", got)
	}
	matches := stringSlicePayloadValue(t, payload["matches"])
	if len(matches) != 1 || matches[0] != filepath.Join(demoDir, "main.py") {
		t.Fatalf("matches = %#v, want only demo/main.py", matches)
	}
	meta := filesystemToolMeta(t, result)
	if got, _ := meta["resolved_pattern"].(string); got != filepath.Join(demoDir, "*.py") {
		t.Fatalf("metadata resolved_pattern = %q, want path-resolved pattern", got)
	}
}

func TestGlobToolRejectsAbsolutePatternWhenPathProvided(t *testing.T) {
	dir := t.TempDir()
	globTool, err := NewGlob(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewGlob() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"pattern": filepath.Join(dir, "*.py"),
		"path":    dir,
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

func TestGlobToolExcludeBarePatternMatchesNestedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	for _, name := range []string{"main.go", filepath.Join("pkg", "service.go"), filepath.Join("pkg", "service_test.go")} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package test\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
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

	result, err := globTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	matches := stringSlicePayloadValue(t, payload["matches"])
	if len(matches) != 2 {
		t.Fatalf("matches = %#v, want 2 non-test files", matches)
	}
	for _, match := range matches {
		if filepath.Base(match) == "service_test.go" {
			t.Fatalf("exclude did not filter nested test file: %#v", matches)
		}
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

func TestSearchToolPlainTxtDollarDoesNotMatchFilenameAndRegexMatchesContent(t *testing.T) {
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
	if got := numericMetaValue(payload["count"]); got != 0 {
		t.Fatalf("plain SEARCH count = %v, want 0 because filenames are not searched", payload["count"])
	}

	input, err = json.Marshal(map[string]any{
		"path":    ".",
		"pattern": ".txt$",
		"regex":   true,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	result, err = searchTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call(regex) error = %v", err)
	}
	payload = filesystemToolPayload(t, result)
	if got := numericMetaValue(payload["count"]); got != 1 {
		t.Fatalf("regex SEARCH count = %v, want 1 content hit", payload["count"])
	}
	if got, _ := payload["path"].(string); filepath.Clean(got) != filepath.Clean(dir) {
		t.Fatalf("payload path = %q, want %q", got, dir)
	}
	if got, _ := payload["pattern"].(string); got != ".txt$" {
		t.Fatalf("payload pattern = %q, want .txt$", got)
	}
	if got, _ := payload["regex"].(bool); !got {
		t.Fatalf("payload regex = %v, want true", payload["regex"])
	}
	hits, _ := payload["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("hits = %#v, want one regex hit", payload["hits"])
	}
	hit, _ := hits[0].(map[string]any)
	if got := numericMetaValue(hit["column"]); got <= 0 {
		t.Fatalf("hit column = %v, want positive column", hit["column"])
	}
	if got, _ := hit["match"].(string); got == "" {
		t.Fatalf("hit match = %q, want non-empty match", got)
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
	if got := numericMetaValue(payload["count"]); got != 2 {
		t.Fatalf("hits = %#v, want two txt hits", payload["hits"])
	}
	hits, _ := payload["hits"].([]any)
	for _, rawHit := range hits {
		hit, _ := rawHit.(map[string]any)
		path, _ := hit["path"].(string)
		if filepath.Ext(path) != ".txt" {
			t.Fatalf("SEARCH include returned non-txt hit: %#v", payload["hits"])
		}
	}
}

func TestSearchToolExcludeBarePatternMatchesNestedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	files := map[string]string{
		filepath.Join("pkg", "service.go"):      "needle\n",
		filepath.Join("pkg", "service_test.go"): "needle\n",
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
		"exclude": "*_test.go",
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
	if len(hits) != 1 {
		t.Fatalf("hits = %#v, want one non-test hit", payload["hits"])
	}
	hit, _ := hits[0].(map[string]any)
	path, _ := hit["path"].(string)
	if filepath.Base(path) != "service.go" {
		t.Fatalf("SEARCH returned excluded file: %#v", payload["hits"])
	}
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
