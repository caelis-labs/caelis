package gatewayapp

import (
	"context"
	"os"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/control/agentbinding"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/internal/productpaths"
)

const dangerouslySkipPermissionsModeLabel = "yolo"

type stackRuntimeConfig struct {
	ApprovalMode                string
	PolicyProfile               string
	DangerouslySkipPermissions  bool
	ContextWindow               int
	SystemPrompt                string
	ModelProfileID              string
	ModelProfileEffort          string
	Model                       ModelConfig
	SkillDirs                   []string
	PluginSkills                []skill.PluginBundle
	SkillCatalog                skill.Catalog
	Plugins                     []PluginConfig
	BaseAssembly                assembly.ResolvedAssembly
	Assembly                    assembly.ResolvedAssembly
	BaseMetadata                map[string]any
	EstimatedPromptPrefixTokens int
}

func (s *runtimeComposition) delegationAgentsForSpawn() []delegation.Agent {
	if s == nil {
		return delegationAgentsForBindings(agentbinding.Configuration{}, true)
	}
	snapshot, err := s.placementSnapshot(context.Background())
	if err != nil {
		return delegationAgentsForBindings(agentbinding.Configuration{}, true)
	}
	return delegationAgentsForBindings(snapshot.placement.Bindings, true)
}

func defaultStoreDir() string {
	return productpaths.DefaultStoreDir(mustGetwd())
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
		return string(kernelimpl.ApprovalModeManual)
	case "", "auto", "auto-review", "auto_review", "autoreview", "default", "plan", "full_control", "full_access":
		return string(kernelimpl.ApprovalModeAutoReview)
	default:
		return string(kernelimpl.ApprovalModeAutoReview)
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
