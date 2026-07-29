package agents

import (
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
)

const (
	directRunSourcePrefix     = "slash_profile_"
	customRoleRunSourcePrefix = "slash_role_"
)

// IsRecoverableSourceHandle reports whether handle can be represented by the
// typed direct-run participant source. It does not assert that the handle is
// still present or bound in the current configuration.
func IsRecoverableSourceHandle(handle agentbinding.Handle) bool {
	handle = agentbinding.NormalizeHandle(handle)
	return agentbinding.IsDirectRun(handle) || agentbinding.ValidateCustomHandle(handle) == nil
}

// DirectRunSource returns the durable participant source for one fixed direct
// handle. Non-addressable handles return an empty source.
func DirectRunSource(handle agentbinding.Handle) string {
	handle = agentbinding.NormalizeHandle(handle)
	if agentbinding.IsDirectRun(handle) {
		return directRunSourcePrefix + string(handle)
	}
	return ""
}

// CustomRoleRunSource returns the durable participant source for one custom
// role after Control has confirmed that the role is configured.
func CustomRoleRunSource(handle agentbinding.Handle) string {
	handle = agentbinding.NormalizeHandle(handle)
	if agentbinding.ValidateCustomHandle(handle) == nil {
		return customRoleRunSourcePrefix + string(handle)
	}
	return ""
}

// DirectRunHandleFromSource recovers a syntactically valid handle from a typed
// Control participant source. It intentionally does not consult current
// bindings so an already-attached run remains addressable after configuration
// changes. Raw external Agent names are not accepted.
func DirectRunHandleFromSource(source string) (agentbinding.Handle, bool) {
	source = strings.ToLower(strings.TrimSpace(source))
	switch {
	case strings.HasPrefix(source, directRunSourcePrefix):
		handle := agentbinding.NormalizeHandle(agentbinding.Handle(strings.TrimPrefix(source, directRunSourcePrefix)))
		return handle, IsRecoverableSourceHandle(handle) && agentbinding.IsDirectRun(handle)
	case strings.HasPrefix(source, customRoleRunSourcePrefix):
		handle := agentbinding.NormalizeHandle(agentbinding.Handle(strings.TrimPrefix(source, customRoleRunSourcePrefix)))
		return handle, IsRecoverableSourceHandle(handle) && !agentbinding.IsDirectRun(handle)
	default:
		return "", false
	}
}

// DirectRunFromParticipant projects one attached profile participant into its
// stable <handle>(<label>) address. Only ACP sidecars started through a
// configured direct handle are addressable.
func DirectRunFromParticipant(label, kind, role, source string) Run {
	handle, ok := DirectRunHandleFromSource(source)
	return Run{
		Name:        FormatRunName(string(handle), label),
		Agent:       string(handle),
		Addressable: ok && strings.EqualFold(strings.TrimSpace(kind), "acp") && strings.EqualFold(strings.TrimSpace(role), "sidecar"),
	}
}
