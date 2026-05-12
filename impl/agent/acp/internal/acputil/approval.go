package acputil

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
)

func AutoApproveAllOnce(
	mode string,
	agent string,
	req client.RequestPermissionRequest,
) (client.RequestPermissionResponse, bool) {
	trimmedAgent := strings.TrimSpace(agent)
	if strings.EqualFold(trimmedAgent, "self") {
		return client.RequestPermissionResponse{}, false
	}
	return client.ResolveApproveAllOnce(mode, trimmedAgent, req).AutoResponse()
}

func SelectedOutcome(
	outcome string,
	optionID string,
) (client.RequestPermissionResponse, bool) {
	if !strings.EqualFold(strings.TrimSpace(outcome), "selected") {
		return client.RequestPermissionResponse{}, false
	}
	optionID = strings.TrimSpace(optionID)
	if optionID == "" {
		return client.RequestPermissionResponse{}, false
	}
	return client.PermissionSelectedOutcome(optionID), true
}

func RejectOnce() client.RequestPermissionResponse {
	return client.PermissionSelectedOutcome("reject_once")
}

func ToolCallName(update client.ToolCallUpdate) string {
	if output, ok := update.RawOutput.(map[string]any); ok {
		if name, _ := output["name"].(string); strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	if input, ok := update.RawInput.(map[string]any); ok {
		if name, _ := input["name"].(string); strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	return "UNKNOWN"
}
