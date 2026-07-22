package appserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	controlclientport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/appserver/generated"
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

func TestHTTPParticipantAttachUsesProfileAndEffortContract(t *testing.T) {
	service := &fakeService{}
	server := newTestServer(t, service, 0)
	request := httptest.NewRequest(http.MethodPost, apiPrefix+"/sessions/session-1/participants", strings.NewReader(`{"profile_id":"acp:helper","effort":"xhigh","role":"sidecar","label":"Helper"}`))
	authorizeTestRequest(request)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "attach-operation-1")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.attached.ProfileID != "acp:helper" || service.attached.Effort != "xhigh" || service.attached.SessionID != "session-1" || service.attached.OperationID != "attach-operation-1" {
		t.Fatalf("attach request = %#v", service.attached)
	}

	legacy := httptest.NewRequest(http.MethodPost, apiPrefix+"/sessions/session-1/participants", strings.NewReader(`{"agent":"helper","role":"sidecar"}`))
	authorizeTestRequest(legacy)
	legacy.Header.Set("Content-Type", "application/json")
	legacy.Header.Set("Idempotency-Key", "attach-operation-2")
	legacyRecorder := httptest.NewRecorder()
	server.ServeHTTP(legacyRecorder, legacy)
	if legacyRecorder.Code != http.StatusBadRequest {
		t.Fatalf("legacy agent request status=%d body=%s", legacyRecorder.Code, legacyRecorder.Body.String())
	}
}

func TestSSEUsesCursorIDAndWholeEnvelopeData(t *testing.T) {
	want := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, Cursor: "signed-cursor-1", SessionID: "session-1",
		Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Anchor: eventstream.DurableFeedPosition{Seq: math.MaxUint64}, Generation: "generation-1", Sequence: math.MaxUint64,
		}},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update:   schema.ContentChunk{SessionUpdate: schema.UpdateAgentMessage, Content: schema.TextContent{Type: "text", Text: "hello"}},
		Meta: map[string]any{"compact": map[string]any{
			"summarized_through_seq": uint64(math.MaxUint64),
		}},
	}
	subscription := newTestSubscription(want)
	service := &fakeService{subscription: subscription}
	server := newTestServer(t, service, time.Hour)
	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/session-1/stream", nil)
	authorizeTestRequest(request)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	if response.Header.Get(resumeModeHeader) != string(controlclientport.ResumeModeExact) || response.Header.Get(transientGapHeader) != "false" || response.Header.Get(boundaryCursorHeader) != "signed-cursor-1" {
		t.Fatalf("SSE resume headers = %#v", response.Header)
	}
	reader := bufio.NewReader(response.Body)
	eventLine, _ := reader.ReadString('\n')
	resumeData, _ := reader.ReadString('\n')
	_, _ = reader.ReadString('\n')
	if eventLine != "event: caelis.control.resume\n" {
		t.Fatalf("resume event line = %q", eventLine)
	}
	var boundary resumeBoundary
	if err := json.Unmarshal(bytes.TrimSpace([]byte(strings.TrimPrefix(resumeData, "data: "))), &boundary); err != nil {
		t.Fatal(err)
	}
	if boundary.ResumeMode != controlclientport.ResumeModeExact || boundary.TransientGap || boundary.BoundaryCursor != "signed-cursor-1" {
		t.Fatalf("resume boundary = %#v", boundary)
	}
	idLine, _ := reader.ReadString('\n')
	dataLine, _ := reader.ReadString('\n')
	if idLine != "id: signed-cursor-1\n" || !strings.HasPrefix(dataLine, "data: ") {
		t.Fatalf("SSE = %q %q", idLine, dataLine)
	}
	if !strings.Contains(dataLine, `"seq":"18446744073709551615"`) ||
		!strings.Contains(dataLine, `"sequence":"18446744073709551615"`) ||
		!strings.Contains(dataLine, `"summarized_through_seq":"18446744073709551615"`) {
		t.Fatalf("SSE lost max uint64 precision: %s", dataLine)
	}
	wantJSON, err := marshalEnvelope(want)
	if err != nil {
		t.Fatal(err)
	}
	var gotObject, wantObject any
	if err := json.Unmarshal(bytes.TrimSpace([]byte(strings.TrimPrefix(dataLine, "data: "))), &gotObject); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wantJSON, &wantObject); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotObject, wantObject) {
		t.Fatalf("SSE Envelope = %#v, want %#v", gotObject, wantObject)
	}
}

func TestSSEReportsTypedGapWithRetryCursor(t *testing.T) {
	subscription := newTestSubscription()
	subscription.err = &controlclientport.FeedGapError{
		Cause: errors.New("splice overtaken"), RetryCursor: "retry-cursor",
		Mode: controlclientport.ResumeModeDurableFallback, TransientGap: true,
	}
	server := newTestServer(t, &fakeService{subscription: subscription}, time.Hour)
	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/session-1/stream", nil)
	authorizeTestRequest(request)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), "event: "+resumeEventName) != 2 ||
		!strings.Contains(string(body), `"resume_mode":"durable_fallback"`) ||
		!strings.Contains(string(body), `"transient_gap":true`) ||
		!strings.Contains(string(body), `"boundary_cursor":"retry-cursor"`) {
		t.Fatalf("SSE typed gap body = %q", body)
	}
}

func TestSSERejectsMismatchedResumeInputsAndCredentialQuery(t *testing.T) {
	server := newTestServer(t, &fakeService{}, 0)
	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/session-1/stream?after=a", nil)
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
	if _, err := New(Config{Service: &fakeService{}, AllowedHosts: []string{"example.test"}}); err == nil {
		t.Fatal("New accepted an unauthenticated HTTP handler")
	}
	if _, err := New(Config{Service: &fakeService{}, Authenticator: testAuthenticator()}); err == nil {
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
	controlclientport.Service
	principal    controlclient.Principal
	created      controlclient.CreateSessionRequest
	attached     controlclient.AttachParticipantRequest
	subscription controlclientport.FeedSubscription
	listCalls    int
	inspectErr   error
}

func (s *fakeService) ListSessions(context.Context, controlclient.Principal, controlclientport.ListSessionsRequest) (session.SessionList, error) {
	s.listCalls++
	return session.SessionList{}, nil
}
func (s *fakeService) CreateSession(_ context.Context, principal controlclient.Principal, req controlclient.CreateSessionRequest) (controlclient.CommandResult, error) {
	s.principal = principal
	s.created = req
	return controlclient.CommandResult{OperationID: req.OperationID, Outcome: controlclient.OutcomeCommitted, SessionID: "session-created", Revision: 1}, nil
}
func (s *fakeService) AttachParticipant(_ context.Context, principal controlclient.Principal, req controlclient.AttachParticipantRequest) (controlclient.CommandResult, error) {
	s.principal = principal
	s.attached = req
	return controlclient.CommandResult{OperationID: req.OperationID, Outcome: controlclient.OutcomeCommitted, SessionID: req.SessionID, Revision: 2}, nil
}
func (s *fakeService) InspectSession(context.Context, controlclient.Principal, controlclientport.StateRequest) (controlclientport.SessionState, error) {
	return controlclientport.SessionState{}, s.inspectErr
}
func (s *fakeService) Subscribe(context.Context, controlclient.Principal, controlclientport.SubscribeRequest) (controlclientport.SubscribeResult, error) {
	return controlclientport.SubscribeResult{Subscription: s.subscription, Mode: controlclientport.ResumeModeExact, BoundaryCursor: "signed-cursor-1"}, nil
}

func newTestServer(t *testing.T, service controlclientport.Service, heartbeat time.Duration) *Server {
	t.Helper()
	server, err := New(Config{
		Service: service, Authenticator: testAuthenticator(),
		AllowedHosts: []string{"example.test", "127.0.0.1"}, Heartbeat: heartbeat,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

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
	events chan eventstream.Envelope
	err    error
}

func newTestSubscription(events ...eventstream.Envelope) *testSubscription {
	channel := make(chan eventstream.Envelope, len(events))
	for _, event := range events {
		channel <- event
	}
	close(channel)
	return &testSubscription{events: channel}
}
func (s *testSubscription) Events() <-chan eventstream.Envelope { return s.events }
func (*testSubscription) Backfill() <-chan eventstream.Envelope {
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
