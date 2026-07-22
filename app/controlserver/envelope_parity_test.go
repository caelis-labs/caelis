package controlserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/surfaces/appserver"
)

func TestInProcessAndHTTPSSEReceiveSameBrokerEnvelope(t *testing.T) {
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := controlclient.NewFeedRegistry(controlclient.FeedRegistryConfig{CursorCodec: codec})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := registry.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	wantSource := eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: "session-1", Notice: "\x1b[32m你好\x1b[0m",
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Meta:     map[string]any{"terminal_output": "\x1b[32m你好\x1b[0m", "exit_code": float64(0)},
	}
	if err := feed.Publish(wantSource); err != nil {
		t.Fatal(err)
	}

	inProcess, err := feed.Subscribe(context.Background(), controlclient.SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := receiveParityEnvelope(t, inProcess.Subscription.Backfill())
	_ = inProcess.Subscription.Close()

	server, err := appserver.New(appserver.Config{
		Service: parityService{feed: feed},
		Authenticator: appserver.AuthenticatorFunc(func(*http.Request) (controlclient.Principal, error) {
			return controlclient.Principal{ID: "owner"}, nil
		}),
		AllowedHosts: []string{"127.0.0.1"}, Heartbeat: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, "http://control.test/api/control/v1/sessions/session-1/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "127.0.0.1"
	request.Header.Set("Authorization", "Bearer parity-test")
	response, err := (&http.Client{Transport: controlHandlerRoundTripper{handler: server}}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for range 3 {
		if _, err := reader.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
	}
	idLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	dataLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(strings.TrimPrefix(idLine, "id: ")) != want.Cursor {
		t.Fatalf("SSE id = %q, want %q", idLine, want.Cursor)
	}
	var got any
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(dataLine, "data: "))), &got); err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var wantWire any
	if err := json.Unmarshal(wantJSON, &wantWire); err != nil {
		t.Fatal(err)
	}
	decimalizeParityPosition(wantWire, want.Position)
	if !reflect.DeepEqual(got, wantWire) {
		t.Fatalf("HTTP/SSE Envelope = %#v, want in-process projection %#v", got, wantWire)
	}
}

func decimalizeParityPosition(value any, position *eventstream.FeedPosition) {
	if position == nil {
		return
	}
	root := value.(map[string]any)
	wirePosition := root["position"].(map[string]any)
	if position.Durable != nil {
		durable := wirePosition["durable"].(map[string]any)
		durable["seq"] = strconv.FormatUint(position.Durable.Seq, 10)
	}
	if position.Transient != nil {
		transient := wirePosition["transient"].(map[string]any)
		anchor := transient["anchor"].(map[string]any)
		anchor["seq"] = strconv.FormatUint(position.Transient.Anchor.Seq, 10)
		transient["sequence"] = strconv.FormatUint(position.Transient.Sequence, 10)
	}
}

type parityService struct {
	controlclient.Service
	feed controlclient.SessionFeed
}

func (s parityService) Subscribe(ctx context.Context, _ controlclient.Principal, req controlclient.SubscribeRequest) (controlclient.SubscribeResult, error) {
	return s.feed.Subscribe(ctx, req)
}

func receiveParityEnvelope(t *testing.T, events <-chan eventstream.Envelope) eventstream.Envelope {
	t.Helper()
	select {
	case envelope := <-events:
		return envelope
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broker Envelope")
		return eventstream.Envelope{}
	}
}

type controlHandlerRoundTripper struct {
	handler http.Handler
}

func (rt controlHandlerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	requestCtx, cancel := context.WithCancel(req.Context())
	request := req.Clone(requestCtx)
	reader, writer := io.Pipe()
	responseWriter := &controlStreamingResponseWriter{
		header: make(http.Header),
		body:   writer,
		ready:  make(chan struct{}),
	}
	go func() {
		rt.handler.ServeHTTP(responseWriter, request)
		responseWriter.finish()
	}()

	select {
	case <-req.Context().Done():
		cancel()
		_ = reader.CloseWithError(req.Context().Err())
		_ = writer.CloseWithError(req.Context().Err())
		return nil, req.Context().Err()
	case <-responseWriter.ready:
		return &http.Response{
			StatusCode: responseWriter.statusCode,
			Header:     responseWriter.header.Clone(),
			Body: &controlResponseBody{
				ReadCloser: reader,
				cancel:     cancel,
			},
			Request: request,
		}, nil
	}
}

type controlStreamingResponseWriter struct {
	header     http.Header
	body       *io.PipeWriter
	ready      chan struct{}
	readyOnce  sync.Once
	statusCode int
}

func (w *controlStreamingResponseWriter) Header() http.Header {
	return w.header
}

func (w *controlStreamingResponseWriter) WriteHeader(statusCode int) {
	w.readyOnce.Do(func() {
		w.statusCode = statusCode
		close(w.ready)
	})
}

func (w *controlStreamingResponseWriter) Write(data []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(data)
}

func (w *controlStreamingResponseWriter) Flush() {
	w.WriteHeader(http.StatusOK)
}

func (w *controlStreamingResponseWriter) FlushError() error {
	w.Flush()
	return nil
}

func (w *controlStreamingResponseWriter) finish() {
	w.WriteHeader(http.StatusOK)
	_ = w.body.Close()
}

type controlResponseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *controlResponseBody) Close() error {
	b.cancel()
	return b.ReadCloser.Close()
}
