package controlserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/control/appserver/wirev1/generated"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestHostHealthReadinessAndInitializeExposeOneInstance(t *testing.T) {
	ready := false
	instanceID := "11111111-1111-4111-8111-111111111111"
	server, err := New(HandlerConfig{
		Services:      testAppServerServices(&fakeService{}, staticStatusService{}),
		Authenticator: testAuthenticator(), AllowedHosts: []string{"example.test"},
		ServerInfo: appserver.ServerInfo{
			ServerID: appserver.ServerIdentity, InstanceID: instanceID,
			Capabilities: appserver.RequiredManagedHostCapabilities(), Transports: []string{"http"},
		},
		Ready: func() bool { return ready },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Host = "example.test"
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var health appserver.HostStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.InstanceID != instanceID || health.Ready {
		t.Fatalf("health = %#v", health)
	}

	request = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	request.Host = "example.test"
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	ready = true
	request = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	request.Host = "example.test"
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("ready status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, apiPrefix+"/initialize", nil)
	authorizeTestRequest(request)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("initialize status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var info appserver.ServerInfo
	if err := wirev1.Unmarshal(recorder.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.ServerID != appserver.ServerIdentity || info.InstanceID != instanceID ||
		!slices.Contains(info.Capabilities, appserver.CapabilityMultiWorkspace) ||
		!slices.Contains(info.Capabilities, appserver.CapabilityWorkspaceCWDList) {
		t.Fatalf("initialize = %#v", info)
	}
}

func TestHTTPCreateUsesTrustedPrincipalAndHeaderContracts(t *testing.T) {
	service := &fakeService{}
	server := newTestServer(t, service, 0)
	body := `{"workspace_key":"workspace-a","title":"hello","expected_revision":"8"}`
	request := httptest.NewRequest(http.MethodPost, apiPrefix+"/sessions", strings.NewReader(body))
	authorizeTestRequest(request)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "operation-1")
	request.Header.Set("If-Match", `"8"`)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.principal.ID != "trusted-owner" || service.created.OperationID != "operation-1" || service.created.ExpectedRevision == nil || *service.created.ExpectedRevision != 8 {
		t.Fatalf("principal/request = %#v %#v", service.principal, service.created)
	}
	want, err := os.ReadFile("testdata/command_result.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(recorder.Body.String()) != strings.TrimSpace(string(want)) {
		t.Fatalf("response:\n%s\nwant:\n%s", recorder.Body.String(), want)
	}
}

func TestHTTPCompactUsesSessionPathAndWriteHeaders(t *testing.T) {
	service := &fakeService{}
	server := newTestServer(t, service, 0)
	request := httptest.NewRequest(
		http.MethodPost,
		apiPrefix+"/sessions/session-1/compact",
		strings.NewReader(`{"expected_controller_epoch":"epoch-1"}`),
	)
	authorizeTestRequest(request)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "operation-compact")
	request.Header.Set("If-Match", `"8"`)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.principal.ID != "trusted-owner" ||
		service.compacted.SessionID != "session-1" ||
		service.compacted.OperationID != "operation-compact" ||
		service.compacted.ExpectedRevision == nil || *service.compacted.ExpectedRevision != 8 ||
		service.compacted.ExpectedControllerEpoch != "epoch-1" {
		t.Fatalf("principal/request = %#v %#v", service.principal, service.compacted)
	}
}

func TestHTTPStatusAddressesSessionAndDiagnostics(t *testing.T) {
	statusService := &recordingStatusService{status: controlstatus.StatusSnapshot{
		Configuration: controlstatus.StatusConfiguration{Revision: math.MaxUint64},
		Session:       controlstatus.StatusSession{ID: "session-1", Surface: "pet"},
	}}
	server, err := New(HandlerConfig{
		Services:      testAppServerServices(&fakeService{}, statusService),
		Authenticator: testAuthenticator(), AllowedHosts: []string{"example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodGet,
		apiPrefix+"/sessions/session-1/status?workspace_key=workspace-a&cwd=%2Ftmp%2Fworkspace-a&surface=pet&diagnostics=true",
		nil,
	)
	authorizeTestRequest(request)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if statusService.principal.ID != "trusted-owner" ||
		statusService.request.SessionID != "session-1" ||
		statusService.request.WorkspaceKey != "workspace-a" ||
		statusService.request.CWD != "/tmp/workspace-a" ||
		statusService.request.Surface != "pet" ||
		!statusService.request.IncludeDiagnostics {
		t.Fatalf("principal/request = %#v %#v", statusService.principal, statusService.request)
	}
	var got controlstatus.StatusSnapshot
	if err := wirev1.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Configuration.Revision != math.MaxUint64 || got.Session.ID != "session-1" || got.Session.Surface != "pet" {
		t.Fatalf("status response = %#v", got)
	}
}

func TestReconnectSSEBootstrapsStateBeforeBackfillAndLiveEvents(t *testing.T) {
	backfill := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, Cursor: "cursor-backfill", SessionID: "session-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "backfill"},
		},
	}
	live := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, Cursor: "cursor-live", SessionID: "session-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "live"},
		},
	}
	subscription := newTestSubscription(live)
	subscription.backfill = envelopeChannel(backfill)
	service := &fakeService{
		subscription: subscription,
		reconnectState: appserver.SessionState{
			ProtocolVersion: acpsdk.ProtocolVersionNumber,
			EnvelopeVersion: appserver.EnvelopeVersion,
			APIVersion:      appserver.HTTPAPIVersion,
			SessionID:       "session-1", Revision: math.MaxUint64,
			ResumeMode: appserver.ResumeModeExact, BoundaryCursor: "cursor-boundary",
		},
	}
	server := newTestServer(t, service, time.Hour)
	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/session-1/reconnect?after=cursor-client", nil)
	authorizeTestRequest(request)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()

	if service.reconnectReq.SessionID != "session-1" || service.reconnectReq.Cursor != "cursor-client" {
		t.Fatalf("Reconnect request = %#v", service.reconnectReq)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	bootstrapAt := strings.Index(text, "event: "+bootstrapEventName)
	backfillAt := strings.Index(text, "id: cursor-backfill")
	doneAt := strings.Index(text, "event: "+backfillDoneEventName)
	liveAt := strings.Index(text, "id: cursor-live")
	if bootstrapAt < 0 || backfillAt <= bootstrapAt || doneAt <= backfillAt || liveAt <= doneAt {
		t.Fatalf("Reconnect SSE ordering is not bootstrap -> backfill -> marker -> live:\n%s", text)
	}
	if !strings.Contains(text, `"revision":"18446744073709551615"`) ||
		response.Header.Get(boundaryCursorHeader) != "cursor-boundary" {
		t.Fatalf("Reconnect bootstrap lost state or boundary: headers=%#v body=%s", response.Header, text)
	}
}

func TestReconnectReportsTypedGapWithRetryCursor(t *testing.T) {
	subscription := newTestSubscription()
	subscription.err = &appserver.FeedGapError{
		Cause: errors.New("splice overtaken"), RetryCursor: "retry-cursor",
		Mode: appserver.ResumeModeDurableFallback, TransientGap: true,
	}
	server := newTestServer(t, &fakeService{
		subscription: subscription,
		reconnectState: appserver.SessionState{
			ProtocolVersion: acpsdk.ProtocolVersionNumber,
			EnvelopeVersion: appserver.EnvelopeVersion,
			APIVersion:      appserver.HTTPAPIVersion,
			SessionID:       "session-1",
			ResumeMode:      appserver.ResumeModeExact,
		},
	}, time.Hour)
	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/session-1/reconnect", nil)
	authorizeTestRequest(request)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), "event: "+resumeEventName) != 1 ||
		!strings.Contains(string(body), `"resume_mode":"durable_fallback"`) ||
		!strings.Contains(string(body), `"transient_gap":true`) ||
		!strings.Contains(string(body), `"boundary_cursor":"retry-cursor"`) {
		t.Fatalf("SSE typed gap body = %q", body)
	}
}

func TestReconnectRejectsMismatchedResumeInputsAndCredentialQuery(t *testing.T) {
	server := newTestServer(t, &fakeService{}, 0)
	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/session-1/reconnect?after=a", nil)
	authorizeTestRequest(request)
	request.Header.Set("Last-Event-ID", "b")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("resume mismatch status = %d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions?token=secret", nil)
	authorizeTestRequest(request)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("query credential status = %d", recorder.Code)
	}
}

func TestNewRequiresNetworkAuthenticatorAndHostAllowlist(t *testing.T) {
	if _, err := New(HandlerConfig{Services: testAppServerServices(&fakeService{}, staticStatusService{}), AllowedHosts: []string{"example.test"}}); err == nil {
		t.Fatal("New accepted an unauthenticated HTTP handler")
	}
	if _, err := New(HandlerConfig{Services: testAppServerServices(&fakeService{}, staticStatusService{}), Authenticator: testAuthenticator()}); err == nil {
		t.Fatal("New accepted an empty Host allowlist")
	}
}

func TestRequestTrustPolicyRejectsBrowserAndRebindingInputsBeforeService(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		header http.Header
	}{
		{name: "host", host: "evil.example"},
		{name: "malformed host", host: "example.test@evil.example"},
		{name: "cross host origin", host: "example.test", header: http.Header{"Origin": {"http://evil.example"}}},
		{name: "cross scheme origin", host: "example.test", header: http.Header{"Origin": {"https://example.test"}}},
		{name: "origin port mismatch", host: "example.test:7777", header: http.Header{"Origin": {"http://example.test:8888"}}},
		{name: "duplicate origin", host: "example.test", header: http.Header{"Origin": {"http://example.test", "http://example.test"}}},
		{name: "fetch metadata", host: "example.test", header: http.Header{"Sec-Fetch-Site": {"cross-site"}}},
		{name: "duplicate fetch metadata", host: "example.test", header: http.Header{"Sec-Fetch-Site": {"same-origin", "same-origin"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeService{}
			server := newTestServer(t, service, 0)
			request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions", nil)
			request.Host = tt.host
			request.Header = tt.header.Clone()
			authorizeTestRequest(request)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if service.listCalls != 0 {
				t.Fatalf("Service called %d times", service.listCalls)
			}
		})
	}
}

func TestSameOriginHostAndBearerAuthentication(t *testing.T) {
	service := &fakeService{}
	server := newTestServer(t, service, 0)
	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions?cwd=%2Fworkspace", nil)
	request.Host = "example.test"
	request.Header.Set("Origin", "http://example.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	authorizeTestRequest(request)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || service.listCalls != 1 {
		t.Fatalf("status = %d, calls = %d, body = %s", recorder.Code, service.listCalls, recorder.Body.String())
	}
	if service.listed.CWD != "/workspace" {
		t.Fatalf("ListSessions request = %#v", service.listed)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

func TestMissingAndWrongBearerReturn401(t *testing.T) {
	server := newTestServer(t, &fakeService{}, 0)
	for _, authorization := range []string{"", "Bearer wrong-token"} {
		request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions", nil)
		request.Host = "example.test"
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q status = %d", authorization, recorder.Code)
		}
		if got := recorder.Header().Get("WWW-Authenticate"); got != `Bearer realm="caelis-control"` {
			t.Fatalf("WWW-Authenticate = %q", got)
		}
	}
}

func TestAuthenticatedCrossSessionAccessReturns403(t *testing.T) {
	service := &fakeService{inspectErr: errorcode.New(errorcode.PermissionDenied, "session belongs to another principal")}
	server := newTestServer(t, service, 0)
	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/other-session/state", nil)
	request.Host = "example.test"
	authorizeTestRequest(request)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), `"forbidden"`) {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestHTTPJSONRequestBodyLimitAcceptsExactCapAndRejectsOverflow(t *testing.T) {
	tests := []struct {
		name       string
		size       int
		wantStatus int
		wantCode   errorcode.Code
	}{
		{name: "former 1 MiB cap plus one", size: 1<<20 + 1, wantStatus: http.StatusOK},
		{name: "exact JSON cap", size: maxJSONRequestBytes, wantStatus: http.StatusOK},
		{
			name:       "one byte over JSON cap",
			size:       maxJSONRequestBytes + 1,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   errorcode.ResourceExhausted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeService{}
			server := newTestServer(t, service, 0)
			request := httptest.NewRequest(http.MethodPost, apiPrefix+"/sessions", bytes.NewReader(jsonRequestBodyOfSize(t, tt.size)))
			request.Host = "example.test"
			authorizeTestRequest(request)
			setJSONContentType(request)
			request.Header.Set("Idempotency-Key", "operation-1")
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			if tt.wantStatus == http.StatusOK {
				if service.created.OperationID != "operation-1" {
					t.Fatalf("Service missed in-cap request: %#v", service.created)
				}
				return
			}
			if service.created.OperationID != "" {
				t.Fatalf("Service received oversized request: %#v", service.created)
			}
			var payload map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode 413 body: %v", err)
			}
			if payload["error"] != jsonRequestBodyTooLargeDetail() {
				t.Fatalf("error = %#v, want %q", payload["error"], jsonRequestBodyTooLargeDetail())
			}
			if payload["code"] != string(tt.wantCode) {
				t.Fatalf("code = %#v, want %q", payload["code"], tt.wantCode)
			}
		})
	}
}

func TestHTTPPromptAcceptsMaximumInlineImageAfterBase64Expansion(t *testing.T) {
	service := &fakeService{}
	server := newTestServer(t, service, 0)
	encodedLen := base64.StdEncoding.EncodedLen(appserver.MaxPromptImageBytes)
	encoded := strings.Repeat("A", encodedLen-1) + "="
	body, err := wirev1.Marshal(appserver.PromptRequest{
		Input: "inspect",
		ContentParts: []model.ContentPart{{
			Type: model.ContentPartImage, MimeType: "image/png", Data: encoded, FileName: "shot.png",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 16<<20 || len(body) >= maxJSONRequestBytes {
		t.Fatalf("maximum inline-image request size = %d, want between former and current caps", len(body))
	}
	request := httptest.NewRequest(http.MethodPost, apiPrefix+"/sessions/session-1/prompt", bytes.NewReader(body))
	request.Host = "example.test"
	authorizeTestRequest(request)
	setJSONContentType(request)
	request.Header.Set("Idempotency-Key", "operation-image-max")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	if len(service.prompted.ContentParts) != 1 || len(service.prompted.ContentParts[0].Data) != len(encoded) {
		t.Fatalf("service prompt content parts = %#v, want maximum inline image", service.prompted.ContentParts)
	}
}

func jsonRequestBodyOfSize(t *testing.T, n int) []byte {
	t.Helper()
	const prefix = `{"workspace_key":"workspace-a","title":"`
	const suffix = `"}`
	if n < len(prefix)+len(suffix) {
		t.Fatalf("jsonRequestBodyOfSize(%d) is smaller than the JSON wrapper", n)
	}
	body := make([]byte, n)
	copy(body, prefix)
	for i := len(prefix); i < n-len(suffix); i++ {
		body[i] = 'a'
	}
	copy(body[n-len(suffix):], suffix)
	return body
}

func TestMalformedWriteInputsReturn400BeforeService(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		mutate func(*http.Request)
	}{
		{name: "missing content type", body: `{}`},
		{name: "trailing JSON", body: `{}{}`, mutate: setJSONContentType},
		{name: "numeric revision", body: `{"expected_revision":9007199254740993}`, mutate: setJSONContentType},
		{name: "noncanonical revision", body: `{"expected_revision":"01"}`, mutate: setJSONContentType},
		{name: "unsafe extension integer", body: `{"metadata":{"unsafe":9007199254740993}}`, mutate: setJSONContentType},
		{name: "unquoted If-Match", body: `{}`, mutate: func(request *http.Request) {
			setJSONContentType(request)
			request.Header.Set("If-Match", "9")
		}},
		{name: "duplicate If-Match", body: `{}`, mutate: func(request *http.Request) {
			setJSONContentType(request)
			request.Header.Add("If-Match", `"9"`)
			request.Header.Add("If-Match", `"9"`)
		}},
		{name: "duplicate idempotency key", body: `{}`, mutate: func(request *http.Request) {
			setJSONContentType(request)
			request.Header.Add("Idempotency-Key", "operation-2")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeService{}
			server := newTestServer(t, service, 0)
			request := httptest.NewRequest(http.MethodPost, apiPrefix+"/sessions", strings.NewReader(tt.body))
			request.Host = "example.test"
			authorizeTestRequest(request)
			request.Header.Set("Idempotency-Key", "operation-1")
			if tt.mutate != nil {
				tt.mutate(request)
			}
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if service.created.OperationID != "" {
				t.Fatalf("Service received malformed request: %#v", service.created)
			}
		})
	}
}

func TestHTTPHandlerRoundTripsMaxUint64Revision(t *testing.T) {
	service := &fakeService{}
	server := newTestServer(t, service, 0)
	const decimal = "18446744073709551615"
	request := httptest.NewRequest(http.MethodPost, apiPrefix+"/sessions", strings.NewReader(`{"expected_revision":"`+decimal+`"}`))
	request.Host = "example.test"
	authorizeTestRequest(request)
	setJSONContentType(request)
	request.Header.Set("Idempotency-Key", "operation-1")
	request.Header.Set("If-Match", `"`+decimal+`"`)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if service.created.ExpectedRevision == nil || *service.created.ExpectedRevision != math.MaxUint64 {
		t.Fatalf("expected revision = %#v", service.created.ExpectedRevision)
	}
}

func setJSONContentType(request *http.Request) {
	request.Header.Set("Content-Type", "application/json")
}

func TestOpenAPI31ContainsEveryGeneratedOperation(t *testing.T) {
	data, err := os.ReadFile("../../api/control/v1/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		OpenAPI string `json:"openapi"`
		Paths   map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI = %q", spec.OpenAPI)
	}
	found := map[string]bool{}
	for _, methods := range spec.Paths {
		for _, operation := range methods {
			found[operation.OperationID] = true
		}
	}
	for _, operationID := range generated.OperationIDs {
		if !found[operationID] {
			t.Fatalf("generated operation %q missing from OpenAPI", operationID)
		}
	}
}

type fakeService struct {
	appserver.Service
	principal      appserver.Principal
	created        appserver.CreateSessionRequest
	closed         appserver.CloseSessionRequest
	compacted      appserver.CompactSessionRequest
	prompted       appserver.PromptRequest
	promptResult   *appserver.CommandResult
	promptErr      error
	subscription   appserver.FeedSubscription
	reconnectReq   appserver.ReconnectRequest
	reconnectState appserver.SessionState
	listCalls      int
	listed         appserver.ListSessionsRequest
	inspectErr     error
}

func (s *fakeService) ListSessions(_ context.Context, _ appserver.Principal, req appserver.ListSessionsRequest) (session.SessionList, error) {
	s.listCalls++
	s.listed = req
	return session.SessionList{}, nil
}
func (s *fakeService) CreateSession(_ context.Context, principal appserver.Principal, req appserver.CreateSessionRequest) (appserver.CommandResult, error) {
	s.principal = principal
	s.created = req
	return appserver.CommandResult{OperationID: req.OperationID, Outcome: appserver.OutcomeCommitted, SessionID: "session-created", Revision: 1}, nil
}
func (s *fakeService) CloseSession(_ context.Context, principal appserver.Principal, req appserver.CloseSessionRequest) (appserver.CommandResult, error) {
	s.principal = principal
	s.closed = req
	return appserver.CommandResult{OperationID: req.OperationID, Outcome: appserver.OutcomeCommitted, SessionID: req.SessionID, Revision: 2}, nil
}
func (s *fakeService) CompactSession(_ context.Context, principal appserver.Principal, req appserver.CompactSessionRequest) (appserver.CommandResult, error) {
	s.principal = principal
	s.compacted = req
	return appserver.CommandResult{OperationID: req.OperationID, Outcome: appserver.OutcomeCommitted, SessionID: req.SessionID, Revision: 3}, nil
}
func (s *fakeService) Prompt(_ context.Context, principal appserver.Principal, req appserver.PromptRequest) (appserver.CommandResult, error) {
	s.principal = principal
	s.prompted = req
	if s.promptResult != nil {
		return *s.promptResult, s.promptErr
	}
	return appserver.CommandResult{
		OperationID: req.OperationID,
		Outcome:     appserver.OutcomeCommitted,
		SessionID:   req.SessionID,
		Revision:    math.MaxUint64,
		Target: appserver.TurnTarget{
			HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
		},
	}, nil
}
func (s *fakeService) InspectSession(context.Context, appserver.Principal, appserver.StateRequest) (appserver.SessionState, error) {
	return appserver.SessionState{}, s.inspectErr
}
func (s *fakeService) Subscribe(context.Context, appserver.Principal, appserver.SubscribeRequest) (appserver.SubscribeResult, error) {
	return appserver.SubscribeResult{Subscription: s.subscription, Mode: appserver.ResumeModeExact, BoundaryCursor: "signed-cursor-1"}, nil
}
func (s *fakeService) Reconnect(_ context.Context, _ appserver.Principal, req appserver.ReconnectRequest) (appserver.ReconnectResult, error) {
	s.reconnectReq = req
	return appserver.ReconnectResult{State: s.reconnectState, Subscription: s.subscription}, nil
}

func newTestServer(t *testing.T, service appserver.Service, heartbeat time.Duration) *Server {
	t.Helper()
	server, err := New(HandlerConfig{
		Services: testAppServerServices(service, staticStatusService{}), Authenticator: testAuthenticator(),
		AllowedHosts: []string{"example.test", "127.0.0.1"}, Heartbeat: heartbeat,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

type staticStatusService struct {
	status controlstatus.StatusSnapshot
	err    error
}

func (s staticStatusService) SessionStatus(context.Context, appserver.Principal, appserver.StatusRequest) (controlstatus.StatusSnapshot, error) {
	return s.status, s.err
}

type recordingStatusService struct {
	principal appserver.Principal
	request   appserver.StatusRequest
	status    controlstatus.StatusSnapshot
}

func (s *recordingStatusService) SessionStatus(_ context.Context, principal appserver.Principal, request appserver.StatusRequest) (controlstatus.StatusSnapshot, error) {
	s.principal = principal
	s.request = request
	return s.status, nil
}

type fakeTaskService struct {
	principal taskstream.Principal
	list      taskstream.ListResult
	batch     taskstream.Batch
	subscribe taskstream.SubscribeResult
	read      taskstream.ReadRequest
	request   taskstream.SubscribeRequest
	err       error
}

func (s *fakeTaskService) List(_ context.Context, principal taskstream.Principal, _ taskstream.ListRequest) (taskstream.ListResult, error) {
	s.principal = principal
	return s.list, s.err
}

func (s *fakeTaskService) Events(_ context.Context, principal taskstream.Principal, request taskstream.ReadRequest) (taskstream.Batch, error) {
	s.principal = principal
	s.read = request
	return s.batch, s.err
}

func (s *fakeTaskService) Subscribe(_ context.Context, principal taskstream.Principal, request taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	s.principal = principal
	s.request = request
	return s.subscribe, s.err
}

var _ taskstream.Service = (*fakeTaskService)(nil)

func testAuthenticator() Authenticator {
	return AuthenticatorFunc(func(request *http.Request) (appserver.Principal, error) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			return appserver.Principal{}, errors.New("invalid bearer")
		}
		return appserver.Principal{ID: "trusted-owner"}, nil
	})
}

func authorizeTestRequest(request *http.Request) {
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	if request.Host == "example.com" {
		request.Host = "example.test"
	}
	request.Header.Set("Authorization", "Bearer test-token")
}

type testSubscription struct {
	backfill chan eventstream.Envelope
	events   chan eventstream.Envelope
	err      error
}

func newTestSubscription(events ...eventstream.Envelope) *testSubscription {
	return &testSubscription{events: envelopeChannel(events...)}
}

func envelopeChannel(events ...eventstream.Envelope) chan eventstream.Envelope {
	channel := make(chan eventstream.Envelope, len(events))
	for _, event := range events {
		channel <- event
	}
	close(channel)
	return channel
}
func (s *testSubscription) Events() <-chan eventstream.Envelope { return s.events }
func (s *testSubscription) Backfill() <-chan eventstream.Envelope {
	if s.backfill != nil {
		return s.backfill
	}
	done := make(chan eventstream.Envelope)
	close(done)
	return done
}
func (*testSubscription) BackfillDone() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
func (*testSubscription) Close() error       { return nil }
func (s *testSubscription) Err() error       { return s.err }
func (*testSubscription) LastCursor() string { return "signed-cursor-1" }
