package promptassembly

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/skill/fs"
	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const (
	globalAgentsFilePath = "~/.agents/AGENTS.md"
	workspaceAgentsFile  = "AGENTS.md"
)

type Config struct {
	AppName          string
	WorkspaceDir     string
	BasePrompt       string
	SkillDirs        []string
	DelegationAgents []delegation.Agent
}

type fragmentKind string

const (
	fragmentSystem   fragmentKind = "system"
	fragmentUser     fragmentKind = "user"
	fragmentContext  fragmentKind = "context"
	fragmentMetadata fragmentKind = "metadata"
)

type fragment struct {
	Kind    fragmentKind
	Stage   string
	Source  string
	Content string
}

type SkillMeta = fs.Meta

func BuildSystemPrompt(cfg Config) (string, error) {
	workspaceDir, err := resolvePromptPath(cfg.WorkspaceDir)
	if err != nil {
		return "", err
	}
	globalAgentsPath, err := resolvePromptPath(globalAgentsFilePath)
	if err != nil {
		return "", err
	}
	globalAgents, err := readOptionalPromptFile(globalAgentsPath)
	if err != nil {
		return "", err
	}
	workspaceAgents, err := readOptionalPromptFile(filepath.Join(workspaceDir, workspaceAgentsFile))
	if err != nil {
		return "", err
	}
	skills, err := discoverSkillMeta(cfg.SkillDirs, workspaceDir)
	if err != nil {
		return "", err
	}
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Path < skills[j].Path
	})
	fragments := []fragment{
		{
			Kind:    fragmentSystem,
			Stage:   "identity",
			Source:  "app:built-in-identity",
			Content: builtInSystemIdentityPrompt(cfg.AppName),
		},
		{
			Kind:    fragmentSystem,
			Stage:   "capability_guidance",
			Source:  "app:role-guidance",
			Content: builtInRolePrompt(),
		},
		{
			Kind:    fragmentSystem,
			Stage:   "capability_guidance",
			Source:  "app:capability-guidance",
			Content: builtInCapabilityGuidancePrompt(cfg.DelegationAgents),
		},
		{
			Kind:    fragmentSystem,
			Stage:   "permissions",
			Source:  "app:permission-boundaries",
			Content: builtInPermissionBoundariesPrompt(),
		},
		{
			Kind:    fragmentUser,
			Stage:   "user_custom_instructions",
			Source:  "app:user-custom-instructions",
			Content: buildUserCustomInstructionsPrompt(cfg.BasePrompt, workspaceAgents, globalAgents),
		},
		{
			Kind:    fragmentContext,
			Stage:   "dynamic_runtime_context",
			Source:  "app:workspace-context",
			Content: builtInEnvironmentContextPrompt(workspaceDir),
		},
		{
			Kind:    fragmentMetadata,
			Stage:   "skills_meta",
			Source:  "skills metadata",
			Content: buildSkillsMetaPrompt(skills),
		},
	}
	return renderPromptFragments(fragments), nil
}

func builtInSystemIdentityPrompt(appName string) string {
	name := strings.TrimSpace(appName)
	if name == "" {
		name = "caelis"
	}
	return strings.Join([]string{
		"## Core Stable Rules",
		"",
		"You are " + name + ", a terminal-first coding agent working in the user's workspace.",
		"Drive toward the user's concrete goal: inspect enough context, make the smallest useful change, verify, then report.",
		"Preserve user work. Do not revert unrelated changes; adapt to the existing code, architecture, and project boundaries.",
		"Prefer repository truth over assumptions. Read or search before editing, and use shell checks when they are the clearest verification path.",
		"Ask only when the answer cannot be discovered locally and would materially change the next action.",
		"Keep responses concise, factual, and focused on what changed, what was verified, and what remains.",
	}, "\n")
}

func builtInRolePrompt() string {
	return strings.Join([]string{
		"## Main Session Role",
		"",
		"Own architecture, task decomposition, integration, validation, and final judgment.",
		"Use plans for non-trivial work, keep them current, and close the loop with concrete verification.",
	}, "\n")
}

func builtInCapabilityGuidancePrompt(agents []delegation.Agent) string {
	lines := []string{
		"## Capability Guidance",
		"",
		"- Inspect with READ, SEARCH, GLOB, and LIST; edit with WRITE or PATCH; use BASH for shell-native work and TASK for yielded async work.",
		"- Use request_permissions for the smallest read/write path or network grant needed before retrying denied work.",
		"- Load a skill only when its description clearly matches the task; read only the needed parts of its `SKILL.md`.",
		"- Obey the active approval mode; treat auto-review denials as concrete feedback to narrow or adjust the next step.",
	}
	if len(agents) > 0 {
		lines = append(lines,
			delegationGuidanceLine(),
		)
	}
	return strings.Join(lines, "\n")
}

func builtInPermissionBoundariesPrompt() string {
	return strings.Join([]string{
		"## BASH Permissions",
		"",
		"- Start BASH commands with default sandbox permissions; workspace-local reads, builds, tests, and temp writes should stay default.",
		"- Use `sandbox_permissions=with_additional_permissions` for the smallest extra read/write path or network grant.",
		"- Use `sandbox_permissions=require_escalated` only when host execution is required; include a short `justification`.",
		"- If policy denies a command or file tool, narrow the scope or request the missing permission with request_permissions.",
	}, "\n")
}

func delegationGuidanceLine() string {
	return "- Use SPAWN for bounded child ACP work that can run independently; use TASK wait, cancel, or write to control yielded work."
}

func builtInEnvironmentContextPrompt(workspaceDir string) string {
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return ""
	}
	return fmt.Sprintf(`<environment_context>
  <cwd>%s</cwd>
  <shell>%s</shell>
</environment_context>`, workspaceDir, currentShellName())
}

func currentShellName() string {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		return "unknown"
	}
	base := filepath.Base(shell)
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return shell
	}
	return base
}

func buildUserCustomInstructionsPrompt(sessionPrompt string, workspaceAgents string, globalAgents string) string {
	sections := make([]string, 0, 3)
	if text := normalizePromptText(sessionPrompt); text != "" {
		sections = append(sections, strings.Join([]string{
			"## Session Overrides",
			"",
			text,
		}, "\n"))
	}
	if text := normalizePromptText(workspaceAgents); text != "" {
		sections = append(sections, strings.Join([]string{
			"## Workspace Instructions",
			"",
			text,
		}, "\n"))
	}
	if text := normalizePromptText(globalAgents); text != "" {
		sections = append(sections, strings.Join([]string{
			"## Global Instructions",
			"",
			text,
		}, "\n"))
	}
	if len(sections) == 0 {
		return ""
	}

	lines := []string{}
	if len(sections) > 1 {
		lines = append(lines, "Session overrides workspace instructions, and workspace instructions override global instructions on conflict.")
		lines = append(lines, "")
	}
	lines = append(lines, sections...)
	return strings.Join(lines, "\n\n")
}

func buildSkillsMetaPrompt(metas []fs.Meta) string {
	if len(metas) == 0 {
		return ""
	}
	var b bytes.Buffer
	b.WriteString("## Skills\n")
	b.WriteString("Use a skill only when its description clearly matches the task. Read the minimum needed from its `SKILL.md`.\n")
	b.WriteString("### Available skills\n")
	for _, meta := range metas {
		fmt.Fprintf(&b, "- %s: %s (file: %s)\n", promptSingleLine(meta.Name), promptSingleLine(meta.Description), strings.TrimSpace(meta.Path))
	}
	return strings.TrimSpace(b.String())
}

func promptSingleLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func renderPromptFragments(fragments []fragment) string {
	systemFragments := make([]fragment, 0, len(fragments))
	userFragments := make([]fragment, 0, len(fragments))
	contextFragments := make([]fragment, 0, len(fragments))
	metadataFragments := make([]fragment, 0, len(fragments))
	for _, fragment := range fragments {
		if normalizePromptText(fragment.Content) == "" {
			continue
		}
		switch fragment.Kind {
		case fragmentUser:
			userFragments = append(userFragments, fragment)
		case fragmentContext:
			contextFragments = append(contextFragments, fragment)
		case fragmentMetadata:
			metadataFragments = append(metadataFragments, fragment)
		default:
			systemFragments = append(systemFragments, fragment)
		}
	}

	parts := make([]string, 0, 4)
	if block := renderInstructionBlock("system_instructions", systemFragments); block != "" {
		parts = append(parts, block)
	}
	if block := renderInstructionBlock("user_custom_instructions", userFragments); block != "" {
		parts = append(parts, block)
	}
	if block := renderRawFragments(metadataFragments); block != "" {
		parts = append(parts, block)
	}
	if block := renderRawFragments(contextFragments); block != "" {
		parts = append(parts, block)
	}
	return strings.Join(parts, "\n\n")
}

func renderInstructionBlock(tag string, fragments []fragment) string {
	body := renderRawFragments(fragments)
	if body == "" {
		return ""
	}
	return "<" + tag + ">\n" + body + "\n</" + tag + ">"
}

func renderRawFragments(fragments []fragment) string {
	parts := make([]string, 0, len(fragments))
	for _, fragment := range fragments {
		if text := normalizePromptText(fragment.Content); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func DefaultSkillDiscoveryDirs(workspaceDir string) []string {
	return fs.DefaultDiscoveryDirs(workspaceDir)
}

func DiscoverSkillMeta(dirs []string, workspaceDir string) ([]SkillMeta, error) {
	return fs.DiscoverMeta(dirs, workspaceDir)
}

func ResolvePromptPath(path string) (string, error) {
	return resolvePromptPath(path)
}

func EstimateModelPromptPrefixTokens(metadata map[string]any, tools []tool.Tool) int {
	total := EstimatePromptTextTokens(stringFromMap(metadata, "system_prompt"))
	total += EstimateToolPromptTokens(tools)
	if total > 0 {
		total += 96
	}
	return total
}

func EstimateToolPromptTokens(tools []tool.Tool) int {
	specs := tool.ModelSpecs(tools)
	if len(specs) == 0 {
		return 0
	}
	raw, err := json.Marshal(specs)
	if err != nil {
		return len(specs) * 64
	}
	return EstimatePromptTextTokens(string(raw)) + len(specs)*24
}

func EstimatePromptTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	runes := len([]rune(text))
	tokens := runes / 4
	if runes%4 != 0 {
		tokens++
	}
	if tokens < 1 {
		return 1
	}
	return tokens
}

func discoverSkillMeta(dirs []string, workspaceDir string) ([]SkillMeta, error) {
	return DiscoverSkillMeta(dirs, workspaceDir)
}

func readOptionalPromptFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return normalizePromptText(string(raw)), nil
}

func resolvePromptPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("empty prompt path")
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path), nil
}

func normalizePromptText(input string) string {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	input = strings.TrimPrefix(input, "\ufeff")
	return strings.TrimSpace(input)
}

func stringFromMap(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}
