package skilltool

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	skillfs "github.com/OnslaughtSnail/caelis/impl/skill/fs"
	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/internal/toolutil"
	"github.com/OnslaughtSnail/caelis/impl/tool/internal/argparse"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/skill"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const ToolName = "skill"

type Config struct {
	Loader  skill.Loader
	Catalog skill.Catalog
}

type Tool struct {
	loader  skill.Loader
	catalog skill.Catalog
}

func New(cfg Config) *Tool {
	loader := cfg.Loader
	if loader == nil {
		loader = skillfs.Loader{}
	}
	return &Tool{
		loader:  loader,
		catalog: cfg.Catalog,
	}
}

func (t *Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		Description: "Load a specialized skill when the task matches one of the available skills in the system context. Use this tool to inject the skill's SKILL.md instructions and relative-resource base directory into the conversation. The skill name must match a listed skill name or an unambiguous local skill name.",
		InputSchema: map[string]any{
			"type":                 "object",
			"required":             []string{"name"},
			"additionalProperties": false,
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Name of the skill from the available skills list.",
				},
			},
		},
		Metadata: toolutil.AnnotationMetadata(true, false, true, false),
	}
}

func (t *Tool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t == nil || t.loader == nil {
		return tool.Result{}, tool.NewError(tool.ErrorCodeNotFound, "skill tool is unavailable")
	}
	if err := toolutil.WithContextCancel(ctx); err != nil {
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
	meta, status := t.catalog.Resolve(name)
	switch status {
	case skill.ResolveMatched:
	case skill.ResolveAmbiguous:
		return tool.Result{}, tool.NewError(tool.ErrorCodeInvalidInput, ambiguousSkillNameMessage(t.catalog, name))
	default:
		return tool.Result{}, tool.NewError(tool.ErrorCodeNotFound, "skill not found: "+name)
	}
	bundle, err := t.loader.Load(ctx, skill.RefFromMeta(meta))
	if err != nil {
		return tool.Result{}, fmt.Errorf("skill: load %q: %w", meta.Name, err)
	}
	return tool.Result{
		ID:   call.ID,
		Name: call.Name,
		Content: []model.Part{
			model.NewTextPart(skillModelOutput(bundle)),
		},
		Metadata: map[string]any{
			"caelis": map[string]any{
				"version": 1,
				"runtime": map[string]any{
					"tool": map[string]any{
						"name": bundle.Meta.Name,
						"path": bundle.Meta.Path,
						"root": bundle.Root,
					},
				},
			},
		},
	}, nil
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
