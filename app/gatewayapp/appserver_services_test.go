package gatewayapp

import (
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

type gatewayTestFocusedServices struct {
	appserver.ParticipantService
	appserver.ConfigurationService
	appserver.AgentService
	appserver.CompletionService
	appserver.PluginService
	appserver.PresentationService
	appserver.TerminalService
}

func gatewayTestAppServerServices(sessions appserver.Service, status appserver.StatusService, tasks ...taskstream.Service) appserver.AppServerServices {
	focused := &gatewayTestFocusedServices{}
	taskService := taskstream.Service(controlClientNoopTaskStreams{})
	if len(tasks) > 0 {
		taskService = tasks[0]
	}
	return appserver.AppServerServices{
		Sessions: sessions, Participants: focused, Status: status, Configuration: focused,
		Agents: focused, Completion: focused, Plugins: focused,
		Presentation: focused, Terminal: focused, Tasks: taskService,
	}
}
