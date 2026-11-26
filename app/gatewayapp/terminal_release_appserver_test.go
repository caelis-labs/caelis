package gatewayapp_test

import (
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/streamspool"
)

func TestAppServerTerminalReleaseSealsTaskOutput(t *testing.T) {
	stack, req, assertSpoolState := gatewayapp.NewTerminalReleaseTestFixture(t)
	server, err := local.NewAppServer(stack)
	if err != nil {
		t.Fatal(err)
	}
	clients, err := server.Bind(appserver.Principal{ID: stack.UserID()})
	if err != nil {
		t.Fatal(err)
	}

	assertSpoolState(streamspool.StateOpen)
	if err := clients.Terminal.ReleaseTerminal(t.Context(), req); err != nil {
		t.Fatalf("release terminal: %v", err)
	}
	assertSpoolState(streamspool.StateSealed)
}
