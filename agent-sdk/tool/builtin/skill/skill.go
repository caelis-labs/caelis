package skilltool

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/toolpresentation"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/argparse"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
)

const ToolName = "Skill"

type Config struct {
	Loader  skill.Loader
	Catalog skill.Catalog
	// ResolveCatalog supplies the current authorized catalog at call time.
	// If unset, the tool uses Catalog.
	ResolveCatalog func(context.Context) (skill.Catalog, error)
}

type Tool struct {
	loader         skill.Loader
	catalog        skill.Catalog
	resolveCatalog func(context.Context) (skill.Catalog, error)
}

func New(cfg Config) *Tool {
	loader := cfg.Loader
	if loader == nil {
		loader = skillfs.Loader{}
	}
	return &Tool{
		loader:         loader,
		catalog:        cfg.Catalog,
		resolveCatalog: cfg.ResolveCatalog,
	}
}

func (t *Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		Description: "Load a specialized skill by name from the available skills, including newly registered skills. Returns the skill's SKILL.md instructions and relative-resource base directory. Use the full name or an unambiguous local skill name.",
		InputSchema: map[string]any{
			"type":                 "object",
			"required":             []string{"name"},
			"additionalProperties": false,
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Name of an available skill.",
				},
			},
		},
	}
}

func (t *Tool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t == nil || t.loader == nil {
		return tool.Result{}, tool.NewError(tool.ErrorCodeNotFound, "skill tool is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return tool.Result{}, err
	}
	if err := tool.RejectUnknownArgs(args, "name"); err != nil {
		return tool.Result{}, err
	}
	name, err := argparse.String(args, "name", true)
	if err != nil {
		return tool.Result{}, err
	}
	name = strings.TrimSpace(name)
	catalog := t.catalog
	if t.resolveCatalog != nil {
		catalog, err = t.resolveCatalog(ctx)
		if err != nil {
			return tool.Result{}, err
		}
	}
	meta, status := catalog.Resolve(name)
	switch status {
	case skill.ResolveMatched:
	case skill.ResolveAmbiguous:
		return tool.Result{}, tool.NewError(tool.ErrorCodeInvalidInput, ambiguousSkillNameMessage(catalog, name))
	default:
		return tool.Result{}, tool.NewError(tool.ErrorCodeNotFound, "skill not found: "+name)
	}
	bundle, err := t.loader.Load(ctx, skill.RefFromMeta(meta))
	if err != nil {
		return tool.Result{}, fmt.Errorf("load skill %q: %w", meta.Name, err)
	}
	toolpresentation.Skill(ctx, bundle.Meta.Name, bundle.Meta.Path, bundle.Root)
	return tool.Result{Content: []tool.ContentBlock{tool.Text(skillModelOutput(bundle))}}, nil
}

func ambiguousSkillNameMessage(catalog skill.Catalog, name string) string {
	suggestions := make([]string, 0, 2)
	for _, meta := range catalog.MatchingMetas(name) {
		canonical := strings.TrimSpace(meta.Name)
		if canonical == "" || containsStringFold(suggestions, canonical) {
			continue
		}
		suggestions = append(suggestions, canonical)
		if len(suggestions) >= 2 {
			break
		}
	}
	if len(suggestions) == 0 {
		return fmt.Sprintf("skill name %q is ambiguous; use a namespaced skill name", strings.TrimSpace(name))
	}
	return fmt.Sprintf("skill name %q is ambiguous; use a namespaced skill name like %q", strings.TrimSpace(name), suggestions[0])
}

func containsStringFold(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(value, needle) {
			return true
		}
	}
	return false
}

func skillModelOutput(bundle skill.Bundle) string {
	name := strings.TrimSpace(bundle.Meta.Name)
	root := strings.TrimSpace(bundle.Root)
	if root == "" && strings.TrimSpace(bundle.Meta.Path) != "" {
		root = filepath.Dir(bundle.Meta.Path)
	}
	lines := []string{
		`<skill_content name="` + xmlAttr(name) + `">`,
		"# Skill: " + name,
		"",
		strings.TrimSpace(bundle.Content),
		"",
		"Base directory for this skill: " + root,
		"Relative paths in this skill are relative to this base directory.",
		"</skill_content>",
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func xmlAttr(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		`"`, "&quot;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(strings.TrimSpace(value))
}

var _ tool.Tool = (*Tool)(nil)
