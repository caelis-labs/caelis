package controlserver

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestApplicationTerminalObservationHasExactScope(t *testing.T) {
	store, err := application.Open(filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token := "app-client-" + strings.Repeat("a", 64)
	conn, err := store.Register(t.Context(), "owner", application.Registration{OperationID: "terminal-app", Name: "terminal-app", Credential: token})
	if err != nil {
		t.Fatal(err)
	}
	scope := application.Scope{PrincipalID: conn.PrincipalID, ApplicationID: conn.ApplicationID, ConnectionID: conn.ConnectionID}
	err = store.PutBinding(t.Context(), application.Binding{Scope: scope, SessionID: "owned", CreationDigest: "digest", Profile: application.Profile{Version: "role/1", Execution: "tools-only", Instructions: "fixture", Model: "fixture", ToolsVersion: "tools/1"}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := appserver.NewApplicationService(appserver.ApplicationServiceConfig{Store: store, Commands: &appserver.CommandService{}, Sessions: &fakeService{}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{config: HandlerConfig{Services: appserver.AppServerServices{Applications: svc, Terminal: &focusedTerminalService{}}}}
	info := applicationServerInfo(appserver.ServerInfo{}, s.config.Services)
	if !slices.Contains(info.Capabilities, appserver.CapabilityApplicationTerminalObservation) {
		t.Fatal("missing negotiated capability")
	}
	for _, tc := range []struct {
		path, method string
		allowed      bool
	}{
		{"/sessions/owned/terminals/output", "POST", true},
		{"/sessions/other/terminals/output", "POST", false},
		{"/sessions/owned/terminals/wait", "POST", false},
		{"/sessions/owned/terminals/kill", "POST", false},
		{"/sessions/owned/terminals/release", "POST", false},
		{"/sessions/owned/terminals/output/extra", "POST", false},
		{"/sessions/owned/terminals/output", http.MethodGet, false},
	} {
		r := httptest.NewRequest(tc.method, apiPrefix+tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		p, isApp, err := s.applicationPrincipal(r)
		if !isApp || (err == nil) != tc.allowed {
			t.Fatalf("%s allowed=%v err=%v", tc.path, tc.allowed, err)
		}
		if err == nil && p.ApplicationID != conn.ApplicationID {
			t.Fatal("principal lost application scope")
		}
	}
	if err = store.Revoke(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", apiPrefix+"/sessions/owned/terminals/output", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	// Revocation removes execution authority, not retained result observation.
	if _, _, err = s.applicationPrincipal(r); err != nil {
		t.Fatal("revoked application lost receipt observation", err)
	}
}
