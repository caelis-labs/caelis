package acpingress

import (
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

// PermissionRequest projects the validated external-Agent request into the
// Control-owned approval payload. This is the sole boundary where the
// A validated standard ACP permission request becomes a Control DTO.
func PermissionRequest(in client.RequestPermissionRequest) (eventstream.RequestPermissionRequest, error) {
	if err := in.Validate(); err != nil {
		return eventstream.RequestPermissionRequest{}, fmt.Errorf("acp ingress: validate permission request: %w", err)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return eventstream.RequestPermissionRequest{}, fmt.Errorf("acp ingress: encode permission request: %w", err)
	}
	var out eventstream.RequestPermissionRequest
	if err := json.Unmarshal(raw, &out); err != nil {
		return eventstream.RequestPermissionRequest{}, fmt.Errorf("acp ingress: project permission request: %w", err)
	}
	return out, nil
}
