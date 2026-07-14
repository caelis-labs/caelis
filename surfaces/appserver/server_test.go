package appserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	controlclient "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/appserver/generated"
)

func TestHTTPCreateUsesTrustedPrincipalAndHeaderContracts(t *testing.T) {
	service := &fakeService{}
	server, err := New(Config{Service: service, LocalPrincipal: controlclient.Principal{ID: "trusted-owner"}})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"workspace_key":"workspace-a","title":"hello","expected_revision":8}`
	request := httptest.NewRequest(http.MethodPost, apiPrefix+"/sessions", strings.NewReader(body))
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

func TestSSEUsesCursorIDAndWholeEnvelopeData(t *testing.T) {
	want := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, Cursor: "signed-cursor-1", SessionID: "session-1",
		Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{Generation: "generation-1", Sequence: 1}},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update:   schema.ContentChunk{SessionUpdate: schema.UpdateAgentMessage, Content: schema.TextContent{Type: "text", Text: "hello"}},
	}
	subscription := newTestSubscription(want)
	service := &fakeService{subscription: subscription}
	server, err := New(Config{Service: service, LocalPrincipal: controlclient.Principal{ID: "owner"}, Heartbeat: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	response, err := http.Get(httpServer.URL + apiPrefix + "/sessions/session-1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get(resumeModeHeader) != string(controlclient.ResumeModeExact) || response.Header.Get(transientGapHeader) != "false" || response.Header.Get(boundaryCursorHeader) != "signed-cursor-1" {
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
	if boundary.ResumeMode != controlclient.ResumeModeExact || boundary.TransientGap || boundary.BoundaryCursor != "signed-cursor-1" {
		t.Fatalf("resume boundary = %#v", boundary)
	}
	idLine, _ := reader.ReadString('\n')
	dataLine, _ := reader.ReadString('\n')
	if idLine != "id: signed-cursor-1\n" || !strings.HasPrefix(dataLine, "data: ") {
		t.Fatalf("SSE = %q %q", idLine, dataLine)
	}
	wantJSON, err := json.Marshal(want)
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
	subscription.err = &controlclient.FeedGapError{
		Cause: errors.New("splice overtaken"), RetryCursor: "retry-cursor",
		Mode: controlclient.ResumeModeDurableFallback, TransientGap: true,
	}
	server, err := New(Config{
		Service:        &fakeService{subscription: subscription},
		LocalPrincipal: controlclient.Principal{ID: "owner"}, Heartbeat: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	response, err := http.Get(httpServer.URL + apiPrefix + "/sessions/session-1/stream")
	if err != nil {
		t.Fatal(err)
	}
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
	server, err := New(Config{Service: &fakeService{}, LocalPrincipal: controlclient.Principal{ID: "owner"}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions/session-1/stream?after=a", nil)
	request.Header.Set("Last-Event-ID", "b")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("resume mismatch status = %d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodGet, apiPrefix+"/sessions?token=secret", nil)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("query credential status = %d", recorder.Code)
	}
}

func TestValidateListenerFailsClosedOffLoopback(t *testing.T) {
	if err := ValidateListener("127.0.0.1:7777", nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidateListener("0.0.0.0:7777", nil); err == nil {
		t.Fatal("non-loopback unauthenticated listener accepted")
	}
	if err := ValidateListener("0.0.0.0:7777", AuthenticatorFunc(func(*http.Request) (controlclient.Principal, error) { return controlclient.Principal{ID: "owner"}, nil })); err != nil {
		t.Fatal(err)
	}
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
	principal    controlclient.Principal
	created      controlclient.CreateSessionRequest
	subscription controlclient.FeedSubscription
}

func (s *fakeService) CreateSession(_ context.Context, principal controlclient.Principal, req controlclient.CreateSessionRequest) (controlclient.CommandResult, error) {
	s.principal = principal
	s.created = req
	return controlclient.CommandResult{OperationID: req.OperationID, Outcome: controlclient.OutcomeCommitted, SessionID: "session-created", Revision: 1}, nil
}
func (s *fakeService) Subscribe(context.Context, controlclient.Principal, controlclient.SubscribeRequest) (controlclient.SubscribeResult, error) {
	return controlclient.SubscribeResult{Subscription: s.subscription, Mode: controlclient.ResumeModeExact, BoundaryCursor: "signed-cursor-1"}, nil
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
