package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/client/wirev1"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestNewRejectsInsecureRemoteOrigin(t *testing.T) {
	if _, err := New(Config{
		BaseURL: "http://control.example.test:7777", BearerToken: "secret",
	}); err == nil {
		t.Fatal("New accepted cleartext non-loopback bearer transport")
	}
	if _, err := New(Config{
		BaseURL: "https://control.example.test", BearerToken: "secret",
	}); err != nil {
		t.Fatalf("New rejected HTTPS remote origin: %v", err)
	}
}

func TestPromptPreservesTypedWriteContract(t *testing.T) {
	var prompted controlclient.PromptRequest
	client, closeServer := newFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case wirev1.APIPrefix + "/initialize":
			writeFixtureJSON(t, w, http.StatusOK, controlclient.ServerInfo{
				ProtocolVersion: schema.CurrentProtocolVersion,
				EnvelopeVersion: controlclient.EnvelopeVersion,
				APIVersion:      controlclient.HTTPAPIVersion,
			})
		case wirev1.APIPrefix + "/sessions/session-1/prompt":
			assertFixtureRequest(t, r, http.MethodPost, "operation-remote-prompt")
			decodeFixtureRequest(t, r, &prompted)
			if prompted.ExpectedRevision == nil {
				t.Fatal("Prompt request omitted expected_revision")
			}
			writeFixtureJSON(t, w, http.StatusOK, controlclient.CommandResult{
				OperationID: "operation-remote-prompt",
				Outcome:     controlclient.OutcomeCommitted,
				SessionID:   "session-1",
				Revision:    math.MaxUint64,
				Target: controlclient.TurnTarget{
					HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
				},
			})
		default:
			http.NotFound(w, r)
		}
	})
	defer closeServer()

	info, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.ProtocolVersion != schema.CurrentProtocolVersion ||
		info.EnvelopeVersion != controlclient.EnvelopeVersion ||
		info.APIVersion != controlclient.HTTPAPIVersion {
		t.Fatalf("Initialize result = %#v", info)
	}
	revision := uint64(math.MaxUint64)
	result, err := client.Prompt(context.Background(), controlclient.PromptRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:      "operation-remote-prompt",
			SessionID:        "session-1",
			ExpectedRevision: &revision,
		},
		Input: "hello from Pet",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != math.MaxUint64 ||
		result.Target.HandleID != "handle-1" ||
		result.Target.RunID != "run-1" ||
		result.Target.TurnID != "turn-1" {
		t.Fatalf("Prompt result = %#v", result)
	}
	if prompted.OperationID != "operation-remote-prompt" ||
		prompted.ExpectedRevision == nil ||
		*prompted.ExpectedRevision != math.MaxUint64 ||
		prompted.Input != "hello from Pet" {
		t.Fatalf("Prompt request = %#v", prompted)
	}
}

func TestCreatesAndClosesSessionThroughTypedFacade(t *testing.T) {
	var created controlclient.CreateSessionRequest
	var closed controlclient.CloseSessionRequest
	client, closeServer := newFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case wirev1.APIPrefix + "/sessions":
			assertFixtureRequest(t, r, http.MethodPost, "operation-create")
			decodeFixtureRequest(t, r, &created)
			writeFixtureJSON(t, w, http.StatusOK, controlclient.CommandResult{
				OperationID: created.OperationID,
				Outcome:     controlclient.OutcomeCommitted,
				SessionID:   "session-created",
				Revision:    1,
			})
		case wirev1.APIPrefix + "/sessions/session-created":
			assertFixtureRequest(t, r, http.MethodDelete, "operation-close")
			decodeFixtureRequest(t, r, &closed)
			writeFixtureJSON(t, w, http.StatusOK, controlclient.CommandResult{
				OperationID: closed.OperationID,
				Outcome:     controlclient.OutcomeCommitted,
				SessionID:   "session-created",
				Revision:    2,
			})
		default:
			http.NotFound(w, r)
		}
	})
	defer closeServer()

	createdResult, err := client.CreateSession(context.Background(), controlclient.CreateSessionRequest{
		WriteBase:          controlclient.WriteBase{OperationID: "operation-create"},
		PreferredSessionID: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if createdResult.SessionID != "session-created" || created.PreferredSessionID != "session-1" {
		t.Fatalf("CreateSession result/request = %#v / %#v", createdResult, created)
	}

	closedResult, err := client.CloseSession(context.Background(), controlclient.CloseSessionRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:             "operation-close",
			SessionID:               "session-created",
			ExpectedControllerEpoch: "epoch-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if closedResult.SessionID != "session-created" ||
		closed.OperationID != "operation-close" ||
		closed.ExpectedControllerEpoch != "epoch-1" {
		t.Fatalf("CloseSession result/request = %#v / %#v", closedResult, closed)
	}
}

func TestPreservesConflictedCommandRecoveryResult(t *testing.T) {
	client, closeServer := newFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertFixtureRequest(t, r, http.MethodPost, "operation-conflict")
		writeFixtureJSON(t, w, http.StatusConflict, controlclient.CommandResult{
			OperationID: "operation-conflict",
			Outcome:     controlclient.OutcomeConflicted,
			SessionID:   "session-1",
			Revision:    9,
			Detail:      "conflict",
		})
	})
	defer closeServer()

	result, err := client.Prompt(context.Background(), controlclient.PromptRequest{
		WriteBase: controlclient.WriteBase{
			OperationID: "operation-conflict",
			SessionID:   "session-1",
		},
		Input: "conflict",
	})
	var outcomeErr *controlclient.OutcomeError
	if !errors.As(err, &outcomeErr) ||
		outcomeErr.Outcome != controlclient.OutcomeConflicted ||
		result.Outcome != controlclient.OutcomeConflicted ||
		result.Revision != 9 {
		t.Fatalf("Prompt conflict = %#v, %v", result, err)
	}
}

func TestPromptReportsRemoteHostUnavailable(t *testing.T) {
	client, closeServer := newFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertFixtureRequest(t, r, http.MethodPost, "operation-unavailable")
		writeFixtureJSON(t, w, http.StatusServiceUnavailable, map[string]string{"error": "service unavailable"})
	})
	defer closeServer()

	result, err := client.Prompt(context.Background(), controlclient.PromptRequest{
		WriteBase: controlclient.WriteBase{
			OperationID: "operation-unavailable",
			SessionID:   "session-1",
		},
		Input: "retry after restart",
	})
	var remoteErr *RemoteError
	if !errors.As(err, &remoteErr) || remoteErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Prompt error = %T %v, want HTTP 503 RemoteError", err, err)
	}
	if result != (controlclient.CommandResult{}) {
		t.Fatalf("Prompt result = %#v, want zero result", result)
	}
}

func TestReconnectReturnsTypedAtomicSubscription(t *testing.T) {
	backfill := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, Cursor: "cursor-backfill", SessionID: "session-1",
		Position: &eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{
			Seq: math.MaxUint64,
		}},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryCanonical},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "replayed"},
		},
		Meta: map[string]any{"compact": map[string]any{
			"summarized_through_seq": uint64(math.MaxUint64),
		}},
	}
	live := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, Cursor: "cursor-live", SessionID: "session-1",
		Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Anchor:     eventstream.DurableFeedPosition{Seq: math.MaxUint64},
			Generation: "generation-1",
			Sequence:   math.MaxUint64,
		}},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update: schema.UsageUpdate{
			SessionUpdate: schema.UpdateUsage,
			Size:          math.MaxUint64,
			Used:          math.MaxUint64,
		},
	}
	state := controlclient.SessionState{
		ProtocolVersion: schema.CurrentProtocolVersion,
		EnvelopeVersion: controlclient.EnvelopeVersion,
		APIVersion:      controlclient.HTTPAPIVersion,
		SessionID:       "session-1",
		Revision:        math.MaxUint64,
		ResumeMode:      controlclient.ResumeModeExact,
		BoundaryCursor:  "cursor-boundary",
		Controller: session.ControllerBinding{
			ContextSyncSeq: math.MaxUint64,
		},
	}
	client, closeServer := newFixtureClient(t, reconnectFixture(t, state, []eventstream.Envelope{backfill}, []eventstream.Envelope{live}, true))
	defer closeServer()

	result, err := client.Reconnect(context.Background(), controlclient.ReconnectRequest{
		SessionID: "session-1", Cursor: "cursor-client",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	if result.State.Revision != math.MaxUint64 ||
		result.State.Controller.ContextSyncSeq != math.MaxUint64 ||
		result.State.BoundaryCursor != "cursor-boundary" {
		t.Fatalf("Reconnect state = %#v", result.State)
	}

	replayed := receiveRemoteEnvelope(t, result.Subscription.Backfill())
	if replayed.Cursor != "cursor-backfill" ||
		replayed.Position == nil ||
		replayed.Position.Durable == nil ||
		replayed.Position.Durable.Seq != math.MaxUint64 {
		t.Fatalf("backfill Envelope = %#v", replayed)
	}
	compact, ok := replayed.Meta["compact"].(map[string]any)
	if !ok || compact["summarized_through_seq"] != uint64(math.MaxUint64) {
		t.Fatalf("backfill metadata = %#v", replayed.Meta)
	}
	if _, ok := replayed.Update.(schema.ContentChunk); !ok {
		t.Fatalf("backfill update = %T", replayed.Update)
	}
	select {
	case <-result.Subscription.BackfillDone():
	case <-time.After(2 * time.Second):
		t.Fatal("backfill marker was not delivered")
	}
	continued := receiveRemoteEnvelope(t, result.Subscription.Events())
	usage, ok := continued.Update.(schema.UsageUpdate)
	if !ok || usage.Size != math.MaxUint64 || usage.Used != math.MaxUint64 ||
		continued.Position == nil ||
		continued.Position.Transient == nil ||
		continued.Position.Transient.Sequence != math.MaxUint64 {
		t.Fatalf("live Envelope = %#v", continued)
	}
	if result.Subscription.LastCursor() != "cursor-live" {
		t.Fatalf("LastCursor = %q", result.Subscription.LastCursor())
	}
	if err := result.Subscription.Err(); err != nil {
		t.Fatalf("subscription error = %v", err)
	}
}

func TestReconnectDisconnectsSlowConsumerWithCursor(t *testing.T) {
	backfill := make([]eventstream.Envelope, 4)
	for index := range backfill {
		backfill[index] = eventstream.Envelope{
			Kind: eventstream.KindNotice, Cursor: "cursor-" + strconv.Itoa(index+1),
			SessionID: "session-1", Notice: "event",
		}
	}
	state := controlclient.SessionState{
		ProtocolVersion: schema.CurrentProtocolVersion,
		EnvelopeVersion: controlclient.EnvelopeVersion,
		APIVersion:      controlclient.HTTPAPIVersion,
		SessionID:       "session-1",
		ResumeMode:      controlclient.ResumeModeExact,
	}
	client, closeServer := newFixtureClientWithConfig(t, Config{EventBuffer: 1}, reconnectFixture(t, state, backfill, nil, false))
	defer closeServer()

	result, err := client.Reconnect(context.Background(), controlclient.ReconnectRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	select {
	case <-result.Subscription.BackfillDone():
	case <-time.After(2 * time.Second):
		t.Fatal("slow-consumer subscription did not terminate")
	}
	var gap *controlclient.FeedGapError
	if !errors.As(result.Subscription.Err(), &gap) ||
		!errors.Is(gap, controlclient.ErrSlowConsumer) ||
		gap.RetryCursor != "cursor-1" {
		t.Fatalf("subscription error = %#v", result.Subscription.Err())
	}
}

func newFixtureClient(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	return newFixtureClientWithConfig(t, Config{}, handler)
}

func newFixtureClientWithConfig(t *testing.T, config Config, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	config.BaseURL = "http://127.0.0.1"
	config.BearerToken = "test-token"
	config.HTTPClient = &http.Client{Transport: fixtureRoundTripper{handler: handler}}
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return client, func() {}
}

func assertFixtureRequest(t *testing.T, request *http.Request, method, operationID string) {
	t.Helper()
	if request.Method != method {
		t.Errorf("method = %q, want %q", request.Method, method)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Errorf("Authorization = %q", got)
	}
	if got := request.Header.Get("Idempotency-Key"); got != operationID {
		t.Errorf("Idempotency-Key = %q, want %q", got, operationID)
	}
}

func writeFixtureJSON(t *testing.T, writer http.ResponseWriter, status int, value any) {
	t.Helper()
	raw, err := wirev1.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
}

func decodeFixtureRequest(t *testing.T, request *http.Request, target any) {
	t.Helper()
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := wirev1.DecodeRequest(raw, target); err != nil {
		t.Fatal(err)
	}
}

func reconnectFixture(
	t *testing.T,
	state controlclient.SessionState,
	backfill []eventstream.Envelope,
	live []eventstream.Envelope,
	holdOpen bool,
) http.HandlerFunc {
	t.Helper()
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != wirev1.APIPrefix+"/sessions/session-1/reconnect" {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if cursor := request.URL.Query().Get("after"); cursor != "" && cursor != "cursor-client" {
			t.Errorf("after = %q", cursor)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeFixtureSSE(t, writer, wirev1.BootstrapEventName, "", state)
		for _, envelope := range backfill {
			writeFixtureSSE(t, writer, "", envelope.Cursor, envelope)
		}
		writeFixtureSSE(t, writer, wirev1.BackfillDoneEventName, "", map[string]any{})
		for _, envelope := range live {
			writeFixtureSSE(t, writer, "", envelope.Cursor, envelope)
		}
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		if holdOpen {
			<-request.Context().Done()
		}
	}
}

func writeFixtureSSE(t *testing.T, writer http.ResponseWriter, event, id string, value any) {
	t.Helper()
	var raw []byte
	var err error
	if envelope, ok := value.(eventstream.Envelope); ok {
		raw, err = wirev1.MarshalEnvelope(envelope)
	} else {
		raw, err = wirev1.Marshal(value)
	}
	if err != nil {
		t.Fatal(err)
	}
	if event != "" {
		_, _ = fmt.Fprintf(writer, "event: %s\n", event)
	}
	if id != "" {
		_, _ = fmt.Fprintf(writer, "id: %s\n", id)
	}
	_, _ = fmt.Fprintf(writer, "data: %s\n\n", raw)
}

func receiveRemoteEnvelope(t *testing.T, events <-chan eventstream.Envelope) eventstream.Envelope {
	t.Helper()
	select {
	case envelope, ok := <-events:
		if !ok {
			t.Fatal("remote Envelope channel closed")
		}
		return envelope
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for remote Envelope")
		return eventstream.Envelope{}
	}
}

type fixtureRoundTripper struct {
	handler http.Handler
}

func (roundTripper fixtureRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	requestContext, cancel := context.WithCancel(request.Context())
	clonedRequest := request.Clone(requestContext)
	reader, writer := io.Pipe()
	responseWriter := &fixtureResponseWriter{
		header: make(http.Header),
		body:   writer,
		ready:  make(chan struct{}),
	}
	go func() {
		defer responseWriter.finish()
		roundTripper.handler.ServeHTTP(responseWriter, clonedRequest)
	}()

	select {
	case <-request.Context().Done():
		cancel()
		_ = reader.CloseWithError(request.Context().Err())
		_ = writer.CloseWithError(request.Context().Err())
		return nil, request.Context().Err()
	case <-responseWriter.ready:
		return &http.Response{
			StatusCode: responseWriter.statusCode,
			Header:     responseWriter.header.Clone(),
			Body: &fixtureResponseBody{
				ReadCloser: reader,
				cancel:     cancel,
			},
			Request: clonedRequest,
		}, nil
	}
}

type fixtureResponseWriter struct {
	header     http.Header
	body       *io.PipeWriter
	ready      chan struct{}
	readyOnce  sync.Once
	statusCode int
}

func (writer *fixtureResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *fixtureResponseWriter) WriteHeader(statusCode int) {
	writer.readyOnce.Do(func() {
		writer.statusCode = statusCode
		close(writer.ready)
	})
}

func (writer *fixtureResponseWriter) Write(data []byte) (int, error) {
	writer.WriteHeader(http.StatusOK)
	return writer.body.Write(data)
}

func (writer *fixtureResponseWriter) Flush() {
	writer.WriteHeader(http.StatusOK)
}

func (writer *fixtureResponseWriter) finish() {
	writer.WriteHeader(http.StatusOK)
	_ = writer.body.Close()
}

type fixtureResponseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *fixtureResponseBody) Close() error {
	body.cancel()
	return body.ReadCloser.Close()
}
