package controlserver

import (
	controlclient "github.com/caelis-labs/caelis/control/client"
)

// testFocusedServices supplies capabilities that a focused HTTP test does not
// exercise. Tests that cover a capability replace the corresponding service.
type testFocusedServices struct {
	controlclient.ParticipantService
	controlclient.ConfigurationService
	controlclient.AgentService
	controlclient.CompletionService
	controlclient.PluginService
}

func testAppServerServices(sessions controlclient.Service, status controlclient.StatusService) controlclient.AppServerServices {
	focused := &testFocusedServices{}
	return controlclient.AppServerServices{
		Sessions: sessions, Participants: focused, Status: status, Configuration: focused,
		Agents: focused, Completion: focused, Plugins: focused,
	}
}
