// Package prompt owns system prompt assembly from skills, agent profiles,
// and environment context.
//
// This is a Layer 3 (Control) sub-package of app/. It assembles the
// system prompt that the agent sees. It does not own tool execution
// or session persistence.
package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/skill"
)

// BlockKind identifies the type of a prompt block.
type BlockKind string

const (
	BlockKindSystem   BlockKind = "system"
	BlockKindUser     BlockKind = "user"
	BlockKindContext  BlockKind = "context"
	BlockKindMetadata BlockKind = "metadata"
)

// Block is a single block in the assembled system prompt.
type Block struct {
	Kind    BlockKind
	Source  string
	Content string
}

// Config holds the configuration for prompt assembly.
type Config struct {
	AppName       string
	WorkspaceDir  string
	Shell         string
	BasePrompt    string // session-level prompt override
	Skills        []skill.Bundle
	AgentProfiles []AgentProfile
}

// Assemble builds the complete system prompt from blocks.
func Assemble(cfg Config) string {
	var blocks []Block

	// 1. System identity.
	blocks = append(blocks, Block{
		Kind:    BlockKindSystem,
		Source:  "identity",
		Content: systemIdentityPrompt(cfg.AppName),
	})

	// 2. User custom instructions (base + workspace + global AGENTS.md).
	if cfg.BasePrompt != "" {
		blocks = append(blocks, Block{
			Kind:    BlockKindUser,
			Source:  "session",
			Content: cfg.BasePrompt,
		})
	}

	// 3. Agent profile instructions.
	for _, profile := range cfg.AgentProfiles {
		if profile.Instructions != "" {
			blocks = append(blocks, Block{
				Kind:    BlockKindUser,
				Source:  fmt.Sprintf("agent:%s", profile.ID),
				Content: profile.Instructions,
			})
		}
	}

	// 4. Skills metadata.
	if len(cfg.Skills) > 0 {
		blocks = append(blocks, Block{
			Kind:    BlockKindMetadata,
			Source:  "skills",
			Content: buildSkillsSection(cfg.Skills),
		})
	}

	// 5. Environment context.
	blocks = append(blocks, Block{
		Kind:    BlockKindContext,
		Source:  "environment",
		Content: environmentContext(cfg.WorkspaceDir, cfg.Shell),
	})

	// Render all blocks.
	return renderBlocks(blocks)
}

// ReadAGENTSFile reads an AGENTS.md file and returns its content.
// Returns empty string if the file doesn't exist.
func ReadAGENTSFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// DefaultAGENTSPaths returns the default AGENTS.md file paths.
func DefaultAGENTSPaths(workspaceDir string) []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".agents", "AGENTS.md"),
		filepath.Join(workspaceDir, "AGENTS.md"),
	}
}

// ─── Internal assembly functions ─────────────────────────────────────

func systemIdentityPrompt(appName string) string {
	return fmt.Sprintf(`You are %s, a terminal-first coding agent working in the user's workspace.
Your job is to turn each concrete request into a safe, minimal, verified workspace change or a grounded answer.

Preserve user work. Do not revert unrelated changes.
Prefer repository truth over assumptions. Read or search before editing.
Keep responses concise, factual, and useful.`, appName)
}

func buildSkillsSection(skills []skill.Bundle) string {
	var b strings.Builder
	b.WriteString("## Skills\n\n")
	b.WriteString("Use a skill only when its description clearly matches the task.\n\n")
	for _, s := range skills {
		b.WriteString(fmt.Sprintf("- %s: %s (file: %s)\n", s.Name, s.Description, s.Path))
	}
	return b.String()
}

func environmentContext(workspaceDir, shell string) string {
	s := shell
	if s == "" {
		s = "sh"
	}
	return fmt.Sprintf("<environment_context>\n  <cwd>%s</cwd>\n  <shell>%s</shell>\n</environment_context>", workspaceDir, s)
}

func renderBlocks(blocks []Block) string {
	var system, user, other []string

	for _, b := range blocks {
		switch b.Kind {
		case BlockKindSystem:
			system = append(system, b.Content)
		case BlockKindUser:
			user = append(user, b.Content)
		default:
			other = append(other, b.Content)
		}
	}

	var parts []string
	if len(system) > 0 {
		parts = append(parts, "<system_instructions>\n"+strings.Join(system, "\n\n")+"\n</system_instructions>")
	}
	if len(user) > 0 {
		parts = append(parts, "<user_custom_instructions>\n"+strings.Join(user, "\n\n")+"\n</user_custom_instructions>")
	}
	parts = append(parts, other...)

	return strings.Join(parts, "\n\n")
}
