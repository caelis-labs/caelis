package agents

import (
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
)

const directRunSourcePrefix = "slash_profile_"

// DirectRunSource returns the durable participant source for one fixed direct
// handle. Non-addressable handles return an empty source.
func DirectRunSource(handle agentbinding.Handle) string {
	handle = agentbinding.NormalizeHandle(handle)
	if !agentbinding.IsDirectRun(handle) {
		return ""
	}
	return directRunSourcePrefix + string(handle)
}

// DirectRunHandleFromSource recovers a fixed handle from a typed Control
// participant source. Raw external Agent names are intentionally not accepted.
func DirectRunHandleFromSource(source string) (agentbinding.Handle, bool) {
	source = strings.ToLower(strings.TrimSpace(source))
	handle := agentbinding.NormalizeHandle(agentbinding.Handle(strings.TrimPrefix(source, directRunSourcePrefix)))
	return handle, strings.HasPrefix(source, directRunSourcePrefix) && agentbinding.IsDirectRun(handle)
}

// DirectRunFromParticipant projects one attached profile participant into its
// stable <handle>(<label>) address. Only ACP sidecars started through a fixed
// direct handle are addressable.
func DirectRunFromParticipant(label, kind, role, source string) Run {
	handle, ok := DirectRunHandleFromSource(source)
	return Run{
		Name:        FormatRunName(string(handle), label),
		Agent:       string(handle),
		Addressable: ok && strings.EqualFold(strings.TrimSpace(kind), "acp") && strings.EqualFold(strings.TrimSpace(role), "sidecar"),
	}
}
