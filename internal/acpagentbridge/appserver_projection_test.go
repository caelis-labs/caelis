package acpagentbridge

import (
	"testing"

	appserver "github.com/caelis-labs/caelis/control/appserver"
)

func TestACPPresentationSnapshotDoesNotExposeApprovalRoutingAsACPMode(t *testing.T) {
	approval := appserver.PresentationSnapshot{Modes: &appserver.PresentationModeState{
		Target:         appserver.PresentationModeTargetApproval,
		CurrentModeID:  "manual",
		AvailableModes: []appserver.PresentationMode{{ID: "manual", Name: "Manual"}},
	}}
	if modes, _, _, _ := acpPresentationSnapshot(approval); modes != nil {
		t.Fatalf("approval routing projected as ACP mode: %#v", modes)
	}

	appOwned := approval
	appOwned.Modes = &appserver.PresentationModeState{
		Target:         appserver.PresentationModeTargetApp,
		CurrentModeID:  "focus",
		AvailableModes: []appserver.PresentationMode{{ID: "focus", Name: "Focus"}},
	}
	modes, _, _, _ := acpPresentationSnapshot(appOwned)
	if modes == nil || modes.CurrentModeID != "focus" || len(modes.AvailableModes) != 1 {
		t.Fatalf("app-owned ACP modes = %#v", modes)
	}
}
