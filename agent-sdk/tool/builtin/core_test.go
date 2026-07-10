package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/plan"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	skilltool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/skill"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/web"
	"github.com/caelis-labs/caelis/agent-sdk/tool/registry"
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
	if got, want := len(tools), 12; got != want {
		t.Fatalf("len(tools) = %d, want %d", got, want)
	}
	if got := tools[0].Definition().Name; got != filesystem.ReadToolName {
		t.Fatalf("first tool = %q, want %q", got, filesystem.ReadToolName)
	}
	if got := tools[6].Definition().Name; got != shell.RunCommandToolName {
		t.Fatalf("run command tool = %q, want %q", got, shell.RunCommandToolName)
	}
	legacyCommandToolName := "BA" + "SH"
	for _, one := range tools {
		if got := one.Definition().Name; got == legacyCommandToolName {
			t.Fatalf("core tools exposed legacy %s tool", legacyCommandToolName)
		}
	}
	if got := tools[7].Definition().Name; got != task.ToolName {
		t.Fatalf("task tool = %q, want %q", got, task.ToolName)
	}
	if got := tools[8].Definition().Name; got != plan.ToolName {
		t.Fatalf("plan tool = %q, want %q", got, plan.ToolName)
	}
	if got := tools[9].Definition().Name; got != skilltool.ToolName {
		t.Fatalf("skill tool = %q, want %q", got, skilltool.ToolName)
	}
	if got := tools[10].Definition().Name; got != web.SearchToolName {
		t.Fatalf("web search tool = %q, want %q", got, web.SearchToolName)
	}
	if got := tools[11].Definition().Name; got != web.FetchToolName {
		t.Fatalf("last tool = %q, want %q", got, web.FetchToolName)
	}
}

func TestCoreToolSchemasDisallowUnknownRootProperties(t *testing.T) {
	t.Parallel()

	rt, err := host.New(host.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	tools, err := BuildCoreTools(CoreToolsConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("BuildCoreTools() error = %v", err)
	}
	for _, one := range tools {
		def := one.Definition()
		if got := def.InputSchema["additionalProperties"]; got != false {
			t.Fatalf("%s additionalProperties = %#v, want false", def.Name, got)
		}
	}
}

func TestCoreToolSchemasExposeGuidanceBoundsAndAnnotations(t *testing.T) {
	t.Parallel()

	rt, err := host.New(host.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	tools, err := BuildCoreTools(CoreToolsConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("BuildCoreTools() error = %v", err)
	}
	defs := map[string]tool.Definition{}
	for _, one := range tools {
		def := one.Definition()
		defs[def.Name] = def
	}

	requireStringMinLength(t, defs[filesystem.ReadToolName], "path", 1)
	requireIntegerBounds(t, defs[filesystem.ReadToolName], "offset", 0, nil)
	requireIntegerBounds(t, defs[filesystem.ReadToolName], "limit", 1, ptrAny(400))
	requireDescriptionContains(t, defs[filesystem.ReadToolName], "numbered lines", "small offsets", "has_more", "revision", "if_revision")
	requireAnnotations(t, defs[filesystem.ReadToolName], true, false, true, false)

	requireIntegerBounds(t, defs[filesystem.ListToolName], "limit", 1, ptrAny(1000))
	requireDescriptionContains(t, defs[filesystem.ListToolName], "not recursive", "metadata")
	requireAnnotations(t, defs[filesystem.ListToolName], true, false, true, false)

	requireStringMinLength(t, defs[filesystem.GlobToolName], "pattern", 1)
	requireStringOrStringArray(t, defs[filesystem.GlobToolName], "exclude", 1)
	requireIntegerBounds(t, defs[filesystem.GlobToolName], "limit", 1, ptrAny(1000))
	requireDescriptionContains(t, defs[filesystem.GlobToolName], "Find filesystem paths", "glob pattern")
	requireAnnotations(t, defs[filesystem.GlobToolName], true, false, true, false)

	requireStringMinLength(t, defs[filesystem.SearchToolName], "path", 1)
	requireStringMinLength(t, defs[filesystem.SearchToolName], "pattern", 1)
	requireNoProperty(t, defs[filesystem.SearchToolName], "query")
	requireStringOrStringArray(t, defs[filesystem.SearchToolName], "include", 1)
	requireStringOrStringArray(t, defs[filesystem.SearchToolName], "exclude", 1)
	requireIntegerBounds(t, defs[filesystem.SearchToolName], "limit", 1, ptrAny(200))
	requireDescriptionContains(t, defs[filesystem.SearchToolName], "Search file contents", "regex matches")
	requireAnnotations(t, defs[filesystem.SearchToolName], true, false, true, false)

	requireStringMinLength(t, defs[filesystem.WriteToolName], "path", 1)
	requireNoMinLength(t, defs[filesystem.WriteToolName], "content")
	requireDescriptionContains(t, defs[filesystem.WriteToolName], "Prefer PATCH", "if_revision")
	requireAnnotations(t, defs[filesystem.WriteToolName], false, true, true, false)

	requireStringMinLength(t, defs[filesystem.PatchToolName], "path", 1)
	requirePatchEditSchema(t, defs[filesystem.PatchToolName])
	requireDescriptionContains(t, defs[filesystem.PatchToolName], "surgical edits", "if_revision")
	requireAnnotations(t, defs[filesystem.PatchToolName], false, true, true, false)

	requireStringMinLength(t, defs[shell.RunCommandToolName], "command", 1)
	requireIntegerBounds(t, defs[shell.RunCommandToolName], "yield_time_ms", 0, nil)
	requireDescriptionContains(t, defs[shell.RunCommandToolName], "repository inspection", "Do not prefix with cd", "workdir", "yield_time_ms", "Prefer use_default")
	requireAnnotations(t, defs[shell.RunCommandToolName], false, true, false, true)

	requireStringMinLength(t, defs[task.ToolName], "task_id", 1)
	requireIntegerBounds(t, defs[task.ToolName], "yield_time_ms", -1, nil)
	requireNoProperty(t, defs[task.ToolName], "wait_until_done")
	requireDescriptionContains(t, defs[task.ToolName], "Control an async task", "terminal stdin", "follow-up prompt", "Always wait")
	requireAnnotations(t, defs[task.ToolName], false, true, false, true)

	requirePlanSchema(t, defs[plan.ToolName])
	requireDescriptionContains(t, defs[plan.ToolName], "multi-step execution checklist", "insert or append", "do not overwrite or delete history")
	requireAnnotations(t, defs[plan.ToolName], false, false, true, false)

	requireStringMinLength(t, defs[skilltool.ToolName], "name", 1)
	requireDescriptionContains(t, defs[skilltool.ToolName], "Load a specialized skill", "available skills", "SKILL.md")
	requireAnnotations(t, defs[skilltool.ToolName], true, false, true, false)

	requireStringMinLength(t, defs[web.SearchToolName], "query", 1)
	requireIntegerBounds(t, defs[web.SearchToolName], "max_results", 1, ptrAny(10))
	requireDescriptionContains(t, defs[web.SearchToolName], "Search the web", "concise keyword queries", "site:", "provider-native web search", "unavailable", "fall back to web_fetch")
	requireAnnotations(t, defs[web.SearchToolName], true, false, false, true)

	requireStringMinLength(t, defs[web.FetchToolName], "url", 1)
	requireIntegerBounds(t, defs[web.FetchToolName], "timeout", 1, ptrAny(120))
	requireDescriptionContains(t, defs[web.FetchToolName], "specific http or https URL", "cleaned markdown", "does not search", "artifact_path")
	requireAnnotations(t, defs[web.FetchToolName], true, false, false, true)
}

func TestCoreSearchAndGlobSchemasRemainStrict(t *testing.T) {
	t.Parallel()

	rt, err := host.New(host.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	tools, err := BuildCoreTools(CoreToolsConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("BuildCoreTools() error = %v", err)
	}
	specs := map[string]model.ToolSpec{}
	for _, spec := range tool.ModelSpecs(tools) {
		if spec.Function != nil {
			specs[spec.Function.Name] = spec
		}
	}
	for _, name := range []string{filesystem.GlobToolName, filesystem.SearchToolName} {
		spec, ok := specs[name]
		if !ok || spec.Function == nil {
			t.Fatalf("%s missing from model specs", name)
		}
		if !spec.Function.Strict {
			t.Fatalf("%s Function.Strict = false, want strict inferred from closed schema", name)
		}
	}
}

func TestEnsureCoreToolsRejectsReservedBuiltinNames(t *testing.T) {
	t.Parallel()

	userTool := tool.NamedTool{Def: tool.Definition{Name: filesystem.ReadToolName}}
	_, err := EnsureCoreTools([]tool.Tool{userTool}, nil)
	if err == nil {
		t.Fatal("EnsureCoreTools() error = nil, want reserved name failure")
	}
}

func TestCoreToolsRejectUnknownArgs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(notes.txt) error = %v", err)
	}
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

	tests := []struct {
		name string
		args map[string]any
	}{
		{filesystem.ReadToolName, map[string]any{"path": "notes.txt", "unexpected": true}},
		{filesystem.ListToolName, map[string]any{"path": ".", "unexpected": true}},
		{filesystem.GlobToolName, map[string]any{"pattern": "*.txt", "unexpected": true}},
		{filesystem.SearchToolName, map[string]any{"path": ".", "pattern": "hello", "unexpected": true}},
		{filesystem.WriteToolName, map[string]any{"path": "new.txt", "content": "new\n", "unexpected": true}},
		{filesystem.PatchToolName, map[string]any{
			"path":       "notes.txt",
			"edits":      []map[string]any{{"old": "hello", "new": "hi"}},
			"unexpected": true,
		}},
		{shell.RunCommandToolName, map[string]any{"command": "printf ok", "unexpected": true}},
		{plan.ToolName, map[string]any{"entries": []map[string]any{{"content": "Read", "status": "pending"}}, "unexpected": true}},
		{skilltool.ToolName, map[string]any{"name": "unknown", "unexpected": true}},
		{web.SearchToolName, map[string]any{"query": "latest", "unexpected": true}},
		{web.FetchToolName, map[string]any{"url": "https://example.com", "unexpected": true}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			targetTool := mustLookupTool(t, reg, tt.name)
			err := runToolErr(t, targetTool, tt.args)
			if err == nil {
				t.Fatalf("%s.Call() error = nil, want unknown arg rejection", tt.name)
			}
			if !strings.Contains(err.Error(), "unexpected") || !strings.Contains(err.Error(), "not supported") {
				t.Fatalf("%s.Call() error = %v, want unsupported unexpected arg", tt.name, err)
			}
		})
	}
}

func TestPatchRejectsUnknownNestedEditArgs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(notes.txt) error = %v", err)
	}
	rt, err := host.New(host.Config{CWD: dir})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	patchTool, err := filesystem.NewPatch(rt)
	if err != nil {
		t.Fatalf("NewPatch() error = %v", err)
	}

	err = runToolErr(t, patchTool, map[string]any{
		"path": "notes.txt",
		"edits": []map[string]any{{
			"old":        "hello",
			"new":        "hi",
			"unexpected": true,
		}},
	})
	if err == nil {
		t.Fatal("PATCH.Call() error = nil, want nested unknown arg rejection")
	}
	if !strings.Contains(err.Error(), "edits[0]") || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("PATCH.Call() error = %v, want nested edit arg context", err)
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
	if revision, _ := writeResult["revision"].(string); revision == "" {
		t.Fatalf("write revision = %#v, want non-empty", writeResult["revision"])
	}

	readTool := mustLookupTool(t, reg, filesystem.ReadToolName)
	readResult := runToolJSON(t, readTool, map[string]any{
		"path": "notes.txt",
	})
	if got := readResult["content"]; !strings.Contains(got.(string), "1: hello") {
		t.Fatalf("read content = %v, want numbered file lines", got)
	}
	revision, _ := readResult["revision"].(string)
	if revision == "" {
		t.Fatal("read revision is empty")
	}

	patchTool := mustLookupTool(t, reg, filesystem.PatchToolName)
	patchResult := runToolJSON(t, patchTool, map[string]any{
		"path": "notes.txt",
		"edits": []map[string]any{
			{
				"old":                   "world",
				"new":                   "caelis",
				"expected_replacements": 1,
			},
		},
		"if_revision": revision,
	})
	if got := patchResult["replacements"]; got != float64(1) {
		t.Fatalf("patch replacements = %v, want 1", got)
	}
	if revision, _ := patchResult["revision"].(string); revision == "" {
		t.Fatalf("patch revision = %#v, want non-empty", patchResult["revision"])
	}

	searchTool := mustLookupTool(t, reg, filesystem.SearchToolName)
	searchResult := runToolJSON(t, searchTool, map[string]any{
		"path":    dir,
		"pattern": "missing|caelis",
	})
	if got := searchResult["count"]; got != float64(1) {
		t.Fatalf("search count = %v, want 1", got)
	}
	hits, _ := searchResult["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("search hits = %#v, want one hit", searchResult["hits"])
	}
	hit, _ := hits[0].(map[string]any)
	if hit["column"] == nil || hit["match"] == nil {
		t.Fatalf("search hit = %#v, want column and match in payload", hit)
	}
	searchResult = runToolJSON(t, searchTool, map[string]any{
		"path":    dir,
		"pattern": `hello|Meta\[\"error\"\]`,
		"regex":   true,
	})
	if got := searchResult["count"]; got != float64(1) {
		t.Fatalf("regex search count = %v, want 1", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.git) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("caelis\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.git/index) error = %v", err)
	}
	searchResult = runToolJSON(t, searchTool, map[string]any{
		"path":    dir,
		"pattern": "caelis",
	})
	if got := searchResult["count"]; got != float64(1) {
		t.Fatalf("search count with .git index = %v, want 1", got)
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
		"path":    dir,
		"pattern": "caelis",
	})
	if got := searchResult["count"]; got != float64(2) {
		t.Fatalf("search count with mirror dir = %v, want 2", got)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("_sync_mirrors/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.gitignore) error = %v", err)
	}
	searchResult = runToolJSON(t, searchTool, map[string]any{
		"path":    dir,
		"pattern": "caelis",
	})
	if got := searchResult["count"]; got != float64(1) {
		t.Fatalf("search count with default gitignore = %v, want 1", got)
	}
	globResult = runToolJSON(t, globTool, map[string]any{
		"pattern": filepath.Join(dir, "**/*.txt"),
	})
	if got := globResult["count"]; got != float64(1) {
		t.Fatalf("glob count with default gitignore = %v, want 1", got)
	}
	listResult = runToolJSON(t, listTool, map[string]any{
		"path": dir,
	})
	if got := listResult["count"]; got != float64(2) {
		t.Fatalf("list count with default gitignore = %v, want 2", got)
	}

	runCommandTool := mustLookupTool(t, reg, shell.RunCommandToolName)
	runCommandResult := runToolJSON(t, runCommandTool, map[string]any{
		"command":       "cat notes.txt",
		"workdir":       dir,
		"yield_time_ms": 100,
	})
	if got := runCommandResult["result"]; !strings.Contains(got.(string), "caelis") {
		t.Fatalf("run command result = %v, want patched file content", got)
	}
	if got := runCommandResult["exit_code"]; got != float64(0) {
		t.Fatalf("run command exit_code = %v, want 0", got)
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
	if got := planResult["updated"]; got != true {
		t.Fatalf("plan updated = %v, want true", got)
	}
}

func mustLookupTool(t *testing.T, reg tool.Registry, name string) tool.Tool {
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

func runToolJSON(t *testing.T, targetTool tool.Tool, args map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := targetTool.Call(context.Background(), tool.Call{
		Name:  targetTool.Definition().Name,
		Input: raw,
	})
	if err != nil {
		t.Fatalf("%s.Call() error = %v", targetTool.Definition().Name, err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("%s returned no content", targetTool.Definition().Name)
	}
	part := result.Content[0]
	if part.Kind != model.PartKindJSON || part.JSON == nil {
		t.Fatalf("%s returned non-json result", targetTool.Definition().Name)
	}
	var out map[string]any
	if err := json.Unmarshal(part.JSONValue(), &out); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	return out
}

func runToolErr(t *testing.T, targetTool tool.Tool, args map[string]any) error {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	_, err = targetTool.Call(context.Background(), tool.Call{
		Name:  targetTool.Definition().Name,
		Input: raw,
	})
	return err
}

func requireDescriptionContains(t *testing.T, def tool.Definition, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(def.Description, want) {
			t.Fatalf("%s description missing %q: %q", def.Name, want, def.Description)
		}
	}
}

func requireStringMinLength(t *testing.T, def tool.Definition, prop string, want int) {
	t.Helper()
	schemaProp := schemaProperty(t, def, prop)
	if got := schemaProp["type"]; got != "string" {
		t.Fatalf("%s.%s type = %#v, want string", def.Name, prop, got)
	}
	if got := schemaProp["minLength"]; got != want {
		t.Fatalf("%s.%s minLength = %#v, want %d", def.Name, prop, got, want)
	}
}

func requireNoMinLength(t *testing.T, def tool.Definition, prop string) {
	t.Helper()
	schemaProp := schemaProperty(t, def, prop)
	if _, ok := schemaProp["minLength"]; ok {
		t.Fatalf("%s.%s minLength present: %#v", def.Name, prop, schemaProp["minLength"])
	}
}

func requireNoProperty(t *testing.T, def tool.Definition, prop string) {
	t.Helper()
	props, _ := def.InputSchema["properties"].(map[string]any)
	if _, ok := props[prop]; ok {
		t.Fatalf("%s.%s property present: %#v", def.Name, prop, props[prop])
	}
}

func requireIntegerBounds(t *testing.T, def tool.Definition, prop string, minimum int, maximum *any) {
	t.Helper()
	schemaProp := schemaProperty(t, def, prop)
	if got := schemaProp["type"]; got != "integer" {
		t.Fatalf("%s.%s type = %#v, want integer", def.Name, prop, got)
	}
	if got := schemaProp["minimum"]; got != minimum {
		t.Fatalf("%s.%s minimum = %#v, want %d", def.Name, prop, got, minimum)
	}
	if maximum != nil {
		if got := schemaProp["maximum"]; got != *maximum {
			t.Fatalf("%s.%s maximum = %#v, want %#v", def.Name, prop, got, *maximum)
		}
	}
}

func requireBooleanProperty(t *testing.T, def tool.Definition, prop string) {
	t.Helper()
	schemaProp := schemaProperty(t, def, prop)
	if got := schemaProp["type"]; got != "boolean" {
		t.Fatalf("%s.%s type = %#v, want boolean", def.Name, prop, got)
	}
}

func requireArrayItemMinLength(t *testing.T, def tool.Definition, prop string, want int) {
	t.Helper()
	schemaProp := schemaProperty(t, def, prop)
	items, _ := schemaProp["items"].(map[string]any)
	if len(items) == 0 {
		t.Fatalf("%s.%s items missing: %#v", def.Name, prop, schemaProp)
	}
	if got := items["minLength"]; got != want {
		t.Fatalf("%s.%s.items minLength = %#v, want %d", def.Name, prop, got, want)
	}
}

func requireStringOrStringArray(t *testing.T, def tool.Definition, prop string, wantMinLength int) {
	t.Helper()
	schemaProp := schemaProperty(t, def, prop)
	rawVariants, _ := schemaProp["anyOf"].([]any)
	if len(rawVariants) != 2 {
		t.Fatalf("%s.%s anyOf = %#v, want string and array variants", def.Name, prop, schemaProp["anyOf"])
	}
	var foundString bool
	var foundArray bool
	for _, raw := range rawVariants {
		variant, _ := raw.(map[string]any)
		switch variant["type"] {
		case "string":
			foundString = true
			if got := variant["minLength"]; got != wantMinLength {
				t.Fatalf("%s.%s string minLength = %#v, want %d", def.Name, prop, got, wantMinLength)
			}
		case "array":
			foundArray = true
			items, _ := variant["items"].(map[string]any)
			if got := items["minLength"]; got != wantMinLength {
				t.Fatalf("%s.%s array item minLength = %#v, want %d", def.Name, prop, got, wantMinLength)
			}
		default:
			t.Fatalf("%s.%s unexpected anyOf variant: %#v", def.Name, prop, variant)
		}
	}
	if !foundString || !foundArray {
		t.Fatalf("%s.%s anyOf missing string or array variant: %#v", def.Name, prop, rawVariants)
	}
}

func requirePatchEditSchema(t *testing.T, def tool.Definition) {
	t.Helper()
	edits := schemaProperty(t, def, "edits")
	items, _ := edits["items"].(map[string]any)
	if got := items["additionalProperties"]; got != false {
		t.Fatalf("PATCH edits item additionalProperties = %#v, want false", got)
	}
	props, _ := items["properties"].(map[string]any)
	oldProp, _ := props["old"].(map[string]any)
	newProp, _ := props["new"].(map[string]any)
	expectedProp, _ := props["expected_replacements"].(map[string]any)
	if got := oldProp["minLength"]; got != 1 {
		t.Fatalf("PATCH edits.old minLength = %#v, want 1", got)
	}
	if _, ok := newProp["minLength"]; ok {
		t.Fatalf("PATCH edits.new minLength present: %#v", newProp["minLength"])
	}
	if got := expectedProp["minimum"]; got != 1 {
		t.Fatalf("PATCH edits.expected_replacements minimum = %#v, want 1", got)
	}
}

func requirePlanSchema(t *testing.T, def tool.Definition) {
	t.Helper()
	entries := schemaProperty(t, def, "entries")
	items, _ := entries["items"].(map[string]any)
	props, _ := items["properties"].(map[string]any)
	contentProp, _ := props["content"].(map[string]any)
	if got := contentProp["minLength"]; got != 1 {
		t.Fatalf("PLAN entries.content minLength = %#v, want 1", got)
	}
}

func requireAnnotations(t *testing.T, def tool.Definition, readOnly, destructive, idempotent, openWorld bool) {
	t.Helper()
	annotations, _ := def.Metadata["annotations"].(map[string]any)
	if len(annotations) == 0 {
		t.Fatalf("%s annotations missing: %#v", def.Name, def.Metadata)
	}
	for key, want := range map[string]bool{
		"readOnlyHint":    readOnly,
		"destructiveHint": destructive,
		"idempotentHint":  idempotent,
		"openWorldHint":   openWorld,
	} {
		if got := annotations[key]; got != want {
			t.Fatalf("%s annotations[%s] = %#v, want %v", def.Name, key, got, want)
		}
	}
}

func schemaProperty(t *testing.T, def tool.Definition, prop string) map[string]any {
	t.Helper()
	props, _ := def.InputSchema["properties"].(map[string]any)
	if len(props) == 0 {
		t.Fatalf("%s schema properties missing", def.Name)
	}
	schemaProp, _ := props[prop].(map[string]any)
	if len(schemaProp) == 0 {
		t.Fatalf("%s.%s schema property missing: %#v", def.Name, prop, props[prop])
	}
	return schemaProp
}

func ptrAny(value any) *any {
	return &value
}
