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
	if raw, ok := spec.Metadata["policy_mode"].(string); ok {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			mode = trimmed
		}
	}
	normalized := normalizePolicyMode(mode)
	if boolMetadata(spec.Metadata, "sandbox_auto_review_disabled") && normalized == presets.ModeAutoReview {
		return presets.ModeManual
	}
	return normalized
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
	if values, ok := stringSliceMetadata(spec.Metadata, "policy_extra_read_roots"); ok {
		opts.ExtraReadRoots = values
	}
	if values, ok := stringSliceMetadata(spec.Metadata, "policy_extra_write_roots"); ok {
		opts.ExtraWriteRoots = values
	}
	return opts
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

func boolMetadata(meta map[string]any, key string) bool {
	if meta == nil {
		return false
	}
	switch typed := meta[key].(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	default:
		return false
	}
}
