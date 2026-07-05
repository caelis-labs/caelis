package approval

import "strings"

// StateCurrentApprovalMode is the durable session-state key for a per-session
// approval routing override selected by the TUI.
const StateCurrentApprovalMode = "gateway.current_approval_mode"

// CurrentMode returns the normalized per-session approval mode, defaulting to
// auto-review when no override is present.
func CurrentMode(state map[string]any) Mode {
	return CurrentModeOrDefault(state, ModeAutoReview)
}

// CurrentModeOrDefault returns the normalized per-session approval mode when
// present; otherwise it normalizes fallback.
func CurrentModeOrDefault(state map[string]any, fallback Mode) Mode {
	if mode, ok := currentModeOverride(state); ok {
		return mode
	}
	return NormalizeMode(string(fallback))
}

func currentModeOverride(state map[string]any) (Mode, bool) {
	if mode := strings.TrimSpace(stringFromState(state, StateCurrentApprovalMode)); mode != "" {
		return NormalizeMode(mode), true
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
