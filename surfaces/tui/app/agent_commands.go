package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// agentCommandBindings leaves conflicting custom roles in configuration while
// keeping them out of slash discovery. Built-in profiles still supply their
// bindings and descriptions to the corresponding direct-run commands.
func agentCommandBindings(status agentbinding.Status) agentbinding.Status {
	handles := make([]agentbinding.HandleStatus, 0, len(status.Handles))
	for _, item := range status.Handles {
		if item.Definition.Custom && controlprompt.IsKnown(string(item.Definition.Handle)) {
			continue
		}
		handles = append(handles, item)
	}
	status.Handles = handles
	return status
}

func agentProfileCommandDetail(status agentbinding.HandleStatus) string {
	description := strings.TrimSpace(status.Definition.Description)
	if strings.TrimSpace(status.Binding.ProfileID) == "" {
		return strings.Join(compactNonEmpty([]string{description, "unbound · configure with /team"}), " · ")
	}
	target := firstNonEmpty(strings.TrimSpace(status.Profile.DisplayName), strings.TrimSpace(status.Binding.ProfileID))
	if effort := strings.TrimSpace(status.Binding.Effort); effort != "" {
		target += " [" + effort + "]"
	}
	return strings.Join(compactNonEmpty([]string{description, target}), " · ")
}
