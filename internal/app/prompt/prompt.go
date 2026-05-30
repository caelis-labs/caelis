// Package prompt assembles model instructions from application resource
// catalogs. It does not discover files or own runtime orchestration.
package prompt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/plugin"
	appresources "github.com/OnslaughtSnail/caelis/internal/app/resources"
)

type Config struct {
	AppName      string
	WorkspaceDir string
	BasePrompt   string
	Catalog      appresources.Catalog
	ACPAgents    []plugin.ACPAgentDescriptor
}

func BuildInstructions(ctx context.Context, cfg Config) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var instructions []string
	if system := systemInstructions(cfg.AppName, cfg.ACPAgents); system != "" {
		instructions = append(instructions, renderInstructionBlock("system_instructions", system))
	}
	agentPromptIDs := agentFilePromptIDs(cfg.Catalog.AgentFiles)
	if user := userCustomInstructions(cfg.BasePrompt, cfg.Catalog.AgentFiles); user != "" {
		instructions = append(instructions, renderInstructionBlock("user_custom_instructions", user))
	}
	prompts, err := promptInstructions(ctx, cfg.Catalog.Prompts, agentPromptIDs)
	if err != nil {
		return nil, err
	}
	instructions = append(instructions, prompts...)
	if skills := skillsInstruction(cfg.Catalog.Skills); skills != "" {
		instructions = append(instructions, skills)
	}
	env, err := environmentContext(cfg.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	if env != "" {
		instructions = append(instructions, env)
	}
	return compactInstructions(instructions), nil
}

func promptInstructions(ctx context.Context, fragments []plugin.PromptFragment, skipIDs map[string]bool) ([]string, error) {
	if len(fragments) == 0 {
		return nil, nil
	}
	ordered := make([]plugin.PromptFragment, 0, len(fragments))
	for _, fragment := range fragments {
		id := strings.TrimSpace(fragment.ID)
		if skipIDs[id] {
			continue
		}
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
	b.WriteString("## Skills\n")
	b.WriteString("Use a skill only when its description clearly matches the task. Read the minimum needed from its `SKILL.md`.\n")
	b.WriteString("### Available skills\n")
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

func systemInstructions(appName string, agents []plugin.ACPAgentDescriptor) string {
	sections := []string{
		systemIdentity(appName),
		mainSessionRoleInstructions(),
		capabilityGuidanceInstructions(agents),
		runtimePermissionInstructions(),
	}
	return strings.Join(compactInstructions(sections), "\n\n")
}

func systemIdentity(appName string) string {
	appName = firstNonEmpty(appName, "caelis")
	return strings.Join([]string{
		"## Core Stable Rules",
		"",
		"You are " + appName + ", an ACP-native coding agent runtime working in the user's workspace.",
		"Drive toward the user's concrete goal: inspect enough context, make the smallest useful change, verify, then report.",
		"Preserve user work. Do not revert unrelated changes; adapt to the existing code, architecture, and project boundaries.",
		"Prefer repository truth over assumptions. Read or search before editing, and use shell checks when they are the clearest verification path.",
		"Ask only when the answer cannot be discovered locally and would materially change the next action.",
		"Keep responses concise, factual, and focused on what changed, what was verified, and what remains.",
	}, "\n")
}

func mainSessionRoleInstructions() string {
	return strings.Join([]string{
		"## Main Session Role",
		"",
		"Own architecture, task decomposition, integration, validation, and final judgment.",
		"Use plans for non-trivial work, keep them current, and close the loop with concrete verification.",
	}, "\n")
}

func capabilityGuidanceInstructions(agents []plugin.ACPAgentDescriptor) string {
	lines := []string{
		"## Capability Guidance",
		"",
		"- Inspect with read, search, glob, and list tools; edit with write or patch tools; use run_command for shell work and task for yielded async work.",
		"- Load a skill only when its description clearly matches the task; read only the needed parts of its `SKILL.md`.",
		"- Obey the active approval mode; treat auto-review denials as concrete feedback to narrow or adjust the next step.",
	}
	if len(agents) > 0 {
		lines = append(lines, "- Use SPAWN for bounded child ACP work that can run independently; use task wait, cancel, or write to control yielded work.")
	}
	return strings.Join(lines, "\n")
}

func runtimePermissionInstructions() string {
	return strings.TrimSpace(`## Shell Tool Permissions
- Run normal inspection, builds, tests, and workspace file edits with default sandbox permissions.
- Git/VCS/control metadata writes, including git add, git commit, tags, merges, rebases, and writes under .git or similar control directories, must use run_command with sandbox_permissions=require_escalated and a concise justification.
- Do not repair permission or lock errors by deleting lock files, resetting state, changing ACLs/modes, or requesting write access to protected control directories. If the original operation is necessary, rerun only that operation with escalation.`)
}

func userCustomInstructions(basePrompt string, agentFiles []appresources.AgentFile) string {
	sections := make([]string, 0, 3)
	if text := normalizePromptText(basePrompt); text != "" {
		sections = append(sections, strings.Join([]string{"## Session Overrides", "", text}, "\n"))
	}
	if text := normalizePromptText(agentFileText(agentFiles, "agents.workspace")); text != "" {
		sections = append(sections, strings.Join([]string{"## Workspace Instructions", "", text}, "\n"))
	}
	if text := normalizePromptText(agentFileText(agentFiles, "agents.global")); text != "" {
		sections = append(sections, strings.Join([]string{"## Global Instructions", "", text}, "\n"))
	}
	if len(sections) == 0 {
		return ""
	}
	if len(sections) == 1 {
		return sections[0]
	}
	return "Session overrides workspace instructions, and workspace instructions override global instructions on conflict.\n\n" + strings.Join(sections, "\n\n")
}

func agentFileText(agentFiles []appresources.AgentFile, id string) string {
	for _, file := range agentFiles {
		if strings.TrimSpace(file.ID) == id {
			return file.Text
		}
	}
	return ""
}

func agentFilePromptIDs(agentFiles []appresources.AgentFile) map[string]bool {
	out := map[string]bool{}
	for _, file := range agentFiles {
		if id := strings.TrimSpace(file.ID); id != "" {
			out[id] = true
		}
	}
	return out
}

func environmentContext(workspaceDir string) (string, error) {
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return "", nil
	}
	cwd, err := filepath.Abs(workspaceDir)
	if err != nil {
		return "", fmt.Errorf("app/prompt: resolve workspace dir %s: %w", workspaceDir, err)
	}
	return fmt.Sprintf(`<environment_context>
  <cwd>%s</cwd>
  <shell>%s</shell>
</environment_context>`, filepath.Clean(cwd), currentShellName()), nil
}

func currentShellName() string {
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		return "unknown"
	}
	base := strings.TrimSpace(filepath.Base(shell))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return shell
	}
	return base
}

func normalizePromptText(text string) string {
	return strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
}

func renderSection(id string, text string) string {
	id = strings.TrimSpace(id)
	text = strings.TrimSpace(text)
	if id == "" {
		return text
	}
	return "### " + id + "\n" + text
}

func renderInstructionBlock(tag string, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return "<" + tag + ">\n" + text + "\n</" + tag + ">"
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
