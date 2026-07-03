package skilltool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skillfs "github.com/caelis-labs/caelis/impl/skill/fs"
	"github.com/caelis-labs/caelis/ports/model"
	"github.com/caelis-labs/caelis/ports/skill"
	"github.com/caelis-labs/caelis/ports/tool"
)

func TestSkillToolLoadsSkillContent(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "echo")
	if err := os.MkdirAll(skillRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(skillRoot) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("---\nname: echo\ndescription: Echo skill.\n---\n# Echo\n\nFollow echo instructions.\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(SKILL.md) error = %v", err)
	}

	skillTool := New(Config{
		Catalog: catalogForSkillToolTest(t, skill.DiscoverRequest{Dirs: []string{root}}),
	})
	result, err := skillTool.Call(context.Background(), tool.Call{
		ID:    "call-1",
		Name:  ToolName,
		Input: mustJSONForSkillToolTest(t, map[string]any{"name": "ECHO"}),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if result.ID != "call-1" || result.Name != ToolName {
		t.Fatalf("result identity = %#v, want call-1/%s", result, ToolName)
	}
	if len(result.Content) != 1 || result.Content[0].Kind != model.PartKindText {
		t.Fatalf("content = %#v, want one text part", result.Content)
	}
	text := result.Content[0].Text.Text
	for _, want := range []string{
		`<skill_content name="echo">`,
		"# Skill: echo",
		"Follow echo instructions.",
		"Base directory for this skill: " + skillRoot,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("skill output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "---") || strings.Contains(text, "description: Echo skill.") {
		t.Fatalf("skill output includes front matter:\n%s", text)
	}
}

func TestSkillToolLoadsPluginSkillByLocalName(t *testing.T) {
	pluginRoot := t.TempDir()
	skillRoot := filepath.Join(pluginRoot, "brainstorm")
	if err := os.MkdirAll(skillRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(skillRoot) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("---\nname: brainstorm\ndescription: Brainstorm options.\n---\n# Brainstorm\n\nGenerate alternatives first.\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(SKILL.md) error = %v", err)
	}

	skillTool := New(Config{
		Catalog: catalogForSkillToolTest(t, skill.DiscoverRequest{
			PluginBundles: []skill.PluginBundle{{
				Plugin:    "superpowers",
				Namespace: "superpowers",
				Root:      pluginRoot,
				Enabled:   true,
			}},
		}),
	})
	result, err := skillTool.Call(context.Background(), tool.Call{
		ID:    "call-1",
		Name:  ToolName,
		Input: mustJSONForSkillToolTest(t, map[string]any{"name": "brainstorm"}),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	text := result.Content[0].Text.Text
	if !strings.Contains(text, `<skill_content name="superpowers:brainstorm">`) {
		t.Fatalf("skill output = %q, want canonical namespaced skill name", text)
	}
	if !strings.Contains(text, "Generate alternatives first.") {
		t.Fatalf("skill output = %q, want loaded plugin skill content", text)
	}
}

func TestSkillToolUsesCatalogSnapshot(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "cached")
	if err := os.MkdirAll(skillRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(skillRoot) error = %v", err)
	}
	skillPath := filepath.Join(skillRoot, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: cached\ndescription: Cached skill.\n---\n# Cached\n\nUse cached metadata.\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(SKILL.md) error = %v", err)
	}

	skillTool := New(Config{
		Catalog: skill.NewCatalog([]skill.Meta{{
			Name:        "cached",
			LocalName:   "cached",
			Description: "Cached skill.",
			Path:        skillPath,
		}}),
	})
	result, err := skillTool.Call(context.Background(), tool.Call{
		ID:    "call-1",
		Name:  ToolName,
		Input: mustJSONForSkillToolTest(t, map[string]any{"name": "cached"}),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if text := result.Content[0].Text.Text; !strings.Contains(text, "Use cached metadata.") {
		t.Fatalf("skill output = %q, want cached skill content", text)
	}
}

func TestSkillToolReportsAmbiguousLocalName(t *testing.T) {
	oneRoot := writePluginSkillForSkillToolTest(t, "brainstorm", "Brainstorm one.")
	twoRoot := writePluginSkillForSkillToolTest(t, "brainstorm", "Brainstorm two.")
	skillTool := New(Config{
		Catalog: catalogForSkillToolTest(t, skill.DiscoverRequest{
			PluginBundles: []skill.PluginBundle{
				{Plugin: "one", Namespace: "one", Root: oneRoot, Enabled: true},
				{Plugin: "two", Namespace: "two", Root: twoRoot, Enabled: true},
			},
		}),
	})

	_, err := skillTool.Call(context.Background(), tool.Call{
		ID:    "call-1",
		Name:  ToolName,
		Input: mustJSONForSkillToolTest(t, map[string]any{"name": "brainstorm"}),
	})
	if err == nil {
		t.Fatal("Call() error = nil, want ambiguous local-name error")
	}
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("Call() error = %T %[1]v, want ToolError", err)
	}
	if toolErr.Code != tool.ErrorCodeInvalidInput {
		t.Fatalf("error code = %s, want %s", toolErr.Code, tool.ErrorCodeInvalidInput)
	}
	if !strings.Contains(toolErr.Message, "ambiguous") || !strings.Contains(toolErr.Message, "one:brainstorm") {
		t.Fatalf("error message = %q, want namespace suggestion", toolErr.Message)
	}
}

func TestSkillToolRejectsUnknownArgsBeforeLookup(t *testing.T) {
	skillTool := New(Config{})
	_, err := skillTool.Call(context.Background(), tool.Call{
		Name:  ToolName,
		Input: mustJSONForSkillToolTest(t, map[string]any{"name": "missing", "unexpected": true}),
	})
	if err == nil {
		t.Fatal("Call() error = nil, want unknown arg rejection")
	}
	if !strings.Contains(err.Error(), "unexpected") || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("Call() error = %v, want unsupported unexpected arg", err)
	}
}

func writePluginSkillForSkillToolTest(t *testing.T, name string, description string) string {
	t.Helper()
	root := t.TempDir()
	skillRoot := filepath.Join(root, name)
	if err := os.MkdirAll(skillRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(skillRoot) error = %v", err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n# " + name + "\n\nUse " + name + ".\n"
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(SKILL.md) error = %v", err)
	}
	return root
}

func catalogForSkillToolTest(t *testing.T, req skill.DiscoverRequest) skill.Catalog {
	t.Helper()
	metas, err := (skillfs.Discovery{}).Discover(context.Background(), req)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	return skill.NewCatalog(metas)
}

func mustJSONForSkillToolTest(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return raw
}
