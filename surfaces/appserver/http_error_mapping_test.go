package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

func TestHTTPStatusMappingUsesTypedErrorsNotMessages(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status int
	}{
		{err: errorcode.New(errorcode.InvalidArgument, "bad"), status: http.StatusBadRequest},
		{err: errorcode.New(errorcode.Unauthenticated, "bad"), status: http.StatusUnauthorized},
		{err: errorcode.New(errorcode.PermissionDenied, "bad"), status: http.StatusForbidden},
		{err: errorcode.New(errorcode.Conflict, "bad"), status: http.StatusConflict},
		{err: errors.New("unauthorized conflict invalid request"), status: http.StatusInternalServerError},
	} {
		if got := statusForError(tt.err); got != tt.status {
			t.Fatalf("statusForError(%T %q) = %d, want %d", tt.err, tt.err, got, tt.status)
		}
	}
}

func TestEveryHandlerUsesTypedHTTPErrorMappingGolden(t *testing.T) {
	type route struct {
		name    string
		method  string
		path    string
		body    string
		command bool
	}
	routes := []route{
		{name: "list sessions", method: http.MethodGet, path: "/sessions"},
		{name: "create session", method: http.MethodPost, path: "/sessions", body: `{"workspace_key":"workspace"}`, command: true},
		{name: "close session", method: http.MethodDelete, path: "/sessions/session-1", command: true},
		{name: "session state", method: http.MethodGet, path: "/sessions/session-1/state"},
		{name: "session events", method: http.MethodGet, path: "/sessions/session-1/events"},
		{name: "session stream", method: http.MethodGet, path: "/sessions/session-1/stream"},
		{name: "prompt", method: http.MethodPost, path: "/sessions/session-1/prompt", body: `{"input":"hello"}`, command: true},
		{name: "steer", method: http.MethodPost, path: "/sessions/session-1/steer", body: `{"input":"continue","target":{"handle_id":"handle-1","run_id":"run-1","turn_id":"turn-1"}}`, command: true},
		{name: "cancel", method: http.MethodPost, path: "/sessions/session-1/cancel", body: `{"target":{"handle_id":"handle-1","run_id":"run-1","turn_id":"turn-1"}}`, command: true},
		{name: "resolve approval", method: http.MethodPost, path: "/sessions/session-1/approvals/approval-1/resolve", body: `{"outcome":"selected","option_id":"allow_once","approved":true,"target":{"handle_id":"handle-1","run_id":"run-1","turn_id":"turn-1"}}`, command: true},
		{name: "attach participant", method: http.MethodPost, path: "/sessions/session-1/participants", body: `{"profile_id":"acp:reviewer","effort":"high","role":"sidecar"}`, command: true},
		{name: "prompt participant", method: http.MethodPost, path: "/sessions/session-1/participants/participant-1/prompt", body: `{"input":"review"}`, command: true},
		{name: "cancel participant", method: http.MethodPost, path: "/sessions/session-1/participants/participant-1/cancel", body: `{"target":{"handle_id":"handle-1","run_id":"run-1","turn_id":"turn-1"}}`, command: true},
		{name: "detach participant", method: http.MethodDelete, path: "/sessions/session-1/participants/participant-1", command: true},
		{name: "handoff", method: http.MethodPost, path: "/sessions/session-1/handoff", body: `{"kind":"kernel"}`, command: true},
	}
	type mapping struct {
		name   string
		status int
		code   errorcode.Code
		auth   bool
	}
	mappings := []mapping{
		{name: "400", status: http.StatusBadRequest, code: errorcode.InvalidArgument, auth: true},
		{name: "401", status: http.StatusUnauthorized},
		{name: "403", status: http.StatusForbidden, code: errorcode.PermissionDenied, auth: true},
		{name: "409", status: http.StatusConflict, code: errorcode.Conflict, auth: true},
	}
	golden := readHTTPErrorGolden(t)
	for _, route := range routes {
		for _, mapping := range mappings {
			t.Run(route.name+"/"+mapping.name, func(t *testing.T) {
				service := &errorMappingService{err: errorcode.New(mapping.code, "validation failed")}
				server := newTestServer(t, service, 0)
				request := httptest.NewRequest(route.method, apiPrefix+route.path, strings.NewReader(route.body))
				request.Host = "example.test"
				if route.body != "" {
					request.Header.Set("Content-Type", "application/json")
				}
				if route.command {
					request.Header.Set("Idempotency-Key", "operation-1")
				}
				if mapping.auth {
					authorizeTestRequest(request)
				}
				recorder := httptest.NewRecorder()
				server.ServeHTTP(recorder, request)
				if recorder.Code != mapping.status {
					t.Fatalf("status = %d, want %d; body = %s", recorder.Code, mapping.status, recorder.Body.String())
				}
				kind := "read"
				if route.command && mapping.status != http.StatusUnauthorized && mapping.status != http.StatusForbidden {
					kind = "command"
				}
				assertJSONEquivalent(t, recorder.Body.Bytes(), golden[kind+"/"+mapping.name])
				if mapping.status == http.StatusUnauthorized && recorder.Header().Get("WWW-Authenticate") == "" {
					t.Fatal("401 response is missing WWW-Authenticate")
				}
			})
		}
	}
}

func readHTTPErrorGolden(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/http_error_mapping.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]json.RawMessage
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	return golden
}

type errorMappingService struct {
	err error
}

func (s *errorMappingService) result() (controlclient.CommandResult, error) {
	outcome := controlclient.OutcomeRejected
	if errorcode.CodeOf(s.err) == errorcode.Conflict {
		outcome = controlclient.OutcomeConflicted
	}
	return controlclient.CommandResult{OperationID: "operation-1", Outcome: outcome}, s.err
}

func (s *errorMappingService) ListSessions(context.Context, controlclient.Principal, controlclient.ListSessionsRequest) (session.SessionList, error) {
	return session.SessionList{}, s.err
}
func (s *errorMappingService) InspectSession(context.Context, controlclient.Principal, controlclient.StateRequest) (controlclient.SessionState, error) {
	return controlclient.SessionState{}, s.err
}
func (s *errorMappingService) Reconnect(context.Context, controlclient.Principal, controlclient.ReconnectRequest) (controlclient.ReconnectResult, error) {
	return controlclient.ReconnectResult{}, s.err
}
func (s *errorMappingService) Events(context.Context, controlclient.Principal, controlclient.SubscribeRequest) (controlclient.EventBatch, error) {
	return controlclient.EventBatch{}, s.err
}
func (s *errorMappingService) Subscribe(context.Context, controlclient.Principal, controlclient.SubscribeRequest) (controlclient.SubscribeResult, error) {
	return controlclient.SubscribeResult{}, s.err
}
func (s *errorMappingService) CreateSession(context.Context, controlclient.Principal, controlclient.CreateSessionRequest) (controlclient.CommandResult, error) {
	return s.result()
}
func (s *errorMappingService) CloseSession(context.Context, controlclient.Principal, controlclient.CloseSessionRequest) (controlclient.CommandResult, error) {
	return s.result()
}
func (s *errorMappingService) Prompt(context.Context, controlclient.Principal, controlclient.PromptRequest) (controlclient.CommandResult, error) {
	return s.result()
}
func (s *errorMappingService) Steer(context.Context, controlclient.Principal, controlclient.SteerRequest) (controlclient.CommandResult, error) {
	return s.result()
}
func (s *errorMappingService) Cancel(context.Context, controlclient.Principal, controlclient.CancelRequest) (controlclient.CommandResult, error) {
	return s.result()
}
func (s *errorMappingService) ResolveApproval(context.Context, controlclient.Principal, controlclient.ResolveApprovalRequest) (controlclient.CommandResult, error) {
	return s.result()
}
func (s *errorMappingService) AttachParticipant(context.Context, controlclient.Principal, controlclient.AttachParticipantRequest) (controlclient.CommandResult, error) {
	return s.result()
}
func (s *errorMappingService) PromptParticipant(context.Context, controlclient.Principal, controlclient.PromptParticipantRequest) (controlclient.CommandResult, error) {
	return s.result()
}
func (s *errorMappingService) CancelParticipant(context.Context, controlclient.Principal, controlclient.CancelParticipantRequest) (controlclient.CommandResult, error) {
	return s.result()
}
func (s *errorMappingService) DetachParticipant(context.Context, controlclient.Principal, controlclient.DetachParticipantRequest) (controlclient.CommandResult, error) {
	return s.result()
}
func (s *errorMappingService) Handoff(context.Context, controlclient.Principal, controlclient.HandoffRequest) (controlclient.CommandResult, error) {
	return s.result()
}
