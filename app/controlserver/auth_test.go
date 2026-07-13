package controlserver

import (
	"net/http/httptest"
	"testing"

	controlclient "github.com/caelis-labs/caelis/ports/controlclient"
)

func TestBearerTokenAuthenticatorUsesTrustedPrincipal(t *testing.T) {
	authenticator, err := BearerTokenAuthenticator("server-secret", controlclient.Principal{ID: "configured-owner", Roles: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://example.test/", nil)
	request.Header.Set("Authorization", "Bearer server-secret")
	principal, err := authenticator.Authenticate(request)
	if err != nil || principal.ID != "configured-owner" || len(principal.Roles) != 1 {
		t.Fatalf("Authenticate() = %#v, %v", principal, err)
	}
	request.Header.Set("Authorization", "Bearer wrong")
	if _, err := authenticator.Authenticate(request); err == nil {
		t.Fatal("wrong bearer token was accepted")
	}
}
