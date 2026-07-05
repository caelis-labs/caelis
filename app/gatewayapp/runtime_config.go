package gatewayapp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/assembly"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/ports/gateway"
)

type stackRuntimeConfig struct {
	ApprovalMode                string
	PolicyProfile               string
	ContextWindow               int
	SystemPrompt                string
	Model                       ModelConfig
	SkillDirs                   []string
	PluginSkills                []skill.PluginBundle
	SkillCatalog                skill.Catalog
	DisableBuiltInAgentProfiles bool
	Plugins                     []PluginConfig
	BaseAssembly                assembly.ResolvedAssembly
	Assembly                    assembly.ResolvedAssembly
	BaseMetadata                map[string]any
	EstimatedPromptPrefixTokens int
}

func delegationAgentsFromAssembly(assembly assembly.ResolvedAssembly) []delegation.Agent {
	out := make([]delegation.Agent, 0, len(assembly.Agents))
	for _, one := range assembly.Agents {
		if !isSpawnVisibleAgent(one) {
			continue
		}
		agent := delegation.NormalizeAgent(delegation.Agent{
			Name:        one.Name,
			Description: one.Description,
		})
		if agent.Name == "" {
			continue
		}
		out = append(out, agent)
	}
	return out
}

func delegationAgentsForSpawn(assembly assembly.ResolvedAssembly, _ []session.ParticipantBinding) []delegation.Agent {
	if len(assembly.Agents) == 0 {
		return nil
	}
	return delegationAgentsFromAssembly(assembly)
}

func isSpawnVisibleAgent(agent assembly.AgentConfig) bool {
	name := strings.TrimSpace(agent.Name)
	return strings.EqualFold(name, "self") || isSubagentProfileAgent(agent)
}

func systemPromptWithDelegationGuidance(systemPrompt string) string {
	systemPrompt = strings.TrimRight(strings.TrimSpace(systemPrompt), "\n")
	guidance := strings.Join([]string{
		"- Delegate only when the subtask has clear independent scope, useful parallelism, or a focused review/investigation role.",
		"- Make delegated prompts self-contained: goal, scope, constraints, edit permission, and expected output.",
		"- Keep architecture, integration, validation, and user-facing judgment in the main session.",
	}, "\n")
	if strings.Contains(systemPrompt, guidance) ||
		strings.Contains(systemPrompt, "Delegate only when the subtask has clear independent scope") ||
		strings.Contains(systemPrompt, "Delegate only bounded side work") ||
		strings.Contains(systemPrompt, "SPAWN for bounded child-agent work") ||
		strings.Contains(systemPrompt, "SPAWN for bounded child ACP work") {
		return systemPrompt
	}
	if systemPrompt == "" {
		return guidance
	}
	return systemPrompt + "\n" + guidance
}

func defaultStoreDir() string {
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".caelis")
	}
	cwd := mustGetwd()
	return filepath.Join(cwd, ".caelis")
}

func (s *Stack) rejectReconfigureWhileActive(action string) error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	return rejectReconfigureWithActiveTurns(s.currentGateway(), action)
}

func rejectReconfigureWithActiveTurns(gw *kernelimpl.Gateway, action string) error {
	if gw == nil {
		return nil
	}
	active := gw.ActiveTurns()
	if len(active) == 0 {
		return nil
	}
	sessions := make([]string, 0, len(active))
	for _, item := range active {
		if sessionID := strings.TrimSpace(item.SessionRef.SessionID); sessionID != "" {
			sessions = append(sessions, sessionID)
		}
	}
	label := strings.TrimSpace(action)
	if label == "" {
		label = "reconfigure runtime"
	}
	if len(sessions) > 0 {
		return fmt.Errorf(
			"gatewayapp: cannot %s while %d turn(s) are active (session(s): %s); wait for completion or interrupt the running turn first",
			label,
			len(active),
			strings.Join(dedupeNonEmptyStrings(sessions), ", "),
		)
	}
	return fmt.Errorf(
		"gatewayapp: cannot %s while %d turn(s) are active; wait for completion or interrupt the running turn first",
		label,
		len(active),
	)
}

func dedupeNonEmptyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func cloneStringSlicePreserveNil(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

func approvalMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "manual":
		return string(gateway.ApprovalModeManual)
	case "", "auto", "auto-review", "auto_review", "autoreview", "default", "plan", "full_control", "full_access":
		return string(gateway.ApprovalModeAutoReview)
	default:
		return string(gateway.ApprovalModeAutoReview)
	}
}

func policyMode(raw string) string {
	return policyProfile(raw)
}

func policyProfile(raw string) string {
	normalized := presets.NormalizeModeName(raw)
	if strings.TrimSpace(normalized) == "" {
		return presets.ModeDefault
	}
	return normalized
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func cloneMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func clonePluginConfigs(values []PluginConfig) []PluginConfig {
	if len(values) == 0 {
		return nil
	}
	return append([]PluginConfig(nil), values...)
}

func stringFromMap(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func mustGetwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}
