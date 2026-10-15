package controlserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestApplicationConfigurationAdditionsPreserveScopeCASAndReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	store, err := sessionprofile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token := "test-token"
	scope := sessionprofile.PrincipalScope("trusted-owner")
	operations, err := appserver.NewSQLiteOperationStoreWithConfig(path, appserver.OperationRetentionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer operations.Close()
	if err := operations.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	commands, err := appserver.NewCommandService(appserver.CommandServiceConfig{Authorizer: configurationTestAuthorizer{}, Operations: operations, Backend: configurationTestBackend{store}})
	if err != nil {
		t.Fatal(err)
	}
	profile := sessionprofile.Profile{Version: "role/1", Execution: "tools-only", Instructions: "original", Model: "main", ToolsVersion: "tools/1"}
	if err := store.PutBinding(t.Context(), sessionprofile.Binding{Scope: scope, SessionID: "owned", CreationDigest: "digest", Profile: profile}); err != nil {
		t.Fatal(err)
	}
	service, err := appserver.NewSessionProfileService(appserver.SessionProfileServiceConfig{Store: store, Commands: commands, Sessions: &fakeService{}, ValidateProfile: func(context.Context, sessionprofile.Profile) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	services := testAppServerServices(&fakeService{}, staticStatusService{})
	services.SessionProfiles = service
	server, err := New(HandlerConfig{Services: services, Authenticator: testAuthenticator(), AllowedHosts: []string{"example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	post := func(key, body, credential string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, apiPrefix+"/sessions/owned/configuration", strings.NewReader(body))
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
	var raw struct {
		Configuration struct {
			Revision string                 `json:"revision"`
			Profile  sessionprofile.Profile `json:"profile"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	config := raw.Configuration
	if config.Revision != "2" || config.Profile.Instructions != "updated" || config.Profile.NativeTools == nil || len(config.Profile.NativeTools) != 0 || config.Profile.Model != "main" {
		t.Fatalf("configuration=%+v", config)
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

type configurationTestAuthorizer struct{}

func (configurationTestAuthorizer) Authorize(_ context.Context, p appserver.Principal, _ appserver.Action, id string) error {
	if p.ID != "trusted-owner" || id != "owned" {
		return appserver.ErrUnauthorized
	}
	return nil
}

type configurationTestBackend struct{ store *sessionprofile.Store }

func (b configurationTestBackend) ExecuteControlCommand(ctx context.Context, p appserver.Principal, _ appserver.Action, request any) (appserver.CommandResult, error) {
	req := request.(appserver.UpdateSessionConfigurationRequest)
	scope, _ := appserver.SessionScope(p)
	config, err := b.store.UpdateConfiguration(ctx, scope, req.SessionID, sessionprofile.UpdateConfigurationRequest{OperationID: req.OperationID, ExpectedConfigurationRevision: req.ExpectedConfigurationRevision, Patch: req.Patch}, nil)
	return appserver.CommandResult{Outcome: appserver.OutcomeCommitted, Configuration: &config}, err
}
