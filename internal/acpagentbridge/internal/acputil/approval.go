package acputil

import (
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

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
	return selectedOutcome(optionID), true
}

func RejectOnce() client.RequestPermissionResponse {
	return selectedOutcome("reject_once")
}

func selectedOutcome(optionID string) client.RequestPermissionResponse {
	return client.RequestPermissionResponse{
		Outcome: acpsdk.NewRequestPermissionOutcomeSelected(acpsdk.PermissionOptionId(strings.TrimSpace(optionID))),
	}
}

func ToolCallName(update acpsdk.ToolCallUpdate) string {
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
	kind := ""
	if update.Kind != nil {
		kind = strings.TrimSpace(string(*update.Kind))
	}
	return kind
}
