package acpagentbridge

import (
	"testing"

	controlclient "github.com/caelis-labs/caelis/control/client"
)

func TestACPPresentationSnapshotDoesNotExposeApprovalRoutingAsACPMode(t *testing.T) {
	approval := controlclient.PresentationSnapshot{Modes: &controlclient.PresentationModeState{
		Target:         controlclient.PresentationModeTargetApproval,
		CurrentModeID:  "manual",
		AvailableModes: []controlclient.PresentationMode{{ID: "manual", Name: "Manual"}},
	}}
	if modes, _, _, _ := acpPresentationSnapshot(approval); modes != nil {
		t.Fatalf("approval routing projected as ACP mode: %#v", modes)
	}

	appOwned := approval
	appOwned.Modes = &controlclient.PresentationModeState{
		Target:         controlclient.PresentationModeTargetApp,
		CurrentModeID:  "focus",
		AvailableModes: []controlclient.PresentationMode{{ID: "focus", Name: "Focus"}},
	}
	modes, _, _, _ := acpPresentationSnapshot(appOwned)
	if modes == nil || modes.CurrentModeID != "focus" || len(modes.AvailableModes) != 1 {
		t.Fatalf("app-owned ACP modes = %#v", modes)
	}
}
