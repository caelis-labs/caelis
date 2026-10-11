package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

type appServerTestTasks struct {
	principal taskstream.Principal
}

func (s *appServerTestTasks) List(_ context.Context, principal taskstream.Principal, _ taskstream.ListRequest) (taskstream.ListResult, error) {
	s.principal = principal
	return taskstream.ListResult{}, nil
}

func (*appServerTestTasks) Events(context.Context, taskstream.Principal, taskstream.ReadRequest) (taskstream.ReadResult, error) {
	return taskstream.ReadResult{}, nil
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

func TestApplicationScopeNeedsItsCanonicalAuthorityWhenBindingClients(t *testing.T) {
	principal := Principal{ID: "owner", ApplicationID: "app-1", ConnectionID: "connection-1"}
	encoded, err := json.Marshal(principal)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"id":"owner"}` {
		t.Fatalf("application scope leaked into JSON principal: %s", encoded)
	}
	if _, err := BindAppServerClients(appServerTestServices(&appServerTestTasks{}), principal); err == nil {
		t.Fatal("scoped application principal bound ordinary product clients")
	}
	for _, partial := range []Principal{{ID: "owner", ApplicationID: "app-1"}, {ID: "owner", ConnectionID: "connection-1"}} {
		if _, err := BindAppServerClients(appServerTestServices(&appServerTestTasks{}), partial); err == nil {
			t.Fatalf("partially scoped application principal %#v bound ordinary product clients", partial)
		}
	}
}

func TestBindApplicationClientsPreservesScopeForFocusedOwners(t *testing.T) {
	applications, principal, _, _, _ := newApplicationArchiveFixture(t)
	tasks := &appServerTestTasks{}
	services := appServerTestServices(tasks)
	services.Applications = applications
	services.UIPreferences.Store = &fakeUIPreferencesStore{}
	clients, err := BindAppServerClients(services, principal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.Tasks.List(t.Context(), taskstream.ListRequest{SessionID: "archive-session"}); err != nil {
		t.Fatal(err)
	}
	if tasks.principal.ApplicationID != principal.ApplicationID || tasks.principal.ConnectionID != principal.ConnectionID {
		t.Fatalf("Task scope was dropped: %#v", tasks.principal)
	}
	boundInputs := clients.SubagentInputs.(*boundSubagentInputClient)
	if !reflect.DeepEqual(boundInputs.principal, principal) {
		t.Fatalf("subagent scope was dropped: %#v", boundInputs.principal)
	}
	if _, err := clients.UIPreferences.LoadUIPreferences(t.Context()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("scoped clients observed global UI preferences: %v", err)
	}
	wrong := principal
	wrong.ConnectionID += "-foreign"
	if _, err := BindAppServerClients(services, wrong); err == nil {
		t.Fatal("unknown connection bound application clients")
	}
	scope, _ := ApplicationScope(principal)
	if err := applications.Store().Revoke(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if _, err := BindAppServerClients(services, principal); err != nil {
		t.Fatalf("revocation removed observation binding: %v", err)
	}
}

func TestBoundApplicationCloseRetainsOriginalArchiveReceipt(t *testing.T) {
	applications, principal, native, _, _ := newApplicationArchiveFixture(t)
	services := appServerTestServices(&appServerTestTasks{})
	services.Applications, services.Sessions = applications, native
	clients, err := BindAppServerClients(services, principal)
	if err != nil {
		t.Fatal(err)
	}
	req := CloseSessionRequest{WriteBase: WriteBase{OperationID: "bound-archive", SessionID: native.active.SessionID}}
	closed, err := clients.Sessions.CloseSession(t.Context(), req)
	if err != nil || closed.Outcome != OutcomeCommitted {
		t.Fatalf("close: %+v, %v", closed, err)
	}
	scope, _ := ApplicationScope(principal)
	binding, err := applications.Store().GetBinding(t.Context(), scope, req.SessionID)
	if err != nil || !binding.Archived {
		t.Fatalf("original Archive binding: %+v, %v", binding, err)
	}
	if retried, err := clients.Sessions.CloseSession(t.Context(), req); err != nil || retried.OperationID != closed.OperationID || native.closes != 1 {
		t.Fatalf("Archive receipt was redispatched: %+v, %v, closes=%d", retried, err, native.closes)
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
		SubagentInputs: &SubagentInputService{},
		UIPreferences:  &UIPreferencesService{},
		Sessions:       struct{ Service }{},
		Participants:   struct{ ParticipantService }{},
		Status:         struct{ StatusService }{},
		Configuration:  struct{ ConfigurationService }{},
		Agents:         struct{ AgentService }{},
		Completion:     struct{ CompletionService }{},
		Plugins:        struct{ PluginService }{},
		Presentation:   struct{ PresentationService }{},
		Terminal:       struct{ TerminalService }{},
		Tasks:          tasks,
	}
}

var _ taskstream.Service = (*appServerTestTasks)(nil)
