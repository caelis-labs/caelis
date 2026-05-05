package gatewayapp

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sdkdelegation "github.com/OnslaughtSnail/caelis/sdk/delegation"
	sdkskill "github.com/OnslaughtSnail/caelis/sdk/skill"
)

const (
	globalAgentsFilePath = "~/.agents/AGENTS.md"
	workspaceAgentsFile  = "AGENTS.md"
)

type promptConfig struct {
	AppName          string
	WorkspaceDir     string
	BasePrompt       string
	SkillDirs        []string
	DelegationAgents []sdkdelegation.Agent
}

type promptFragmentKind string

const (
	promptFragmentSystem   promptFragmentKind = "system"
	promptFragmentUser     promptFragmentKind = "user"
	promptFragmentContext  promptFragmentKind = "context"
	promptFragmentMetadata promptFragmentKind = "metadata"
)

type promptFragment struct {
	Kind    promptFragmentKind
	Stage   string
	Source  string
	Content string
}

type SkillMeta = sdkskill.Meta

func buildSystemPrompt(cfg promptConfig) (string, error) {
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
	fragments := []promptFragment{
		{
			Kind:    promptFragmentSystem,
			Stage:   "identity",
			Source:  "app:built-in-identity",
			Content: builtInSystemIdentityPrompt(cfg.AppName),
		},
		{
			Kind:    promptFragmentSystem,
			Stage:   "capability_guidance",
			Source:  "app:role-guidance",
			Content: builtInRolePrompt(),
		},
		{
			Kind:    promptFragmentSystem,
			Stage:   "capability_guidance",
			Source:  "app:capability-guidance",
			Content: builtInCapabilityGuidancePrompt(cfg.DelegationAgents),
		},
		{
			Kind:    promptFragmentUser,
			Stage:   "user_custom_instructions",
			Source:  "app:user-custom-instructions",
			Content: buildUserCustomInstructionsPrompt(cfg.BasePrompt, workspaceAgents, globalAgents),
		},
		{
			Kind:    promptFragmentContext,
			Stage:   "dynamic_runtime_context",
			Source:  "app:workspace-context",
			Content: builtInEnvironmentContextPrompt(workspaceDir),
		},
		{
			Kind:    promptFragmentMetadata,
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
		"You are " + name + ", a coding-oriented assistant working in the user's workspace.",
		"Prefer a tight loop: understand the goal, inspect the minimum context, act, verify, then report.",
		"Prefer direct progress over discussion. Ask only when a missing answer would materially change the next step.",
		"Use the least powerful tool that can finish the step. Prefer read/search before write and avoid unnecessary retries.",
		"When a tool fails with a recoverable error, correct the call and continue instead of abandoning the path.",
		"Keep outputs concise, factual, and action-oriented. Do not restate long context unless it changes the next action.",
		"Be token disciplined: keep instructions compact, avoid repeating stable rules, and preserve only active state.",
	}, "\n")
}

func builtInRolePrompt() string {
	return strings.Join([]string{
		"## Main Session Role",
		"",
		"You are the primary session responsible for end-to-end progress.",
		"Choose tools, plan when useful, and integrate results into one user-facing answer.",
	}, "\n")
}

func builtInCapabilityGuidancePrompt(agents []sdkdelegation.Agent) string {
	lines := []string{
		"## Capability Guidance",
		"",
		"- Tool families: use READ/SEARCH/GLOB/LIST to inspect, WRITE/PATCH for targeted file changes, BASH for shell work, and TASK for async follow-up.",
		"- Skills: load a skill only when its description clearly matches the current task; read the minimum needed from its `SKILL.md`.",
		"- Modes: obey active session mode rules and avoid leaking planning-only behavior into execution turns.",
	}
	if len(agents) > 0 {
		lines = append(lines,
			"- Delegation: use SPAWN for bounded child ACP work that can run independently. Use TASK wait for progress, TASK cancel to stop a running child, and TASK write to send stdin to a running BASH task or a follow-up prompt to a completed SPAWN child.",
		)
	}
	return strings.Join(lines, "\n")
}

func builtInEnvironmentContextPrompt(workspaceDir string) string {
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return ""
	}
	return fmt.Sprintf(`<environment_context>
  <cwd>%s</cwd>
  <shell>%s</shell>
  <current_date>%s</current_date>
  <timezone>%s</timezone>
</environment_context>`, workspaceDir, currentShellName(), time.Now().Format("2006-01-02"), currentTimezoneLabel())
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

func currentTimezoneLabel() string {
	now := time.Now()
	name, offsetSeconds := now.Zone()
	name = strings.TrimSpace(name)
	if name == "" {
		name = now.Location().String()
	}
	sign := "+"
	if offsetSeconds < 0 {
		sign = "-"
		offsetSeconds = -offsetSeconds
	}
	hours := offsetSeconds / 3600
	minutes := (offsetSeconds % 3600) / 60
	return fmt.Sprintf("%s %s%02d:%02d", name, sign, hours, minutes)
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

func buildSkillsMetaPrompt(metas []sdkskill.Meta) string {
	if len(metas) == 0 {
		return ""
	}
	var b bytes.Buffer
	b.WriteString("## Skills\n")
	b.WriteString("Use a skill only when its description clearly matches the task. Read the minimum needed from its `SKILL.md`.\n")
	b.WriteString("### Available skills\n")
	for _, meta := range metas {
		fmt.Fprintf(&b, "- %s: %s (file: %s)\n", meta.Name, meta.Description, meta.Path)
	}
	return strings.TrimSpace(b.String())
}

func renderPromptFragments(fragments []promptFragment) string {
	systemFragments := make([]promptFragment, 0, len(fragments))
	userFragments := make([]promptFragment, 0, len(fragments))
	contextFragments := make([]promptFragment, 0, len(fragments))
	metadataFragments := make([]promptFragment, 0, len(fragments))
	for _, fragment := range fragments {
		if normalizePromptText(fragment.Content) == "" {
			continue
		}
		switch fragment.Kind {
		case promptFragmentUser:
			userFragments = append(userFragments, fragment)
		case promptFragmentContext:
			contextFragments = append(contextFragments, fragment)
		case promptFragmentMetadata:
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

func renderInstructionBlock(tag string, fragments []promptFragment) string {
	body := renderRawFragments(fragments)
	if body == "" {
		return ""
	}
	return "<" + tag + ">\n" + body + "\n</" + tag + ">"
}

func renderRawFragments(fragments []promptFragment) string {
	parts := make([]string, 0, len(fragments))
	for _, fragment := range fragments {
		if text := normalizePromptText(fragment.Content); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func DefaultSkillDiscoveryDirs(workspaceDir string) []string {
	return sdkskill.DefaultDiscoveryDirs(workspaceDir)
}

func DiscoverSkillMeta(dirs []string, workspaceDir string) ([]SkillMeta, error) {
	return sdkskill.DiscoverMeta(dirs, workspaceDir)
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
