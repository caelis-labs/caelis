package controlserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestApplicationReviewerStateRouteScopesBindingAndCapability(t *testing.T) {
	store, err := application.Open(filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tokens := []string{"app-client-" + strings.Repeat("a", 64), "app-client-" + strings.Repeat("b", 64)}
	connections := make([]application.Connection, 2)
	for i := range tokens {
		connections[i], err = store.Register(t.Context(), "owner", application.Registration{OperationID: "review-app-" + string(rune('a'+i)), Name: "review-app", Credential: tokens[i]})
		if err != nil {
			t.Fatal(err)
		}
	}
	conn := connections[0]
	scope := application.Scope{PrincipalID: conn.PrincipalID, ApplicationID: conn.ApplicationID, ConnectionID: conn.ConnectionID}
	profile := application.Profile{Version: "role/1", Execution: "tools-only", Instructions: "fixture", Model: "main", ToolsVersion: "tools/1", Reviewer: &application.Reviewer{Kind: "guardian", Model: "reviewer"}}
	profile.Permissions.ApprovalMode = "auto-review"
	if err := store.PutBinding(t.Context(), application.Binding{Scope: scope, SessionID: "owned", CreationDigest: "digest", Profile: profile}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service, err := appserver.NewApplicationService(appserver.ApplicationServiceConfig{Store: store, Commands: &appserver.CommandService{}, Sessions: &fakeService{}, ReviewerState: func(_ context.Context, p application.Profile) (application.ReviewerState, error) {
		calls++
		return application.ReviewerState{ApprovalMode: p.Permissions.ApprovalMode, Reviewer: p.Reviewer, Status: "ready"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	services := testAppServerServices(&fakeService{}, staticStatusService{})
	services.Applications = service
	server, err := New(HandlerConfig{Services: services, Authenticator: testAuthenticator(), AllowedHosts: []string{"example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(server.config.ServerInfo.Capabilities, application.CapabilityGuardianReview) {
		t.Fatal("reviewer capability not advertised")
	}
	path := apiPrefix + "/application/sessions/owned/reviewer-state"
	for i, status := range []int{http.StatusOK, http.StatusNotFound} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Host = "example.test"
		request.Header.Set("Authorization", "Bearer "+tokens[i])
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("connection %d: status %d, body %s", i, response.Code, response.Body.String())
		}
		if i == 0 {
			var got application.ReviewerState
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got.SessionID != "owned" || got.Status != "ready" || got.Reviewer == nil || got.Reviewer.Model != "reviewer" {
				t.Fatalf("reviewer response %+v, %v", got, err)
			}
		}
	}
	if calls != 1 {
		t.Fatalf("cross-scope lookup called reviewer %d times", calls)
	}
	service, err = appserver.NewApplicationService(appserver.ApplicationServiceConfig{Store: store, Commands: &appserver.CommandService{}, Sessions: &fakeService{}})
	if err != nil {
		t.Fatal(err)
	}
	services.Applications = service
	if slices.Contains(applicationServerInfo(appserver.ServerInfo{}, services).Capabilities, application.CapabilityGuardianReview) {
		t.Fatal("unbound reviewer capability advertised")
	}
}
