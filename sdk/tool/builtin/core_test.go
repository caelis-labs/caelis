package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/host"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
	"github.com/OnslaughtSnail/caelis/sdk/tool/builtin/filesystem"
	"github.com/OnslaughtSnail/caelis/sdk/tool/builtin/plan"
	"github.com/OnslaughtSnail/caelis/sdk/tool/builtin/shell"
	builtintask "github.com/OnslaughtSnail/caelis/sdk/tool/builtin/task"
	"github.com/OnslaughtSnail/caelis/sdk/tool/registry"
)

func TestBuildCoreToolsCreatesDefaultCodingGroup(t *testing.T) {
	t.Parallel()

	rt, err := host.New(host.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	tools, err := BuildCoreTools(CoreToolsConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("BuildCoreTools() error = %v", err)
	}
	if got, want := len(tools), 9; got != want {
		t.Fatalf("len(tools) = %d, want %d", got, want)
	}
	if got := tools[0].Definition().Name; got != filesystem.ReadToolName {
		t.Fatalf("first tool = %q, want %q", got, filesystem.ReadToolName)
	}
	if got := tools[6].Definition().Name; got != shell.BashToolName {
		t.Fatalf("bash tool = %q, want %q", got, shell.BashToolName)
	}
	if got := tools[7].Definition().Name; got != builtintask.ToolName {
		t.Fatalf("task tool = %q, want %q", got, builtintask.ToolName)
	}
	if got := tools[8].Definition().Name; got != plan.ToolName {
		t.Fatalf("last tool = %q, want %q", got, plan.ToolName)
	}
}

func TestEnsureCoreToolsRejectsReservedBuiltinNames(t *testing.T) {
	t.Parallel()

	userTool := sdktool.NamedTool{Def: sdktool.Definition{Name: filesystem.ReadToolName}}
	_, err := EnsureCoreTools([]sdktool.Tool{userTool}, nil)
	if err == nil {
		t.Fatal("EnsureCoreTools() error = nil, want reserved name failure")
	}
}

func TestCoreCodingToolsE2E(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rt, err := host.New(host.Config{CWD: dir})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	tools, err := BuildCoreTools(CoreToolsConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("BuildCoreTools() error = %v", err)
	}
	reg, err := registry.NewMemory(tools...)
	if err != nil {
		t.Fatalf("NewMemory() error = %v", err)
	}

	writeTool := mustLookupTool(t, reg, filesystem.WriteToolName)
	writeResult := runToolJSON(t, writeTool, map[string]any{
		"path":    "notes.txt",
		"content": "hello\nworld\n",
	})
	if got := writeResult["path"]; got != filepath.Join(dir, "notes.txt") {
		t.Fatalf("write path = %v", got)
	}

	readTool := mustLookupTool(t, reg, filesystem.ReadToolName)
	readResult := runToolJSON(t, readTool, map[string]any{
		"path": "notes.txt",
	})
	if got := readResult["content"]; !strings.Contains(got.(string), "1: hello") {
		t.Fatalf("read content = %v, want numbered file lines", got)
	}

	patchTool := mustLookupTool(t, reg, filesystem.PatchToolName)
	patchResult := runToolJSON(t, patchTool, map[string]any{
		"path": "notes.txt",
		"old":  "world",
		"new":  "caelis",
	})
	if got := patchResult["replaced"]; got != float64(1) {
		t.Fatalf("patch replaced = %v, want 1", got)
	}

	searchTool := mustLookupTool(t, reg, filesystem.SearchToolName)
	searchResult := runToolJSON(t, searchTool, map[string]any{
		"path":  dir,
		"query": "caelis",
	})
	if got := searchResult["count"]; got != float64(1) {
		t.Fatalf("search count = %v, want 1", got)
	}

	globTool := mustLookupTool(t, reg, filesystem.GlobToolName)
	globResult := runToolJSON(t, globTool, map[string]any{
		"pattern": filepath.Join(dir, "*.txt"),
	})
	if got := globResult["count"]; got != float64(1) {
		t.Fatalf("glob count = %v, want 1", got)
	}

	listTool := mustLookupTool(t, reg, filesystem.ListToolName)
	listResult := runToolJSON(t, listTool, map[string]any{
		"path": dir,
	})
	if got := listResult["count"]; got != float64(1) {
		t.Fatalf("list count = %v, want 1", got)
	}

	if err := os.MkdirAll(filepath.Join(dir, "_sync_mirrors", "large.git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(_sync_mirrors) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "_sync_mirrors", "large.git", "ignored.txt"), []byte("caelis\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(ignored mirror) error = %v", err)
	}
	searchResult = runToolJSON(t, searchTool, map[string]any{
		"path":  dir,
		"query": "caelis",
	})
	if got := searchResult["count"]; got != float64(2) {
		t.Fatalf("search count with mirror dir = %v, want 2", got)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("_sync_mirrors/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.gitignore) error = %v", err)
	}
	searchResult = runToolJSON(t, searchTool, map[string]any{
		"path":              dir,
		"query":             "caelis",
		"respect_gitignore": true,
	})
	if got := searchResult["count"]; got != float64(1) {
		t.Fatalf("search count with respect_gitignore = %v, want 1", got)
	}
	globResult = runToolJSON(t, globTool, map[string]any{
		"pattern":           filepath.Join(dir, "**/*.txt"),
		"respect_gitignore": true,
	})
	if got := globResult["count"]; got != float64(1) {
		t.Fatalf("glob count with respect_gitignore = %v, want 1", got)
	}
	listResult = runToolJSON(t, listTool, map[string]any{
		"path":              dir,
		"respect_gitignore": true,
	})
	if got := listResult["count"]; got != float64(2) {
		t.Fatalf("list count with respect_gitignore = %v, want 2", got)
	}

	bashTool := mustLookupTool(t, reg, shell.BashToolName)
	bashResult := runToolJSON(t, bashTool, map[string]any{
		"command":       "cat notes.txt",
		"workdir":       dir,
		"yield_time_ms": 100,
	})
	if got := bashResult["stdout"]; !strings.Contains(got.(string), "caelis") {
		t.Fatalf("bash stdout = %v, want patched file content", got)
	}

	data, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	if got := string(data); !strings.Contains(got, "caelis") {
		t.Fatalf("notes.txt = %q, want patched content", got)
	}

	planTool := mustLookupTool(t, reg, plan.ToolName)
	planResult := runToolJSON(t, planTool, map[string]any{
		"entries": []map[string]any{
			{"content": "Read file", "status": "completed"},
			{"content": "Summarize", "status": "in_progress"},
		},
	})
	if got := planResult["message"]; got != "Plan updated" {
		t.Fatalf("plan message = %v, want %q", got, "Plan updated")
	}
}

func mustLookupTool(t *testing.T, reg sdktool.Registry, name string) sdktool.Tool {
	t.Helper()
	item, ok, err := reg.Lookup(context.Background(), name)
	if err != nil {
		t.Fatalf("Lookup(%q) error = %v", name, err)
	}
	if !ok || item == nil {
		t.Fatalf("Lookup(%q) = nil, want tool", name)
	}
	return item
}

func runToolJSON(t *testing.T, tool sdktool.Tool, args map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := tool.Call(context.Background(), sdktool.Call{
		Name:  tool.Definition().Name,
		Input: raw,
	})
	if err != nil {
		t.Fatalf("%s.Call() error = %v", tool.Definition().Name, err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("%s returned no content", tool.Definition().Name)
	}
	part := result.Content[0]
	if part.Kind != sdkmodel.PartKindJSON || part.JSON == nil {
		t.Fatalf("%s returned non-json result", tool.Definition().Name)
	}
	var out map[string]any
	if err := json.Unmarshal(part.JSONValue(), &out); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	return out
}
