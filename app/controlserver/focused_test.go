package controlserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
)

func TestFocusedClientsRoundTripThroughHTTPAppServer(t *testing.T) {
	participants := &focusedParticipantService{}
	agentMessages := &focusedAgentMessageService{}
	configuration := &focusedConfigurationService{}
	agents := &focusedAgentService{}
	completion := &focusedCompletionService{}
	plugins := &focusedPluginService{}
	presentation := &focusedPresentationService{}
	terminals := &focusedTerminalService{}
	services := testAppServerServices(&fakeService{}, staticStatusService{})
	services.Participants = participants
	services.AgentMessages = agentMessages
	services.Configuration = configuration
	services.Agents = agents
	services.Completion = completion
	services.Plugins = plugins
	services.Presentation = presentation
	services.Terminal = terminals
	server, err := New(HandlerConfig{
		Services: services, TaskStreams: &fakeTaskService{}, Authenticator: testAuthenticator(),
		AllowedHosts: []string{"127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := httpclient.New(httpclient.Config{
		BaseURL: "http://127.0.0.1", BearerToken: "test-token",
		HTTPClient:    &http.Client{Transport: controlHandlerRoundTripper{handler: server}},
		Compatibility: appserver.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}

	messageResult, err := client.DeliverAgentMessage(context.Background(), appserver.AgentMessageRequest{
		SessionID: "session-1", MessageID: "message-1", To: "parent", Text: "status", DisplayFrom: "@guardian",
	})
	if err != nil || !messageResult.Accepted || messageResult.MessageID != "message-1" {
		t.Fatalf("DeliverAgentMessage() = %#v, %v", messageResult, err)
	}
	handles, err := client.Handles(context.Background(), "session-1")
	if err != nil || len(handles) != 1 || handles[0] != "review" {
		t.Fatalf("Handles() = %#v, %v", handles, err)
	}
	expectedSessionRevision := uint64(3)
	command, err := client.ConfigureSessionMode(context.Background(), appserver.SessionModeRequest{
		WriteBase: appserver.WriteBase{OperationID: "session-mode-1", SessionID: "session-1", ExpectedRevision: &expectedSessionRevision, ExpectedControllerEpoch: "epoch-1"},
		Mode:      "manual",
	})
	if err != nil || command.SessionID != "session-1" || command.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConfigureSessionMode() = %#v, %v", command, err)
	}
	command, err = client.UseSessionModel(context.Background(), appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "session-model-1", SessionID: "session-1", ExpectedRevision: &expectedSessionRevision, ExpectedControllerEpoch: "epoch-1"},
		Model:     "mimo", ReasoningEffort: "high",
	})
	if err != nil || command.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("UseSessionModel() = %#v, %v", command, err)
	}
	command, err = client.ConfigureSessionControllerMode(context.Background(), appserver.SessionControllerModeRequest{
		WriteBase: appserver.WriteBase{OperationID: "controller-mode-1", SessionID: "session-1", ExpectedRevision: &expectedSessionRevision, ExpectedControllerEpoch: "epoch-1"},
		Mode:      "code",
	})
	if err != nil || command.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConfigureSessionControllerMode() = %#v, %v", command, err)
	}
	command, err = client.ConfigureSessionPresentationMode(context.Background(), appserver.SessionPresentationModeRequest{
		WriteBase: appserver.WriteBase{OperationID: "presentation-mode-1", SessionID: "session-1", ExpectedRevision: &expectedSessionRevision, ExpectedControllerEpoch: "epoch-1"},
		Mode:      "focus",
	})
	if err != nil || command.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConfigureSessionPresentationMode() = %#v, %v", command, err)
	}
	command, err = client.ConfigureSessionPresentation(context.Background(), appserver.SessionPresentationConfigRequest{
		WriteBase: appserver.WriteBase{OperationID: "presentation-config-1", SessionID: "session-1", ExpectedRevision: &expectedSessionRevision, ExpectedControllerEpoch: "epoch-1"},
		ConfigID:  "tone", Value: "quiet",
	})
	if err != nil || command.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConfigureSessionPresentation() = %#v, %v", command, err)
	}
	expectedConfigurationRevision := uint64(7)
	command, err = client.ResetSandbox(context.Background(), appserver.SandboxRequest{WriteBase: appserver.WriteBase{
		OperationID: "sandbox-reset-1", ExpectedRevision: &expectedConfigurationRevision,
	}})
	if err != nil || command.Outcome != appserver.OutcomeCommitted || command.Revision != expectedConfigurationRevision {
		t.Fatalf("ResetSandbox() = %#v, %v", command, err)
	}
	agentStatus, err := client.AgentStatus(context.Background(), appserver.AgentRequest{SessionID: "session-1", Surface: "bar"})
	if err != nil || agentStatus.ControllerLabel != "main" {
		t.Fatalf("AgentStatus() = %#v, %v", agentStatus, err)
	}
	command, err = client.HandoffAgent(context.Background(), appserver.HandoffAgentRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "handoff-1",
			SessionID:               "session-1",
			ExpectedRevision:        &expectedSessionRevision,
			ExpectedControllerEpoch: "epoch-1",
		},
		Target: "orbit",
	})
	if err != nil || command.Outcome != appserver.OutcomeCommitted || agents.handoff.OperationID != "handoff-1" {
		t.Fatalf("HandoffAgent() = %#v, %v; request = %#v", command, err, agents.handoff)
	}
	disconnectSnapshot, err := client.DisconnectCandidates(context.Background(), appserver.AgentRequest{Surface: "host"})
	if err != nil || disconnectSnapshot.Revision != expectedConfigurationRevision || len(disconnectSnapshot.Candidates) != 1 {
		t.Fatalf("DisconnectCandidates() = %#v, %v", disconnectSnapshot, err)
	}
	command, err = client.DisconnectACP(context.Background(), appserver.DisconnectACPRequest{
		WriteBase: appserver.WriteBase{OperationID: "agent-disconnect-1", ExpectedRevision: &expectedConfigurationRevision},
		AgentID:   "codex",
	})
	if err != nil || command.Outcome != appserver.OutcomeCommitted || agents.disconnect.OperationID != "agent-disconnect-1" || agents.disconnect.SessionID != "" {
		t.Fatalf("DisconnectACP() = %#v, %v; request = %#v", command, err, agents.disconnect)
	}
	command, err = client.PrepareACP(context.Background(), appserver.PrepareACPRequest{
		WriteBase: appserver.WriteBase{OperationID: "agent-prepare-1", ExpectedRevision: &expectedConfigurationRevision},
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "codex", Launcher: controlagents.LauncherChoiceNPX, ModelID: "mimo", CWD: "/workspace",
		},
	})
	if err != nil || command.Outcome != appserver.OutcomeCommitted || command.Resource == nil || command.Resource.Kind != appserver.CommandResourceACPPreparation {
		t.Fatalf("PrepareACP() = %#v, %v", command, err)
	}
	preparation, err := client.ACPPreparation(context.Background(), appserver.ACPPreparationRequest{Ref: command.Resource.Ref})
	if err != nil || preparation.Ref != command.Resource.Ref || agents.preparationRef != command.Resource.Ref {
		t.Fatalf("ACPPreparation() = %#v, %v", preparation, err)
	}
	command, err = client.PrepareACPAuthentication(context.Background(), appserver.PrepareACPAuthenticationRequest{
		WriteBase:      appserver.WriteBase{OperationID: "agent-prepare-auth-1", ExpectedRevision: &expectedConfigurationRevision},
		PreparationRef: preparation.Ref, PreparationDigest: preparation.ContentDigest, MethodID: "login",
	})
	if err != nil || command.Outcome != appserver.OutcomeCommitted || agents.prepareAuth.MethodID != "login" {
		t.Fatalf("PrepareACPAuthentication() = %#v, %v", command, err)
	}
	command, err = client.ConnectACP(context.Background(), appserver.ConnectACPRequest{
		WriteBase:      appserver.WriteBase{OperationID: "agent-connect-1", ExpectedRevision: &expectedConfigurationRevision},
		PreparationRef: preparation.Ref, PreparationDigest: preparation.ContentDigest,
	})
	if err != nil || command.Outcome != appserver.OutcomeCommitted || command.Resource == nil ||
		command.Resource.Kind != appserver.CommandResourceModelProfile || agents.connect.OperationID != "agent-connect-1" {
		t.Fatalf("ConnectACP() = %#v, %v", command, err)
	}
	command, err = client.BindAgentBinding(context.Background(), appserver.BindAgentBindingRequest{
		WriteBase: appserver.WriteBase{OperationID: "agent-binding-1", ExpectedRevision: &expectedConfigurationRevision},
		Binding:   agentbinding.Binding{Handle: agentbinding.HandleOrbit, ProfileID: "provider:mimo", Effort: "high"},
	})
	if err != nil || command.Outcome != appserver.OutcomeCommitted || command.Revision != expectedConfigurationRevision+1 ||
		agents.binding.OperationID != "agent-binding-1" || agents.binding.SessionID != "" {
		t.Fatalf("BindAgentBinding() = %#v, %v; request = %#v", command, err, agents.binding)
	}
	files, err := client.CompleteFile(context.Background(), appserver.CompletionRequest{
		SessionID: "session-1", WorkspaceKey: "workspace-a", CWD: "/tmp/workspace-a", Query: "REA",
	})
	if err != nil || len(files) != 1 || files[0].Value != "README.md" {
		t.Fatalf("CompleteFile() = %#v, %v", files, err)
	}
	plugin, err := client.InspectPlugin(context.Background(), appserver.PluginRequest{SessionID: "session-1", ID: "demo"})
	if err != nil || plugin.ID != "demo" {
		t.Fatalf("InspectPlugin() = %#v, %v", plugin, err)
	}
	snapshot, err := client.PresentationSnapshot(context.Background(), appserver.PresentationRequest{SessionID: "session-1"})
	if err != nil || snapshot.Modes == nil || snapshot.Modes.CurrentModeID != "manual" {
		t.Fatalf("PresentationSnapshot() = %#v, %v", snapshot, err)
	}
	terminal, err := client.TerminalOutput(context.Background(), appserver.TerminalRequest{SessionID: "session-1", TerminalID: "call-1"})
	if err != nil || terminal.Output != "done\n" {
		t.Fatalf("TerminalOutput() = %#v, %v", terminal, err)
	}
	if participants.principal.ID != "trusted-owner" || participants.sessionID != "session-1" ||
		agentMessages.principal.ID != "trusted-owner" || agentMessages.request.SessionID != "session-1" ||
		agentMessages.request.DisplayFrom != "@guardian" || configuration.principal.ID != "trusted-owner" ||
		configuration.request.SessionID != "session-1" || configuration.model.Model != "mimo" ||
		configuration.controllerMode.ExpectedControllerEpoch != "epoch-1" ||
		configuration.presentationMode.Mode != "focus" || configuration.presentation.ConfigID != "tone" ||
		configuration.reset.SessionID != "" ||
		configuration.reset.OperationID != "sandbox-reset-1" || configuration.reset.ExpectedRevision == nil ||
		*configuration.reset.ExpectedRevision != expectedConfigurationRevision ||
		agents.request.SessionID != "session-1" ||
		completion.request.SessionID != "session-1" || completion.request.WorkspaceKey != "workspace-a" ||
		completion.request.CWD != "/tmp/workspace-a" || plugins.request.SessionID != "session-1" ||
		presentation.request.SessionID != "session-1" || terminals.request.SessionID != "session-1" {
		t.Fatalf("focused requests were not principal-bound and Session-addressed: %#v %#v %#v %#v %#v", participants, configuration.request, agents.request, completion.request, plugins.request)
	}

	hostStatus, err := client.SessionStatus(context.Background(), appserver.StatusRequest{Surface: "host"})
	if err != nil || hostStatus.Session.ID != "" {
		t.Fatalf("Host SessionStatus() = %#v, %v", hostStatus, err)
	}
	connectRevision := uint64(7)
	if _, err := client.ConnectModel(context.Background(), appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "model-connect-1", ExpectedRevision: &connectRevision},
		Config:    appserver.ConnectConfig{Provider: "openai", Model: "mimo"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AgentStatus(context.Background(), appserver.AgentRequest{Surface: "host"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CompleteFile(context.Background(), appserver.CompletionRequest{Surface: "host"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.InspectPlugin(context.Background(), appserver.PluginRequest{Surface: "host", ID: "demo"}); err != nil {
		t.Fatal(err)
	}
	if agents.request.SessionID != "" || completion.request.SessionID != "" || plugins.request.SessionID != "" {
		t.Fatalf("Host focused requests acquired Session addresses: %#v %#v %#v %#v", configuration.connect, agents.request, completion.request, plugins.request)
	}
	legacyRequest := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1"+apiPrefix+"/sessions/session-1/configuration/sandbox-reset",
		http.NoBody,
	)
	legacyRequest.Header.Set("Authorization", "Bearer test-token")
	legacyResponse := httptest.NewRecorder()
	server.ServeHTTP(legacyResponse, legacyRequest)
	if legacyResponse.Code != http.StatusNotFound {
		t.Fatalf("legacy Session sandbox route status = %d, want 404", legacyResponse.Code)
	}
	for _, path := range []string{
		"/agents/discover-acp",
		"/sessions/session-1/configuration/connect-model",
		"/sessions/session-1/configuration/use-model",
		"/sessions/session-1/configuration/delete-model",
		"/sessions/session-1/agents/binding-status",
		"/sessions/session-1/agents/bind",
		"/sessions/session-1/agents/reset-binding",
		"/sessions/session-1/agents/create-role",
		"/sessions/session-1/agents/delete-role",
		"/sessions/session-1/agents/save-binding-set",
		"/sessions/session-1/agents/apply-binding-set",
		"/sessions/session-1/agents/delete-binding-set",
		"/sessions/session-1/agents/disconnect-candidates",
		"/sessions/session-1/agents/disconnect-acp",
		"/sessions/session-1/agents/discover-acp",
		"/sessions/session-1/agents/connect-acp",
		"/sessions/session-1/presentation/mode",
		"/sessions/session-1/presentation/config",
		"/sessions/session-1/presentation/model",
	} {
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+apiPrefix+path, http.NoBody)
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("legacy route %q status = %d, want 404", path, response.Code)
		}
	}
	forgedDisconnect := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1"+apiPrefix+"/agents/disconnect-acp",
		strings.NewReader(`{"operation_id":"forged-disconnect","session_id":"session-1","expected_revision":"7","agent_id":"codex"}`),
	)
	forgedDisconnect.Header.Set("Authorization", "Bearer test-token")
	forgedDisconnect.Header.Set("Content-Type", "application/json")
	forgedDisconnect.Header.Set("Idempotency-Key", "forged-disconnect")
	forgedDisconnect.Header.Set("If-Match", `"7"`)
	forgedResponse := httptest.NewRecorder()
	server.ServeHTTP(forgedResponse, forgedDisconnect)
	if forgedResponse.Code != http.StatusBadRequest || agents.disconnectCalls != 1 {
		t.Fatalf("forged Host disconnect status/calls = %d/%d, want 400/1", forgedResponse.Code, agents.disconnectCalls)
	}

	modelCalls := configuration.modelCalls
	forgedSessionRequest := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1"+apiPrefix+"/sessions/session-1/configuration/model",
		strings.NewReader(`{"session_id":"forged-session","model":"mimo"}`),
	)
	forgedSessionRequest.Header.Set("Authorization", "Bearer test-token")
	forgedSessionRequest.Header.Set("Content-Type", "application/json")
	forgedSessionRequest.Header.Set("Idempotency-Key", "session-model-forged-session")
	forgedSessionRequest.Header.Set("If-Match", `"3"`)
	forgedSessionResponse := httptest.NewRecorder()
	server.ServeHTTP(forgedSessionResponse, forgedSessionRequest)
	if forgedSessionResponse.Code != http.StatusBadRequest || configuration.modelCalls != modelCalls {
		t.Fatalf("Session model path/body status/calls = %d/%d, want 400/%d", forgedSessionResponse.Code, configuration.modelCalls, modelCalls)
	}

	resetCalls := configuration.resetCalls
	forgedHostRequest := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1"+apiPrefix+"/configuration/sandbox-reset",
		strings.NewReader(`{"session_id":"forged-session"}`),
	)
	forgedHostRequest.Header.Set("Authorization", "Bearer test-token")
	forgedHostRequest.Header.Set("Content-Type", "application/json")
	forgedHostRequest.Header.Set("Idempotency-Key", "sandbox-reset-forged-session")
	forgedHostRequest.Header.Set("If-Match", `"7"`)
	forgedHostResponse := httptest.NewRecorder()
	server.ServeHTTP(forgedHostResponse, forgedHostRequest)
	if forgedHostResponse.Code != http.StatusBadRequest || configuration.resetCalls != resetCalls {
		t.Fatalf("Host sandbox Session body status/calls = %d/%d, want 400/%d", forgedHostResponse.Code, configuration.resetCalls, resetCalls)
	}

	connectCalls := configuration.connectCalls
	forgedConnectRequest := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1"+apiPrefix+"/configuration/connect-model",
		strings.NewReader(`{"session_id":"forged-session","config":{}}`),
	)
	forgedConnectRequest.Header.Set("Authorization", "Bearer test-token")
	forgedConnectRequest.Header.Set("Content-Type", "application/json")
	forgedConnectRequest.Header.Set("Idempotency-Key", "model-connect-forged-session")
	forgedConnectRequest.Header.Set("If-Match", `"7"`)
	forgedConnectResponse := httptest.NewRecorder()
	server.ServeHTTP(forgedConnectResponse, forgedConnectRequest)
	if forgedConnectResponse.Code != http.StatusBadRequest || configuration.connectCalls != connectCalls {
		t.Fatalf("Host connect Session body status/calls = %d/%d, want 400/%d", forgedConnectResponse.Code, configuration.connectCalls, connectCalls)
	}

	bindingCalls := agents.bindingCalls
	forgedBindingRequest := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1"+apiPrefix+"/agents/bind",
		strings.NewReader(`{"session_id":"forged-session","binding":{"handle":"orbit","profile_id":"provider:mimo","effort":"high"}}`),
	)
	forgedBindingRequest.Header.Set("Authorization", "Bearer test-token")
	forgedBindingRequest.Header.Set("Content-Type", "application/json")
	forgedBindingRequest.Header.Set("Idempotency-Key", "agent-binding-forged-session")
	forgedBindingRequest.Header.Set("If-Match", `"7"`)
	forgedBindingResponse := httptest.NewRecorder()
	server.ServeHTTP(forgedBindingResponse, forgedBindingRequest)
	if forgedBindingResponse.Code != http.StatusBadRequest || agents.bindingCalls != bindingCalls {
		t.Fatalf("Host binding Session body status/calls = %d/%d, want 400/%d", forgedBindingResponse.Code, agents.bindingCalls, bindingCalls)
	}
}

type focusedAgentMessageService struct {
	appserver.AgentMessageService
	principal appserver.Principal
	request   appserver.AgentMessageRequest
}

func (s *focusedAgentMessageService) DeliverAgentMessage(_ context.Context, principal appserver.Principal, request appserver.AgentMessageRequest) (appserver.AgentMessageResult, error) {
	s.principal, s.request = principal, request
	return appserver.AgentMessageResult{MessageID: request.MessageID, Accepted: true, State: "pending"}, nil
}

type focusedParticipantService struct {
	appserver.ParticipantService
	principal appserver.Principal
	sessionID string
}

func (s *focusedParticipantService) ListParticipantHandles(_ context.Context, principal appserver.Principal, sessionID string) ([]string, error) {
	s.principal, s.sessionID = principal, sessionID
	return []string{"review"}, nil
}

type focusedConfigurationService struct {
	appserver.ConfigurationService
	principal        appserver.Principal
	request          appserver.SessionModeRequest
	model            appserver.SessionModelRequest
	controllerMode   appserver.SessionControllerModeRequest
	presentationMode appserver.SessionPresentationModeRequest
	presentation     appserver.SessionPresentationConfigRequest
	connect          appserver.ConnectModelRequest
	reset            appserver.SandboxRequest
	modelCalls       int
	connectCalls     int
	resetCalls       int
}

func (s *focusedConfigurationService) ConnectModel(_ context.Context, _ appserver.Principal, request appserver.ConnectModelRequest) (appserver.CommandResult, error) {
	s.connectCalls++
	s.connect = request
	return appserver.CommandResult{OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted, Revision: 8}, nil
}

func (s *focusedConfigurationService) ConfigureSessionMode(_ context.Context, _ appserver.Principal, request appserver.SessionModeRequest) (appserver.CommandResult, error) {
	s.request = request
	return appserver.CommandResult{OperationID: request.OperationID, SessionID: request.SessionID, Outcome: appserver.OutcomeCommitted}, nil
}

func (s *focusedConfigurationService) UseSessionModel(_ context.Context, _ appserver.Principal, request appserver.SessionModelRequest) (appserver.CommandResult, error) {
	s.modelCalls++
	s.model = request
	return appserver.CommandResult{OperationID: request.OperationID, SessionID: request.SessionID, Outcome: appserver.OutcomeCommitted}, nil
}

func (s *focusedConfigurationService) ConfigureSessionControllerMode(_ context.Context, _ appserver.Principal, request appserver.SessionControllerModeRequest) (appserver.CommandResult, error) {
	s.controllerMode = request
	return appserver.CommandResult{OperationID: request.OperationID, SessionID: request.SessionID, Outcome: appserver.OutcomeCommitted}, nil
}

func (s *focusedConfigurationService) ConfigureSessionPresentationMode(_ context.Context, _ appserver.Principal, request appserver.SessionPresentationModeRequest) (appserver.CommandResult, error) {
	s.presentationMode = request
	return appserver.CommandResult{OperationID: request.OperationID, SessionID: request.SessionID, Outcome: appserver.OutcomeCommitted}, nil
}

func (s *focusedConfigurationService) ConfigureSessionPresentation(_ context.Context, _ appserver.Principal, request appserver.SessionPresentationConfigRequest) (appserver.CommandResult, error) {
	s.presentation = request
	return appserver.CommandResult{OperationID: request.OperationID, SessionID: request.SessionID, Outcome: appserver.OutcomeCommitted}, nil
}

func (s *focusedConfigurationService) ResetSandbox(_ context.Context, principal appserver.Principal, request appserver.SandboxRequest) (appserver.CommandResult, error) {
	s.resetCalls++
	s.principal, s.reset = principal, request
	return appserver.CommandResult{
		OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted, Revision: *request.ExpectedRevision,
	}, nil
}

type focusedAgentService struct {
	appserver.AgentService
	request         appserver.AgentRequest
	candidates      appserver.AgentRequest
	handoff         appserver.HandoffAgentRequest
	binding         appserver.BindAgentBindingRequest
	disconnect      appserver.DisconnectACPRequest
	prepare         appserver.PrepareACPRequest
	prepareAuth     appserver.PrepareACPAuthenticationRequest
	connect         appserver.ConnectACPRequest
	preparationRef  string
	disconnectCalls int
	bindingCalls    int
}

func (s *focusedAgentService) PrepareACP(_ context.Context, _ appserver.Principal, request appserver.PrepareACPRequest) (appserver.CommandResult, error) {
	s.prepare = request
	s.preparationRef = "acpp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	return appserver.CommandResult{
		OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted, Revision: *request.ExpectedRevision,
		Resource: &appserver.CommandResource{Kind: appserver.CommandResourceACPPreparation, Ref: s.preparationRef, Digest: strings.Repeat("a", 64)},
	}, nil
}

func (s *focusedAgentService) PrepareACPAuthentication(_ context.Context, _ appserver.Principal, request appserver.PrepareACPAuthenticationRequest) (appserver.CommandResult, error) {
	s.prepareAuth = request
	return appserver.CommandResult{
		OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted, Revision: *request.ExpectedRevision,
		Resource: &appserver.CommandResource{Kind: appserver.CommandResourceACPPreparation, Ref: request.PreparationRef, Digest: request.PreparationDigest},
	}, nil
}

func (s *focusedAgentService) ACPPreparation(_ context.Context, _ appserver.Principal, request appserver.ACPPreparationRequest) (controlagents.ACPPreparation, error) {
	s.preparationRef = request.Ref
	return controlagents.ACPPreparation{Ref: request.Ref, State: controlagents.PreparationStateReady, ContentDigest: strings.Repeat("a", 64)}, nil
}

func (s *focusedAgentService) ConnectACP(_ context.Context, _ appserver.Principal, request appserver.ConnectACPRequest) (appserver.CommandResult, error) {
	s.connect = request
	return appserver.CommandResult{
		OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted, Revision: *request.ExpectedRevision + 1,
		Resource: &appserver.CommandResource{Kind: appserver.CommandResourceModelProfile, Ref: "acp:codex:mimo", Digest: request.PreparationDigest},
	}, nil
}

func (s *focusedAgentService) DisconnectCandidates(_ context.Context, _ appserver.Principal, request appserver.AgentRequest) (appserver.DisconnectCandidatesSnapshot, error) {
	s.candidates = request
	return appserver.DisconnectCandidatesSnapshot{
		Revision: 7,
		Candidates: []controlagents.DisconnectCandidate{{
			AgentID: "codex", ConnectionID: "codex", LastOnConnection: true,
		}},
	}, nil
}

func (s *focusedAgentService) DisconnectACP(_ context.Context, _ appserver.Principal, request appserver.DisconnectACPRequest) (appserver.CommandResult, error) {
	s.disconnectCalls++
	s.disconnect = request
	revision := uint64(0)
	if request.ExpectedRevision != nil {
		revision = *request.ExpectedRevision + 1
	}
	return appserver.CommandResult{
		OperationID: request.OperationID,
		Outcome:     appserver.OutcomeCommitted,
		Revision:    revision,
	}, nil
}

func (s *focusedAgentService) AgentStatus(_ context.Context, _ appserver.Principal, request appserver.AgentRequest) (appserver.AgentStatusSnapshot, error) {
	s.request = request
	return appserver.AgentStatusSnapshot{SessionID: request.SessionID, ControllerLabel: "main"}, nil
}

func (s *focusedAgentService) HandoffAgent(_ context.Context, _ appserver.Principal, request appserver.HandoffAgentRequest) (appserver.CommandResult, error) {
	s.handoff = request
	revision := uint64(0)
	if request.ExpectedRevision != nil {
		revision = *request.ExpectedRevision + 1
	}
	return appserver.CommandResult{
		OperationID: request.OperationID,
		SessionID:   request.SessionID,
		Outcome:     appserver.OutcomeCommitted,
		Revision:    revision,
	}, nil
}

func (s *focusedAgentService) BindAgentBinding(_ context.Context, _ appserver.Principal, request appserver.BindAgentBindingRequest) (appserver.CommandResult, error) {
	s.bindingCalls++
	s.binding = request
	revision := uint64(0)
	if request.ExpectedRevision != nil {
		revision = *request.ExpectedRevision + 1
	}
	return appserver.CommandResult{
		OperationID: request.OperationID,
		Outcome:     appserver.OutcomeCommitted,
		Revision:    revision,
	}, nil
}

type focusedCompletionService struct {
	appserver.CompletionService
	request appserver.CompletionRequest
}

func (s *focusedCompletionService) CompleteFile(_ context.Context, _ appserver.Principal, request appserver.CompletionRequest) ([]appserver.CompletionCandidate, error) {
	s.request = request
	return []appserver.CompletionCandidate{{Value: "README.md"}}, nil
}

type focusedPluginService struct {
	appserver.PluginService
	request appserver.PluginRequest
}

type focusedPresentationService struct {
	appserver.PresentationService
	request appserver.PresentationRequest
}

func (s *focusedPresentationService) PresentationSnapshot(_ context.Context, _ appserver.Principal, request appserver.PresentationRequest) (appserver.PresentationSnapshot, error) {
	s.request = request
	return appserver.PresentationSnapshot{Modes: &appserver.PresentationModeState{CurrentModeID: "manual"}}, nil
}

type focusedTerminalService struct {
	appserver.TerminalService
	request appserver.TerminalRequest
}

func (s *focusedTerminalService) TerminalOutput(_ context.Context, _ appserver.Principal, request appserver.TerminalRequest) (appserver.TerminalOutput, error) {
	s.request = request
	return appserver.TerminalOutput{Output: "done\n"}, nil
}

func (s *focusedPluginService) InspectPlugin(_ context.Context, _ appserver.Principal, request appserver.PluginRequest) (appserver.PluginSnapshot, error) {
	s.request = request
	return appserver.PluginSnapshot{ID: request.ID, Name: "Demo", Version: "1", Status: "ready"}, nil
}
