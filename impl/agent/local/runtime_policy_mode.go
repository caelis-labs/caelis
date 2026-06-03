package local

import (
	"os"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/policy/presets"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/policy"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (r *Runtime) policyMode(spec agent.AgentSpec) string {
	mode := strings.TrimSpace(r.defaultPolicyMode)
	if raw, ok := spec.Metadata[policy.MetadataPolicyProfile].(string); ok {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			mode = trimmed
		}
	} else if raw, ok := spec.Metadata[policy.MetadataLegacyPolicyMode].(string); ok {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			mode = trimmed
		}
	}
	return normalizePolicyMode(mode)
}

func normalizePolicyMode(mode string) string {
	return presets.NormalizeModeName(mode)
}

func modeOptionsFromSession(activeSession session.Session, spec agent.AgentSpec) policy.ModeOptions {
	opts := policy.ModeOptions{
		WorkspaceRoot: strings.TrimSpace(activeSession.CWD),
		TempRoot:      os.TempDir(),
	}
	if opts.WorkspaceRoot == "" {
		opts.WorkspaceRoot = activeSession.CWD
	}
	if values, ok := stringSliceMetadata(spec.Metadata, policy.MetadataExtraReadRoots); ok {
		opts.ExtraReadRoots = values
	}
	if values, ok := stringSliceMetadata(spec.Metadata, policy.MetadataExtraWriteRoots); ok {
		opts.ExtraWriteRoots = values
	}
	if value, ok := boolMetadata(spec.Metadata, "policy_network_enabled"); ok {
		opts.NetworkEnabled = &value
	}
	return opts
}

func boolMetadata(meta map[string]any, key string) (bool, bool) {
	raw, ok := meta[key]
	if !ok || raw == nil {
		return false, false
	}
	switch typed := raw.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on", "enabled":
			return true, true
		case "false", "0", "no", "off", "disabled":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func stringSliceMetadata(meta map[string]any, key string) ([]string, bool) {
	raw, ok := meta[key]
	if !ok || raw == nil {
		return nil, false
	}
	switch typed := raw.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, one := range typed {
			if trimmed := strings.TrimSpace(one); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, one := range typed {
			text, ok := one.(string)
			if !ok {
				continue
			}
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out, true
	default:
		return nil, false
	}
}
