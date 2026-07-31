package controlserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/client/wirev1/generated"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

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
		reconnectState: controlclient.SessionState{
			ProtocolVersion: schema.CurrentProtocolVersion,
			EnvelopeVersion: controlclient.EnvelopeVersion,
			APIVersion:      controlclient.HTTPAPIVersion,
			SessionID:       "session-1", Revision: math.MaxUint64,
			ResumeMode: controlclient.ResumeModeExact, BoundaryCursor: "cursor-boundary",
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
	subscription.err = &controlclient.FeedGapError{
		Cause: errors.New("splice overtaken"), RetryCursor: "retry-cursor",
		Mode: controlclient.ResumeModeDurableFallback, TransientGap: true,
	}
	server := newTestServer(t, &fakeService{
		subscription: subscription,
		reconnectState: controlclient.SessionState{
			ProtocolVersion: schema.CurrentProtocolVersion,
			EnvelopeVersion: controlclient.EnvelopeVersion,
			APIVersion:      controlclient.HTTPAPIVersion,
			SessionID:       "session-1",
			ResumeMode:      controlclient.ResumeModeExact,
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
	if _, err := New(HandlerConfig{Service: &fakeService{}, TaskStreams: &fakeTaskService{}, AllowedHosts: []string{"example.test"}}); err == nil {
		t.Fatal("New accepted an unauthenticated HTTP handler")
	}
	if _, err := New(HandlerConfig{Service: &fakeService{}, TaskStreams: &fakeTaskService{}, Authenticator: testAuthenticator()}); err == nil {
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
	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions", nil)
	request.Host = "example.test"
	request.Header.Set("Origin", "http://example.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	authorizeTestRequest(request)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || service.listCalls != 1 {
		t.Fatalf("status = %d, calls = %d, body = %s", recorder.Code, service.listCalls, recorder.Body.String())
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
	controlclient.Service
	principal      controlclient.Principal
	created        controlclient.CreateSessionRequest
	closed         controlclient.CloseSessionRequest
	prompted       controlclient.PromptRequest
	promptResult   *controlclient.CommandResult
	promptErr      error
	subscription   controlclient.FeedSubscription
	reconnectReq   controlclient.ReconnectRequest
	reconnectState controlclient.SessionState
	listCalls      int
	inspectErr     error
}

func (s *fakeService) ListSessions(context.Context, controlclient.Principal, controlclient.ListSessionsRequest) (session.SessionList, error) {
	s.listCalls++
	return session.SessionList{}, nil
}
func (s *fakeService) CreateSession(_ context.Context, principal controlclient.Principal, req controlclient.CreateSessionRequest) (controlclient.CommandResult, error) {
	s.principal = principal
	s.created = req
	return controlclient.CommandResult{OperationID: req.OperationID, Outcome: controlclient.OutcomeCommitted, SessionID: "session-created", Revision: 1}, nil
}
func (s *fakeService) CloseSession(_ context.Context, principal controlclient.Principal, req controlclient.CloseSessionRequest) (controlclient.CommandResult, error) {
	s.principal = principal
	s.closed = req
	return controlclient.CommandResult{OperationID: req.OperationID, Outcome: controlclient.OutcomeCommitted, SessionID: req.SessionID, Revision: 2}, nil
}
func (s *fakeService) Prompt(_ context.Context, principal controlclient.Principal, req controlclient.PromptRequest) (controlclient.CommandResult, error) {
	s.principal = principal
	s.prompted = req
	if s.promptResult != nil {
		return *s.promptResult, s.promptErr
	}
	return controlclient.CommandResult{
		OperationID: req.OperationID,
		Outcome:     controlclient.OutcomeCommitted,
		SessionID:   req.SessionID,
		Revision:    math.MaxUint64,
		Target: controlclient.TurnTarget{
			HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
		},
	}, nil
}
func (s *fakeService) InspectSession(context.Context, controlclient.Principal, controlclient.StateRequest) (controlclient.SessionState, error) {
	return controlclient.SessionState{}, s.inspectErr
}
func (s *fakeService) Subscribe(context.Context, controlclient.Principal, controlclient.SubscribeRequest) (controlclient.SubscribeResult, error) {
	return controlclient.SubscribeResult{Subscription: s.subscription, Mode: controlclient.ResumeModeExact, BoundaryCursor: "signed-cursor-1"}, nil
}
func (s *fakeService) Reconnect(_ context.Context, _ controlclient.Principal, req controlclient.ReconnectRequest) (controlclient.ReconnectResult, error) {
	s.reconnectReq = req
	return controlclient.ReconnectResult{State: s.reconnectState, Subscription: s.subscription}, nil
}

func newTestServer(t *testing.T, service controlclient.Service, heartbeat time.Duration) *Server {
	t.Helper()
	server, err := New(HandlerConfig{
		Service: service, TaskStreams: &fakeTaskService{}, Authenticator: testAuthenticator(),
		AllowedHosts: []string{"example.test", "127.0.0.1"}, Heartbeat: heartbeat,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

type fakeTaskService struct {
	principal taskstream.Principal
	list      taskstream.ListResult
	batch     taskstream.Batch
	subscribe taskstream.SubscribeResult
	err       error
}

func (s *fakeTaskService) List(_ context.Context, principal taskstream.Principal, _ taskstream.ListRequest) (taskstream.ListResult, error) {
	s.principal = principal
	return s.list, s.err
}

func (s *fakeTaskService) Events(_ context.Context, principal taskstream.Principal, _ taskstream.ReadRequest) (taskstream.Batch, error) {
	s.principal = principal
	return s.batch, s.err
}

func (s *fakeTaskService) Subscribe(_ context.Context, principal taskstream.Principal, _ taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	s.principal = principal
	return s.subscribe, s.err
}

var _ taskstream.Service = (*fakeTaskService)(nil)

func testAuthenticator() Authenticator {
	return AuthenticatorFunc(func(request *http.Request) (controlclient.Principal, error) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			return controlclient.Principal{}, errors.New("invalid bearer")
		}
		return controlclient.Principal{ID: "trusted-owner"}, nil
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
