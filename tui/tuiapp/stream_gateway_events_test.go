package tuiapp

import (
	"testing"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
)

func TestGatewayEventHelpersUseCanonicalOrigin(t *testing.T) {
	t.Parallel()

	ev := appgateway.Event{
		Origin: &appgateway.EventOrigin{
			Scope:         appgateway.EventScopeSubagent,
			ScopeID:       "task-1",
			ParticipantID: "participant-1",
		},
		ToolCall: &appgateway.ToolCallPayload{
			ToolName: "READ",
		},
	}

	if got := gatewayEventScope(ev); got != ACPProjectionSubagent {
		t.Fatalf("gatewayEventScope() = %q, want %q", got, ACPProjectionSubagent)
	}
	if got := gatewayEventScopeID(ev); got != "task-1" {
		t.Fatalf("gatewayEventScopeID() = %q, want %q", got, "task-1")
	}
	if got := gatewayParticipantID(ev); got != "participant-1" {
		t.Fatalf("gatewayParticipantID() = %q, want %q", got, "participant-1")
	}
}

func TestGatewayNoticeTextUsesCanonicalNarrativeOnly(t *testing.T) {
	t.Parallel()

	ev := appgateway.Event{
		Kind: appgateway.EventKindNotice,
		Narrative: &appgateway.NarrativePayload{
			Role: appgateway.NarrativeRoleNotice,
			Text: "gateway notice",
		},
	}

	if got := gatewayNoticeText(ev); got != "gateway notice" {
		t.Fatalf("gatewayNoticeText() = %q, want %q", got, "gateway notice")
	}
}
