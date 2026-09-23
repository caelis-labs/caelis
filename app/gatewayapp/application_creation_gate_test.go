package gatewayapp

import (
	"testing"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

func TestGenericSessionCreationOnlyReservesApplicationAndRetiredProductAuthority(t *testing.T) {
	for _, tc := range []struct {
		name     string
		req      appserver.CreateSessionRequest
		reserved bool
	}{
		{name: "ordinary"},
		{name: "spawned subagent", req: appserver.CreateSessionRequest{Metadata: map[string]any{sessionvisibility.MetadataSystemManagedAgent: "subagent"}}},
		{name: "other system managed", req: appserver.CreateSessionRequest{Metadata: map[string]any{sessionvisibility.MetadataSystemManagedAgent: "guardian"}}},
		{name: "application", req: appserver.CreateSessionRequest{Metadata: map[string]any{sessionvisibility.MetadataSystemManagedAgent: application.MetadataKind}}, reserved: true},
		{name: "application record", req: appserver.CreateSessionRequest{Metadata: map[string]any{application.StateKey: "forged"}}, reserved: true},
		{name: "application id", req: appserver.CreateSessionRequest{PreferredSessionID: "application-forged"}, reserved: true},
		{name: "retired chat", req: appserver.CreateSessionRequest{Metadata: map[string]any{sessionvisibility.MetadataSystemManagedAgent: "bot"}}, reserved: true},
		{name: "retired work", req: appserver.CreateSessionRequest{PreferredSessionID: "bot-work-forged"}, reserved: true},
		{name: "retired id", req: appserver.CreateSessionRequest{Metadata: map[string]any{"control_bot_id": "forged"}}, reserved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reservedApplicationOrRetiredCreation(tc.req); got != tc.reserved {
				t.Fatalf("reserved = %v, want %v", got, tc.reserved)
			}
		})
	}
}
