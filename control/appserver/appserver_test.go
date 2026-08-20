package appserver

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

type appServerTestTasks struct {
	principal taskstream.Principal
}

func (s *appServerTestTasks) List(_ context.Context, principal taskstream.Principal, _ taskstream.ListRequest) (taskstream.ListResult, error) {
	s.principal = principal
	return taskstream.ListResult{}, nil
}

func (*appServerTestTasks) Events(context.Context, taskstream.Principal, taskstream.ReadRequest) (taskstream.Batch, error) {
	return taskstream.Batch{}, nil
}

func (*appServerTestTasks) Subscribe(context.Context, taskstream.Principal, taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	return taskstream.SubscribeResult{}, nil
}

func TestBindAppServerClientsIncludesPrincipalBoundTaskObservation(t *testing.T) {
	tasks := &appServerTestTasks{}
	services := appServerTestServices(tasks)
	clients, err := BindAppServerClients(services, Principal{ID: "owner", Roles: []string{"operator"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := clients.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Tasks.List(context.Background(), taskstream.ListRequest{SessionID: "session-a"}); err != nil {
		t.Fatal(err)
	}
	if tasks.principal.ID != "owner" || len(tasks.principal.Roles) != 1 || tasks.principal.Roles[0] != "operator" {
		t.Fatalf("Task principal = %#v", tasks.principal)
	}
}

func TestAppServerAggregateRejectsMissingTaskCapability(t *testing.T) {
	services := appServerTestServices(nil)
	if err := services.Validate(); err == nil {
		t.Fatal("Validate() accepted AppServer services without Task observation")
	}
	services.Tasks = &appServerTestTasks{}
	clients, err := BindAppServerClients(services, Principal{ID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	clients.Tasks = nil
	if err := clients.Validate(); err == nil {
		t.Fatal("Validate() accepted AppServer clients without Task observation")
	}
}

func appServerTestServices(tasks taskstream.Service) AppServerServices {
	return AppServerServices{
		Sessions:      struct{ Service }{},
		Participants:  struct{ ParticipantService }{},
		Status:        struct{ StatusService }{},
		Configuration: struct{ ConfigurationService }{},
		Agents:        struct{ AgentService }{},
		Completion:    struct{ CompletionService }{},
		Plugins:       struct{ PluginService }{},
		Presentation:  struct{ PresentationService }{},
		Terminal:      struct{ TerminalService }{},
		Tasks:         tasks,
	}
}

var _ taskstream.Service = (*appServerTestTasks)(nil)
