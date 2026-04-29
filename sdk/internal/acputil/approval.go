package acputil

import (
	"strings"

	sdkacpclient "github.com/OnslaughtSnail/caelis/acp/client"
)

func AutoApproveAllOnce(
	mode string,
	agent string,
	req sdkacpclient.RequestPermissionRequest,
) (sdkacpclient.RequestPermissionResponse, bool) {
	trimmedAgent := strings.TrimSpace(agent)
	if strings.EqualFold(trimmedAgent, "self") {
		return sdkacpclient.RequestPermissionResponse{}, false
	}
	return sdkacpclient.ResolveApproveAllOnce(mode, trimmedAgent, req).AutoResponse()
}

func SelectedOutcome(
	outcome string,
	optionID string,
) (sdkacpclient.RequestPermissionResponse, bool) {
	if !strings.EqualFold(strings.TrimSpace(outcome), "selected") {
		return sdkacpclient.RequestPermissionResponse{}, false
	}
	optionID = strings.TrimSpace(optionID)
	if optionID == "" {
		return sdkacpclient.RequestPermissionResponse{}, false
	}
	return sdkacpclient.PermissionSelectedOutcome(optionID), true
}

func RejectOnce() sdkacpclient.RequestPermissionResponse {
	return sdkacpclient.PermissionSelectedOutcome("reject_once")
}

func ToolCallName(update sdkacpclient.ToolCallUpdate) string {
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
