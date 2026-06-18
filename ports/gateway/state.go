package gateway

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/policy"
)

const (
	// StateCurrentModelAlias is the durable session-state key for a per-session
	// model reference selected by the TUI. Newer clients store stable model IDs
	// here; older session state may still contain visible model aliases.
	StateCurrentModelAlias = "gateway.current_model_alias"
	// StateCurrentApprovalMode is the durable session-state key for a
	// per-session approval routing override selected by the TUI.
	StateCurrentApprovalMode = "gateway.current_approval_mode"
	// StateCurrentPolicyProfile is the durable session-state key for a
	// per-session policy profile override.
	StateCurrentPolicyProfile = "gateway.current_policy_profile"
	// StateCurrentReasoningEffort is the durable session-state key for a
	// per-session reasoning effort override selected by the TUI.
	StateCurrentReasoningEffort = "gateway.current_reasoning_effort"
	// StateUsageAccounting is the durable non-invocation session-state key for
	// token usage bookkeeping that must not enter canonical prompt history.
	StateUsageAccounting = "gateway.usage.v1"
)

var unsupportedLegacyStateKeys = []string{
	"gateway.current_session_mode",
	"gateway.current_sandbox_mode",
}

type SurfaceClass string

const (
	SurfaceClassInteractive SurfaceClass = "interactive"
	SurfaceClassBatch       SurfaceClass = "batch"
)

func ClassifySurface(surface string) SurfaceClass {
	normalized := strings.ToLower(strings.TrimSpace(surface))
	switch {
	case normalized == "":
		return SurfaceClassInteractive
	case strings.HasPrefix(normalized, "headless"),
		strings.HasPrefix(normalized, "batch"),
		strings.HasPrefix(normalized, "cron"),
		strings.HasPrefix(normalized, "export"),
		strings.HasPrefix(normalized, "script"):
		return SurfaceClassBatch
	default:
		return SurfaceClassInteractive
	}
}

func CurrentModelAlias(state map[string]any) string {
	current, _ := state[StateCurrentModelAlias].(string)
	return strings.TrimSpace(current)
}

func CurrentReasoningEffort(state map[string]any) string {
	current, _ := state[StateCurrentReasoningEffort].(string)
	return strings.TrimSpace(current)
}

func CurrentSessionMode(state map[string]any) string {
	return string(CurrentApprovalMode(state))
}

func CurrentSessionModeOrDefault(state map[string]any, fallback string) string {
	return string(CurrentApprovalModeOrDefault(state, NormalizeApprovalMode(fallback)))
}

func CurrentPolicyProfile(state map[string]any) string {
	return policy.NormalizeProfileName(stringFromState(state, StateCurrentPolicyProfile))
}

// UnsupportedLegacyStateKey returns the first old session-state key that should
// no longer be interpreted as runtime configuration.
func UnsupportedLegacyStateKey(state map[string]any) string {
	for _, key := range unsupportedLegacyStateKeys {
		if strings.TrimSpace(stringFromState(state, key)) != "" {
			return key
		}
	}
	return ""
}

func currentApprovalModeOverride(state map[string]any) (ApprovalMode, bool) {
	if mode := strings.TrimSpace(stringFromState(state, StateCurrentApprovalMode)); mode != "" {
		return NormalizeApprovalMode(mode), true
	}
	return "", false
}

func stringFromState(state map[string]any, key string) string {
	if len(state) == 0 {
		return ""
	}
	value, _ := state[key].(string)
	return value
}
