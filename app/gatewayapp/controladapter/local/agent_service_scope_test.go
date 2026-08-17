package local

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

func TestAgentBindingStatusRejectsSessionScopeBeforeProjection(t *testing.T) {
	service := &AgentService{}
	_, err := service.AgentBindingStatus(context.Background(), appserver.Principal{ID: "owner"}, appserver.AgentRequest{
		SessionID: "session-1",
	})
	if errorcode.CodeOf(err) != errorcode.InvalidArgument || !strings.Contains(err.Error(), "Host-scoped") {
		t.Fatalf("AgentBindingStatus(Session) error = %v, want Host-scoped invalid argument", err)
	}
}

func TestDisconnectCandidatesRejectsSessionScopeBeforeProjection(t *testing.T) {
	service := &AgentService{}
	_, err := service.DisconnectCandidates(context.Background(), appserver.Principal{ID: "owner"}, appserver.AgentRequest{
		SessionID: "session-1",
	})
	if errorcode.CodeOf(err) != errorcode.InvalidArgument || !strings.Contains(err.Error(), "Host-scoped") {
		t.Fatalf("DisconnectCandidates(Session) error = %v, want Host-scoped invalid argument", err)
	}
}
