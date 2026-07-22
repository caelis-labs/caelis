package controlserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	controlclientport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/surfaces/appserver"
)

func TestBearerTokenAuthenticatorUsesTrustedPrincipal(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef"
	authenticator, err := BearerTokenAuthenticator(token, controlclient.Principal{ID: "configured-owner", Roles: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://example.test/", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	principal, err := authenticator.Authenticate(request)
	if err != nil || principal.ID != "configured-owner" || len(principal.Roles) != 1 {
		t.Fatalf("Authenticate() = %#v, %v", principal, err)
	}
	request.Header.Set("Authorization", "Bearer wrong")
	if _, err := authenticator.Authenticate(request); errorcode.CodeOf(err) != errorcode.Unauthenticated {
		t.Fatalf("wrong bearer token error = %v (code %q)", err, errorcode.CodeOf(err))
	}
}

func TestAllowedHostAndProductionBearerReachService(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef0123456789abcdef"
	authenticator, err := BearerTokenAuthenticator(token, controlclient.Principal{ID: "configured-owner"})
	if err != nil {
		t.Fatal(err)
	}
	service := &authBoundaryService{}
	server, err := appserver.New(appserver.Config{
		Service: service, Authenticator: authenticator, AllowedHosts: []string{"control.example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/control/v1/sessions", nil)
	request.Host = "control.example.test"
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || service.calls != 1 || service.principal.ID != "configured-owner" {
		t.Fatalf("status = %d, calls = %d, principal = %#v", recorder.Code, service.calls, service.principal)
	}
}

type authBoundaryService struct {
	controlclientport.Service
	calls     int
	principal controlclient.Principal
}

func (s *authBoundaryService) ListSessions(_ context.Context, principal controlclient.Principal, _ controlclientport.ListSessionsRequest) (session.SessionList, error) {
	s.calls++
	s.principal = principal
	return session.SessionList{}, nil
}
