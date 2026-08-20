package controlserver

import (
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// testFocusedServices supplies capabilities that a focused HTTP test does not
// exercise. Tests that cover a capability replace the corresponding service.
type testFocusedServices struct {
	appserver.ParticipantService
	appserver.ConfigurationService
	appserver.AgentService
	appserver.CompletionService
	appserver.PluginService
	appserver.PresentationService
	appserver.TerminalService
}

func testAppServerServices(sessions appserver.Service, status appserver.StatusService) appserver.AppServerServices {
	focused := &testFocusedServices{}
	return appserver.AppServerServices{
		Sessions: sessions, Participants: focused, Status: status, Configuration: focused,
		Agents: focused, Completion: focused, Plugins: focused,
		Presentation: focused, Terminal: focused, Tasks: &fakeTaskService{},
	}
}
