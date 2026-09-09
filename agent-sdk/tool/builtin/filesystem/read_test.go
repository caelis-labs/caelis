package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestReadToolDoesNotScanOversizedTailPastLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "generated.txt")
	oversizedLine := strings.Repeat("x", 9*1024*1024)
	if err := os.WriteFile(path, []byte("first\n"+oversizedLine+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	readTool, err := NewRead(DefaultReadConfig(), fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewRead() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"path":  "generated.txt",
		"limit": 1,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	result, err := readTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Kind != model.PartKindJSON {
		t.Fatalf("result.Content = %+v, want json", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSONValue(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, _ := payload["content"].(string); got != "1: first" {
		t.Fatalf("content = %q, want first line only", got)
	}
	if got, _ := payload["has_more"].(bool); !got {
		t.Fatalf("has_more = %v, want true", payload["has_more"])
	}
	if got, _ := payload["revision"].(string); got == "" {
		t.Fatal("revision is empty")
	}
	if got, _ := payload["revision"].(string); !strings.HasPrefix(got, "stat:") {
		t.Fatalf("truncated read revision = %q, want stat revision without tail scan", got)
	}
}

func TestReadToolDefinitionIsZeroValueSafe(t *testing.T) {
	zero := (&ReadTool{}).Definition()
	readTool, err := NewRead(ReadConfig{}, fakeRuntime{defaultFS: hostFileSystem{cwd: t.TempDir()}})
	if err != nil {
		t.Fatalf("NewRead() error = %v", err)
	}
	constructed := readTool.Definition()
	if zero.Name != ReadToolName || constructed.Name != ReadToolName {
		t.Fatalf("Name = %q / %q, want %s", zero.Name, constructed.Name, ReadToolName)
	}
	if got, want := schemaLimitMaximum(t, zero), DefaultReadConfig().MaxLimit; got != want {
		t.Fatalf("zero-value limit maximum = %d, want %d", got, want)
	}
	if got := schemaLimitMaximum(t, constructed); got != schemaLimitMaximum(t, zero) {
		t.Fatalf("constructed limit maximum = %d, want zero-value %d", got, schemaLimitMaximum(t, zero))
	}
}

func TestReadToolPreservesNumberedOutputAndCompleteRevision(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	readTool, err := NewRead(DefaultReadConfig(), fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewRead() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{"path": "notes.txt"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	result, err := readTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	if got, _ := payload["content"].(string); got != "1: alpha\n2: beta" {
		t.Fatalf("content = %q, want numbered complete file", got)
	}
	if got, _ := payload["has_more"].(bool); got {
		t.Fatalf("has_more = true, want false")
	}
	if got, _ := payload["revision"].(string); !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("revision = %q, want content hash", got)
	}
	if got := numericMetaValue(payload["next_offset"]); got != 2 {
		t.Fatalf("next_offset = %v, want 2", payload["next_offset"])
	}
}

func TestReadToolRejectsNonRegularFile(t *testing.T) {
	dir := t.TempDir()
	readTool, err := NewRead(DefaultReadConfig(), fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewRead() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	_, err = readTool.Call(context.Background(), tool.Call{Input: input})
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != tool.ErrorCodeInvalidInput || !strings.Contains(toolErr.Error(), "not a regular file") {
		t.Fatalf("Call(dir) error = %v, want invalid_input regular-file error", err)
	}
}

func TestReadToolRejectsOversizedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxReadLineBytes+1)+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	readTool, err := NewRead(DefaultReadConfig(), fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewRead() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{"path": "huge.txt", "limit": 1})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	_, err = readTool.Call(context.Background(), tool.Call{Input: input})
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != tool.ErrorCodeInvalidInput || !strings.Contains(toolErr.Error(), "8MiB") {
		t.Fatalf("Call(oversized) error = %v, want invalid_input 8MiB line error", err)
	}
}

func TestReadToolPropagatesCancellationDuringScan(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	readTool, err := NewRead(DefaultReadConfig(), fakeRuntime{defaultFS: cancelOnOpenFileSystem{
		hostFileSystem: hostFileSystem{cwd: dir},
		cancel:         cancel,
	}})
	if err != nil {
		t.Fatalf("NewRead() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{"path": "notes.txt"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	_, err = readTool.Call(ctx, tool.Call{Input: input})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Call(canceled) error = %v, want context.Canceled", err)
	}
}

func TestReadToolByteBudgetPagesWholeNumberedLines(t *testing.T) {
	root := t.TempDir()
	line := strings.Repeat("x", maxReadReturnedBytes/3)
	body := strings.Join([]string{line, line, line}, "\n")
	path := filepath.Join(root, "large.txt")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewRead(DefaultReadConfig(), fakeRuntime{defaultFS: hostFileSystem{cwd: root}})
	if err != nil {
		t.Fatal(err)
	}
	hasher := contentHasher()
	_, _ = hasher.Write([]byte(body))
	for _, page := range []struct {
		offset, end int
		more        bool
	}{
		{offset: 0, end: 2, more: true},
		{offset: 2, end: 3},
	} {
		raw, err := json.Marshal(map[string]any{"path": path, "offset": page.offset})
		if err != nil {
			t.Fatal(err)
		}
		result, err := reader.Call(t.Context(), tool.Call{Input: raw})
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Content    string `json:"content"`
			StartLine  int    `json:"start_line"`
			EndLine    int    `json:"end_line"`
			NextOffset int    `json:"next_offset"`
			HasMore    bool   `json:"has_more"`
			Revision   string `json:"revision"`
		}
		if err := json.Unmarshal(result.Content[0].JSONValue(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Content) > maxReadReturnedBytes || payload.StartLine != page.offset+1 || payload.EndLine != page.end || payload.NextOffset != page.end || payload.HasMore != page.more {
			t.Fatalf("offset=%d: bytes=%d start=%d end=%d next=%d more=%v", page.offset, len(payload.Content), payload.StartLine, payload.EndLine, payload.NextOffset, payload.HasMore)
		}
		if got := strings.Count(payload.Content, "\n") + 1; got != page.end-page.offset {
			t.Fatalf("offset=%d: returned %d lines", page.offset, got)
		}
		if !strings.HasSuffix(payload.Content, line) {
			t.Fatal("page ended with a partial line")
		}
		if page.more && !strings.HasPrefix(payload.Revision, "stat:") {
			t.Fatal("partial read must retain a stat revision")
		}
		if !page.more && payload.Revision != contentHashRevision(hasher) {
			t.Fatal("complete offset read must hash the complete file")
		}
	}
}

func schemaLimitMaximum(t *testing.T, def tool.Definition) int {
	t.Helper()
	properties, _ := def.InputSchema["properties"].(map[string]any)
	limit, _ := properties["limit"].(map[string]any)
	switch value := limit["maximum"].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		t.Fatalf("limit.maximum = %#v", limit["maximum"])
		return 0
	}
}
