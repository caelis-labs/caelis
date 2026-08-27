package acp

import (
	"encoding/json"
	"fmt"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	protocolacp "github.com/caelis-labs/caelis/protocol/acp/schema"
)

func permissionRequestToSDK(request protocolacp.RequestPermissionRequest) (acpsdk.RequestPermissionRequest, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return acpsdk.RequestPermissionRequest{}, fmt.Errorf("acp surface: encode permission request: %w", err)
	}
	var wire acpsdk.RequestPermissionRequest
	if err := json.Unmarshal(raw, &wire); err != nil {
		return acpsdk.RequestPermissionRequest{}, fmt.Errorf("acp surface: normalize permission request: %w", err)
	}
	if err := wire.Validate(); err != nil {
		return acpsdk.RequestPermissionRequest{}, fmt.Errorf("acp surface: validate permission request: %w", err)
	}
	return wire, nil
}

func permissionResponseFromSDK(wire acpsdk.RequestPermissionResponse) (protocolacp.RequestPermissionResponse, error) {
	if err := wire.Validate(); err != nil {
		return protocolacp.RequestPermissionResponse{}, fmt.Errorf("acp surface: validate permission response: %w", err)
	}
	switch {
	case wire.Outcome.Cancelled != nil:
		return protocolacp.RequestPermissionResponse{
			Outcome: protocolacp.PermissionOutcome{Outcome: "cancelled"},
		}, nil
	case wire.Outcome.Selected != nil:
		return protocolacp.RequestPermissionResponse{
			Outcome: protocolacp.PermissionOutcome{
				Outcome:  "selected",
				OptionID: string(wire.Outcome.Selected.OptionId),
			},
		}, nil
	default:
		return protocolacp.RequestPermissionResponse{}, fmt.Errorf("acp surface: permission response has no outcome")
	}
}
