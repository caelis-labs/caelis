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

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestApplicationReviewerStateRouteScopesBindingAndCapability(t *testing.T) {
	store, err := sessionprofile.Open(filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tokens := []string{"test-token", "app-client-" + strings.Repeat("a", 64)}
	scope := sessionprofile.PrincipalScope("trusted-owner")
	profile := sessionprofile.Profile{Version: "role/1", Execution: "tools-only", Instructions: "fixture", Model: "main", ToolsVersion: "tools/1", Reviewer: &sessionprofile.Reviewer{Kind: "guardian", Model: "reviewer"}}
	profile.Permissions.ApprovalMode = "auto-review"
	if err := store.PutBinding(t.Context(), sessionprofile.Binding{Scope: scope, SessionID: "owned", CreationDigest: "digest", Profile: profile}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service, err := appserver.NewSessionProfileService(appserver.SessionProfileServiceConfig{Store: store, Commands: &appserver.CommandService{}, Sessions: &fakeService{}, ReviewerState: func(_ context.Context, p sessionprofile.Profile) (sessionprofile.ReviewerState, error) {
		calls++
		return sessionprofile.ReviewerState{ApprovalMode: p.Permissions.ApprovalMode, Reviewer: p.Reviewer, Status: "ready"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	services := testAppServerServices(&fakeService{}, staticStatusService{})
	services.SessionProfiles = service
	server, err := New(HandlerConfig{Services: services, Authenticator: testAuthenticator(), AllowedHosts: []string{"example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(server.config.ServerInfo.Capabilities, sessionprofile.CapabilityGuardianReview) {
		t.Fatal("reviewer capability not advertised")
	}
	path := apiPrefix + "/sessions/owned/reviewer-state"
	for i, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Host = "example.test"
		request.Header.Set("Authorization", "Bearer "+tokens[i])
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("connection %d: status %d, body %s", i, response.Code, response.Body.String())
		}
		if i == 0 {
			var got sessionprofile.ReviewerState
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got.SessionID != "owned" || got.Status != "ready" || got.Reviewer == nil || got.Reviewer.Model != "reviewer" {
				t.Fatalf("reviewer response %+v, %v", got, err)
			}
		}
	}
	if calls != 1 {
		t.Fatalf("cross-scope lookup called reviewer %d times", calls)
	}
	service, err = appserver.NewSessionProfileService(appserver.SessionProfileServiceConfig{Store: store, Commands: &appserver.CommandService{}, Sessions: &fakeService{}})
	if err != nil {
		t.Fatal(err)
	}
	services.SessionProfiles = service
	if slices.Contains(sessionServerInfo(appserver.ServerInfo{}, services).Capabilities, sessionprofile.CapabilityGuardianReview) {
		t.Fatal("unbound reviewer capability advertised")
	}
}
