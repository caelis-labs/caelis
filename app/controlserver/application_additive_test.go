package controlserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestApplicationConfigurationAdditionsPreserveScopeCASAndReceipt(t *testing.T) {
	store, err := application.Open(filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token := "app-client-" + strings.Repeat("a", 64)
	connection, err := store.Register(t.Context(), "owner", application.Registration{OperationID: "register", Name: "fixture", Credential: token})
	if err != nil {
		t.Fatal(err)
	}
	scope := application.Scope{PrincipalID: connection.PrincipalID, ApplicationID: connection.ApplicationID, ConnectionID: connection.ConnectionID}
	profile := application.Profile{Version: "role/1", Execution: "tools-only", Instructions: "original", Model: "main", ToolsVersion: "tools/1"}
	if err := store.PutBinding(t.Context(), application.Binding{Scope: scope, SessionID: "owned", CreationDigest: "digest", Profile: profile}); err != nil {
		t.Fatal(err)
	}
	service, err := appserver.NewApplicationService(appserver.ApplicationServiceConfig{Store: store, Commands: &appserver.CommandService{}, Sessions: &fakeService{}, ValidateProfile: func(context.Context, application.Profile) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	services := testAppServerServices(&fakeService{}, staticStatusService{})
	services.Applications = service
	server, err := New(HandlerConfig{Services: services, Authenticator: testAuthenticator(), AllowedHosts: []string{"example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	post := func(key, body, credential string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, apiPrefix+"/application/sessions/owned/configuration", strings.NewReader(body))
		r.Host = "example.test"
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	body := `{"operation_id":"update","expected_configuration_revision":"1","future":true,"patch":{"instructions":"updated","native_tools":[],"future":null}}`
	first := post("update", body, token)
	if first.Code != http.StatusOK {
		t.Fatalf("update %d: %s", first.Code, first.Body.String())
	}
	// The original strict application reader still accepts the Host's unchanged
	// response shape; unknown request fields never become persisted profile data.
	var raw struct {
		Revision string              `json:"revision"`
		Profile  application.Profile `json:"profile"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Revision != "2" || raw.Profile.Instructions != "updated" || raw.Profile.NativeTools == nil || len(raw.Profile.NativeTools) != 0 || raw.Profile.Model != "main" {
		t.Fatalf("configuration changed incorrectly: %+v", raw)
	}
	replay := post("update", body, token)
	if replay.Code != 200 || replay.Body.String() != first.Body.String() {
		t.Fatalf("original receipt changed: %d %s", replay.Code, replay.Body.String())
	}
	forbidden := post("forbidden", `{"operation_id":"forbidden","expected_configuration_revision":"2","patch":{"permissions":{},"future":true}}`, token)
	if forbidden.Code != http.StatusBadRequest {
		t.Fatalf("permissions admitted: %d", forbidden.Code)
	}
	unauthorized := post("update", body, "unrecognized-token")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("authorization changed: %d", unauthorized.Code)
	}
	current, err := store.Configuration(t.Context(), scope, "owned")
	if err != nil || current.Revision != 2 || current.Profile.Instructions != "updated" {
		t.Fatalf("unexpected effect: %+v %v", current, err)
	}
}
