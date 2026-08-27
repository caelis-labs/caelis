package acpagentbridge

import (
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

func TestACPPresentationSnapshotDoesNotExposeApprovalRoutingAsACPMode(t *testing.T) {
	approval := appserver.PresentationSnapshot{Modes: &appserver.PresentationModeState{
		Target:         appserver.PresentationModeTargetApproval,
		CurrentModeID:  "manual",
		AvailableModes: []appserver.PresentationMode{{ID: "manual", Name: "Manual"}},
	}}
	if modes, _, _ := acpPresentationSnapshot(approval); modes != nil {
		t.Fatalf("approval routing projected as ACP mode: %#v", modes)
	}

	appOwned := approval
	appOwned.Modes = &appserver.PresentationModeState{
		Target:         appserver.PresentationModeTargetApp,
		CurrentModeID:  "focus",
		AvailableModes: []appserver.PresentationMode{{ID: "focus", Name: "Focus"}},
	}
	modes, _, _ := acpPresentationSnapshot(appOwned)
	if modes == nil || modes.CurrentModeId != "focus" || len(modes.AvailableModes) != 1 {
		t.Fatalf("app-owned ACP modes = %#v", modes)
	}
}

func TestACPPresentationConfigOptionsPublishesOnlyMutableSelectOptions(t *testing.T) {
	options := acpPresentationConfigOptions([]appserver.PresentationConfigOption{
		{
			Type: "select", ID: "model", Name: "Model", Category: "model", CurrentValue: "mimo",
			Options: []appserver.PresentationSelectOption{{Value: "mimo", Name: "MiMo"}},
		},
		{Type: "boolean", ID: "verbose", Name: "Verbose", CurrentValue: true},
	})
	if len(options) != 1 || options[0].Select == nil {
		t.Fatalf("projected config options = %#v, want only the mutable select variant", options)
	}
	if got := options[0].Select.CurrentValue; got != acpsdk.SessionConfigValueId("mimo") {
		t.Fatalf("select current value = %q, want mimo", got)
	}
}
