package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/filesystem"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/plan"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/shell"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/task"
	"github.com/OnslaughtSnail/caelis/impl/tool/registry"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/tool"
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
		t.Fatalf("last tool = %q, want %q", got, plan.ToolName)
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
	requireArrayItemMinLength(t, defs[filesystem.GlobToolName], "exclude", 1)
	requireIntegerBounds(t, defs[filesystem.GlobToolName], "limit", 1, ptrAny(1000))
	requireAnnotations(t, defs[filesystem.GlobToolName], true, false, true, false)

	requireStringMinLength(t, defs[filesystem.SearchToolName], "path", 1)
	requireStringMinLength(t, defs[filesystem.SearchToolName], "query", 1)
	requireArrayItemMinLength(t, defs[filesystem.SearchToolName], "exclude", 1)
	requireIntegerBounds(t, defs[filesystem.SearchToolName], "limit", 1, ptrAny(200))
	requireDescriptionContains(t, defs[filesystem.SearchToolName], "locate symbols", "regex")
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
	requireDescriptionContains(t, defs[shell.RunCommandToolName], "repository inspection", "Do not prefix with cd", "workdir", "yield_time_ms", "require_escalated")
	requireAnnotations(t, defs[shell.RunCommandToolName], false, true, false, true)

	requireStringMinLength(t, defs[task.ToolName], "task_id", 1)
	requireIntegerBounds(t, defs[task.ToolName], "yield_time_ms", -1, nil)
	requireNoProperty(t, defs[task.ToolName], "wait_until_done")
	requireDescriptionContains(t, defs[task.ToolName], "Control an async task", "terminal stdin", "follow-up prompt", "Always wait")
	requireAnnotations(t, defs[task.ToolName], false, true, false, true)

	requirePlanSchema(t, defs[plan.ToolName])
	requireDescriptionContains(t, defs[plan.ToolName], "multi-step, risky, or ambiguous", "skip it for trivial")
	requireAnnotations(t, defs[plan.ToolName], false, false, true, false)
}

func TestEnsureCoreToolsRejectsReservedBuiltinNames(t *testing.T) {
	t.Parallel()

	userTool := tool.NamedTool{Def: tool.Definition{Name: filesystem.ReadToolName}}
	_, err := EnsureCoreTools([]tool.Tool{userTool}, nil)
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

	searchTool := mustLookupTool(t, reg, filesystem.SearchToolName)
	searchResult := runToolJSON(t, searchTool, map[string]any{
		"path":  dir,
		"query": "missing|caelis",
	})
	if got := searchResult["count"]; got != float64(1) {
		t.Fatalf("search count = %v, want 1", got)
	}
	searchResult = runToolJSON(t, searchTool, map[string]any{
		"path":  dir,
		"query": `hello|Meta\[\"error\"\]`,
		"regex": true,
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
		"path":  dir,
		"query": "caelis",
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
		"path":  dir,
		"query": "caelis",
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
