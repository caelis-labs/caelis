package acpagentbridge

import (
	"encoding/json"
	"fmt"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/acppermission"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// sdkPermissionRequestFromApproval keeps live ACP callback wire ownership in
// the Host bridge while reusing the durable compatibility projection.
func sdkPermissionRequestFromApproval(ref session.SessionRef, approval *session.ProtocolApproval, meta map[string]any) (acpsdk.RequestPermissionRequest, error) {
	request, err := acppermission.EncodePermissionRequest(ref, approval, meta)
	if err != nil {
		return acpsdk.RequestPermissionRequest{}, err
	}
	return sdkPermissionRequestFromSchema(request)
}

// sdkPermissionRequestFromSchema strictly normalizes a durable compatibility
// request without first reducing it to the narrower Runtime approval model.
func sdkPermissionRequestFromSchema(request eventstream.RequestPermissionRequest) (acpsdk.RequestPermissionRequest, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return acpsdk.RequestPermissionRequest{}, fmt.Errorf("internal/acpagentbridge: encode permission request: %w", err)
	}
	var wire acpsdk.RequestPermissionRequest
	if err := json.Unmarshal(raw, &wire); err != nil {
		return acpsdk.RequestPermissionRequest{}, fmt.Errorf("internal/acpagentbridge: normalize permission request: %w", err)
	}
	if err := wire.Validate(); err != nil {
		return acpsdk.RequestPermissionRequest{}, fmt.Errorf("internal/acpagentbridge: validate permission request: %w", err)
	}
	return wire, nil
}

func approvalResponseFromSDK(wire acpsdk.RequestPermissionResponse, approval *session.ProtocolApproval) agent.ApprovalResponse {
	if err := wire.Validate(); err != nil {
		return agent.ApprovalResponse{}
	}
	out := agent.ApprovalResponse{}
	switch {
	case wire.Outcome.Cancelled != nil:
		out.Outcome = "cancelled"
	case wire.Outcome.Selected != nil:
		out.Outcome = "selected"
		out.OptionID = strings.TrimSpace(string(wire.Outcome.Selected.OptionId))
	}
	if out.Outcome != "selected" || approval == nil {
		return out
	}
	for _, option := range approval.Options {
		if strings.TrimSpace(option.ID) == out.OptionID && strings.HasPrefix(strings.ToLower(strings.TrimSpace(option.Kind)), "allow") {
			out.Approved = true
			break
		}
	}
	return out
}
