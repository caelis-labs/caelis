// Package prompt assembles model instructions from application resource
// catalogs. It does not discover files or own runtime orchestration.
package prompt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/plugin"
	appresources "github.com/OnslaughtSnail/caelis/internal/app/resources"
)

type Config struct {
	AppName string
	Catalog appresources.Catalog
}

func BuildInstructions(ctx context.Context, cfg Config) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var instructions []string
	if intro := systemIdentity(cfg.AppName); intro != "" {
		instructions = append(instructions, intro)
	}
	if permissions := runtimePermissionInstructions(); permissions != "" {
		instructions = append(instructions, permissions)
	}
	prompts, err := promptInstructions(ctx, cfg.Catalog.Prompts)
	if err != nil {
		return nil, err
	}
	instructions = append(instructions, prompts...)
	if skills := skillsInstruction(cfg.Catalog.Skills); skills != "" {
		instructions = append(instructions, skills)
	}
	return compactInstructions(instructions), nil
}

func promptInstructions(ctx context.Context, fragments []plugin.PromptFragment) ([]string, error) {
	if len(fragments) == 0 {
		return nil, nil
	}
	ordered := make([]plugin.PromptFragment, 0, len(fragments))
	for _, fragment := range fragments {
		scope := strings.ToLower(strings.TrimSpace(fragment.Scope))
		if scope != "" && scope != "system" && scope != "instruction" {
			continue
		}
		ordered = append(ordered, fragment)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority == ordered[j].Priority {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Priority < ordered[j].Priority
	})
	out := make([]string, 0, len(ordered))
	for _, fragment := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text, err := promptText(fragment)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, renderSection(firstNonEmpty(fragment.ID, "prompt"), text))
	}
	return out, nil
}

func promptText(fragment plugin.PromptFragment) (string, error) {
	var parts []string
	if text := strings.TrimSpace(fragment.Text); text != "" {
		parts = append(parts, text)
	}
	for _, path := range fragment.Paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && strings.TrimSpace(fragment.Text) != "" {
				continue
			}
			return "", fmt.Errorf("app/prompt: read prompt fragment %s: %w", path, err)
		}
		if text := strings.TrimSpace(string(raw)); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func skillsInstruction(skills []plugin.SkillDescriptor) string {
	if len(skills) == 0 {
		return ""
	}
	ordered := make([]plugin.SkillDescriptor, 0, len(skills))
	for _, skill := range skills {
		if strings.TrimSpace(skill.Name) == "" {
			continue
		}
		ordered = append(ordered, skill)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Name < ordered[j].Name
	})
	var b strings.Builder
	b.WriteString("### Available Skills\n")
	for _, skill := range ordered {
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(skill.Name))
		if description := strings.TrimSpace(skill.Description); description != "" {
			b.WriteString(": ")
			b.WriteString(description)
		}
		if len(skill.Paths) > 0 {
			b.WriteString(" (")
			b.WriteString(strings.Join(skill.Paths, ", "))
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func systemIdentity(appName string) string {
	appName = firstNonEmpty(appName, "caelis")
	return "You are " + appName + ", an ACP-native coding agent runtime."
}

func runtimePermissionInstructions() string {
	return strings.TrimSpace(`### Runtime Permissions
- Run normal inspection, builds, tests, and workspace file edits with default sandbox permissions.
- For host-level operations that must bypass the sandbox, such as git/control metadata writes, use run_command with sandbox_permissions=require_escalated and a concise justification.`)
}

func renderSection(id string, text string) string {
	id = strings.TrimSpace(id)
	text = strings.TrimSpace(text)
	if id == "" {
		return text
	}
	return "### " + id + "\n" + text
}

func compactInstructions(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, text := range in {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
