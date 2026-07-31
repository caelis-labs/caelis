package controlserver

import (
	"context"
	"net/http"
	"testing"

	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/client/httpclient"
	controlstatus "github.com/caelis-labs/caelis/control/status"
)

func TestFocusedClientsRoundTripThroughHTTPAppServer(t *testing.T) {
	participants := &focusedParticipantService{}
	configuration := &focusedConfigurationService{}
	agents := &focusedAgentService{}
	completion := &focusedCompletionService{}
	plugins := &focusedPluginService{}
	services := testAppServerServices(&fakeService{}, staticStatusService{})
	services.Participants = participants
	services.Configuration = configuration
	services.Agents = agents
	services.Completion = completion
	services.Plugins = plugins
	server, err := New(HandlerConfig{
		Services: services, TaskStreams: &fakeTaskService{}, Authenticator: testAuthenticator(),
		AllowedHosts: []string{"127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := httpclient.New(httpclient.Config{
		BaseURL: "http://127.0.0.1", BearerToken: "test-token",
		HTTPClient: &http.Client{Transport: controlHandlerRoundTripper{handler: server}},
	})
	if err != nil {
		t.Fatal(err)
	}

	handles, err := client.Handles(context.Background(), "session-1")
	if err != nil || len(handles) != 1 || handles[0] != "review" {
		t.Fatalf("Handles() = %#v, %v", handles, err)
	}
	status, err := client.ConfigureSessionMode(context.Background(), controlclient.SessionModeRequest{SessionID: "session-1", Mode: "manual", Surface: "pet"})
	if err != nil || status.Session.ID != "session-1" {
		t.Fatalf("ConfigureSessionMode() = %#v, %v", status, err)
	}
	agentStatus, err := client.AgentStatus(context.Background(), controlclient.AgentRequest{SessionID: "session-1", Surface: "bar"})
	if err != nil || agentStatus.ControllerLabel != "main" {
		t.Fatalf("AgentStatus() = %#v, %v", agentStatus, err)
	}
	files, err := client.CompleteFile(context.Background(), controlclient.CompletionRequest{SessionID: "session-1", Query: "REA"})
	if err != nil || len(files) != 1 || files[0].Value != "README.md" {
		t.Fatalf("CompleteFile() = %#v, %v", files, err)
	}
	plugin, err := client.InspectPlugin(context.Background(), controlclient.PluginRequest{SessionID: "session-1", ID: "demo"})
	if err != nil || plugin.ID != "demo" {
		t.Fatalf("InspectPlugin() = %#v, %v", plugin, err)
	}
	if participants.principal.ID != "trusted-owner" || participants.sessionID != "session-1" ||
		configuration.request.SessionID != "session-1" || agents.request.SessionID != "session-1" ||
		completion.request.SessionID != "session-1" || plugins.request.SessionID != "session-1" {
		t.Fatalf("focused requests were not principal-bound and Session-addressed: %#v %#v %#v %#v %#v", participants, configuration.request, agents.request, completion.request, plugins.request)
	}
}

type focusedParticipantService struct {
	controlclient.ParticipantService
	principal controlclient.Principal
	sessionID string
}

func (s *focusedParticipantService) ListParticipantHandles(_ context.Context, principal controlclient.Principal, sessionID string) ([]string, error) {
	s.principal, s.sessionID = principal, sessionID
	return []string{"review"}, nil
}

type focusedConfigurationService struct {
	controlclient.ConfigurationService
	request controlclient.SessionModeRequest
}

func (s *focusedConfigurationService) ConfigureSessionMode(_ context.Context, _ controlclient.Principal, request controlclient.SessionModeRequest) (controlstatus.StatusSnapshot, error) {
	s.request = request
	return controlstatus.StatusSnapshot{Session: controlstatus.StatusSession{ID: request.SessionID, Surface: request.Surface}}, nil
}

type focusedAgentService struct {
	controlclient.AgentService
	request controlclient.AgentRequest
}

func (s *focusedAgentService) AgentStatus(_ context.Context, _ controlclient.Principal, request controlclient.AgentRequest) (controlclient.AgentStatusSnapshot, error) {
	s.request = request
	return controlclient.AgentStatusSnapshot{SessionID: request.SessionID, ControllerLabel: "main"}, nil
}

type focusedCompletionService struct {
	controlclient.CompletionService
	request controlclient.CompletionRequest
}

func (s *focusedCompletionService) CompleteFile(_ context.Context, _ controlclient.Principal, request controlclient.CompletionRequest) ([]controlclient.CompletionCandidate, error) {
	s.request = request
	return []controlclient.CompletionCandidate{{Value: "README.md"}}, nil
}

type focusedPluginService struct {
	controlclient.PluginService
	request controlclient.PluginRequest
}

func (s *focusedPluginService) InspectPlugin(_ context.Context, _ controlclient.Principal, request controlclient.PluginRequest) (controlclient.PluginSnapshot, error) {
	s.request = request
	return controlclient.PluginSnapshot{ID: request.ID, Name: "Demo", Version: "1", Status: "ready"}, nil
}
