package controlserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/control/uipreferences"
)

type workspaceHTTPBackend struct {
	preferences uipreferences.Preferences
	principal   string
}

func (*workspaceHTTPBackend) List(context.Context, string) ([]collaboration.Thread, error) {
	return []collaboration.Thread{{ID: "task", ParticipantID: "child", SessionID: "child-session", Generation: "g", CanDeliver: true}}, nil
}
func (*workspaceHTTPBackend) Deliver(context.Context, string, []collaboration.Message) error {
	return nil
}
func (*workspaceHTTPBackend) DeliverUserInput(context.Context, collaboration.UserInput) error {
	return nil
}
func (b *workspaceHTTPBackend) Authorize(_ context.Context, p appserver.Principal, _ appserver.Action, sessionID string) error {
	b.principal = p.ID
	if sessionID != "session-1" {
		return appserver.ErrUnauthorized
	}
	return nil
}
func (b *workspaceHTTPBackend) LoadUIPreferences(context.Context) (uipreferences.Preferences, error) {
	return b.preferences, nil
}
func (b *workspaceHTTPBackend) SaveUIPreferences(_ context.Context, p uipreferences.Preferences) error {
	b.preferences = b.preferences.Merge(p)
	return nil
}
func TestSubagentWorkspaceHTTPUsesAuthenticatedPrincipalAndTypedPreferences(t *testing.T) {
	backend := &workspaceHTTPBackend{preferences: (uipreferences.Preferences{}).WithDefaults()}
	mailbox, err := collaboration.Open(filepath.Join(t.TempDir(), "control.sqlite"), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mailbox.Close() }()
	services := testAppServerServices(&fakeService{}, staticStatusService{})
	services.SubagentInputs = &appserver.SubagentInputService{Authorizer: backend, Mailbox: mailbox}
	services.UIPreferences = &appserver.UIPreferencesService{Store: backend}
	server, err := New(HandlerConfig{Services: services, Authenticator: testAuthenticator(), AllowedHosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	client, err := httpclient.New(httpclient.Config{BaseURL: "http://127.0.0.1", BearerToken: "test-token", HTTPClient: &http.Client{Transport: controlHandlerRoundTripper{handler: server}}, Compatibility: appserver.CurrentCompatibility()})
	if err != nil {
		t.Fatal(err)
	}
	req := appserver.SubagentInputRequest{OperationID: "input", SessionID: "session-1", ParticipantID: "child", TaskID: "task", Input: "human guide"}
	req.ContentParts = []model.ContentPart{{Type: model.ContentPartImage, MimeType: "image/png", Data: "aGk="}}
	receipt, err := client.SubmitSubagentInput(t.Context(), req)
	if err != nil || receipt.State != "queued" || backend.principal == "" {
		t.Fatalf("input=%#v,%v principal=%q", receipt, err, backend.principal)
	}
	receipt2, err := client.SubmitSubagentInput(t.Context(), req)
	if err != nil || receipt2 != receipt {
		t.Fatalf("idempotent receipt=%#v,%v", receipt2, err)
	}
	invalidInput := req
	invalidInput.OperationID = "bad-image"
	invalidInput.ContentParts = []model.ContentPart{{Type: model.ContentPartImage, MimeType: "image/png", Data: "not base64"}}
	if _, err := client.SubmitSubagentInput(t.Context(), invalidInput); !errorcode.Is(err, errorcode.InvalidArgument) {
		t.Fatalf("malformed image=%v", err)
	}
	statuses, err := client.SubagentInputStatuses(t.Context(), appserver.SubagentInputStatusRequest{SessionID: "session-1", IDs: []string{"input"}})
	if err != nil || len(statuses) != 1 || statuses[0] != receipt {
		t.Fatalf("statuses=%#v,%v", statuses, err)
	}
	req.SessionID = "other-session"
	if _, err := client.SubmitSubagentInput(t.Context(), req); err == nil {
		t.Fatal("cross-Session input accepted")
	}
	pref := uipreferences.Preferences{SubagentLayout: uipreferences.Left, HorizontalRatio: 62, VerticalRatio: 39}
	if err := client.SaveUIPreferences(t.Context(), pref); err != nil {
		t.Fatal(err)
	}
	invalid := pref
	invalid.HorizontalRatio = 1
	if err := client.SaveUIPreferences(t.Context(), invalid); !errorcode.Is(err, errorcode.InvalidArgument) {
		t.Fatalf("invalid preference=%v", err)
	}
	got, err := client.LoadUIPreferences(t.Context())
	if err != nil || got != pref {
		t.Fatalf("preferences=%#v,%v", got, err)
	}
}

func TestUIPreferencesHTTPSparseUpdateAndValidation(t *testing.T) {
	backend := &workspaceHTTPBackend{preferences: uipreferences.Preferences{SubagentLayout: uipreferences.Left, HorizontalRatio: 62, VerticalRatio: 39}}
	services := testAppServerServices(&fakeService{}, staticStatusService{})
	services.UIPreferences = &appserver.UIPreferencesService{Store: backend}
	server, err := New(HandlerConfig{Services: services, Authenticator: testAuthenticator(), AllowedHosts: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	client, err := httpclient.New(httpclient.Config{BaseURL: "http://127.0.0.1", BearerToken: "test-token", HTTPClient: &http.Client{Transport: controlHandlerRoundTripper{handler: server}}, Compatibility: appserver.CurrentCompatibility()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SaveUIPreferences(t.Context(), uipreferences.Preferences{Theme: "catppuccin"}); err != nil {
		t.Fatal(err)
	}
	want := uipreferences.Preferences{SubagentLayout: uipreferences.Left, HorizontalRatio: 62, VerticalRatio: 39, Theme: "catppuccin"}
	got, err := client.LoadUIPreferences(t.Context())
	if err != nil || got != want {
		t.Fatalf("sparse theme=%#v,%v", got, err)
	}
	if err := client.SaveUIPreferences(t.Context(), uipreferences.Preferences{}); err != nil {
		t.Fatal(err)
	}
	got, err = client.LoadUIPreferences(t.Context())
	if err != nil || got != want {
		t.Fatalf("empty save=%#v,%v", got, err)
	}
	if err := client.SaveUIPreferences(t.Context(), uipreferences.Preferences{Theme: "not-a-palette"}); err != nil {
		t.Fatal(err)
	}
	if backend.preferences.Theme != "not-a-palette" {
		t.Fatalf("opaque theme rejected=%#v", backend.preferences)
	}
	if err := client.SaveUIPreferences(t.Context(), uipreferences.Preferences{SubagentLayout: "grid"}); !errorcode.Is(err, errorcode.InvalidArgument) {
		t.Fatalf("invalid layout=%v", err)
	}
	put := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPut, apiPrefix+"/presentation/preferences", strings.NewReader(body))
		request.Host = "127.0.0.1"
		authorizeTestRequest(request)
		setJSONContentType(request)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		return recorder
	}
	if recorder := put(`{"theme":"auto"}`); recorder.Code != http.StatusOK {
		t.Fatalf("sparse auto theme status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if backend.preferences.Theme != "auto" || backend.preferences.SubagentLayout != uipreferences.Left {
		t.Fatalf("raw sparse auto=%#v", backend.preferences)
	}
	beforeZeros := backend.preferences
	if recorder := put(`{"subagent_layout":"","horizontal_ratio":0,"vertical_ratio":0,"theme":""}`); recorder.Code != http.StatusOK {
		t.Fatalf("raw zero no-op status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if backend.preferences != beforeZeros {
		t.Fatalf("raw zeros mutated=%#v", backend.preferences)
	}
	if recorder := put(`{"horizontal_ratio":1}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("ratio 1 status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := put(`{"theme":"catppuccin","unknown":true}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
