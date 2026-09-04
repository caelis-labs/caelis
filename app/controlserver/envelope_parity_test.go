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

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	streamspoolfile "github.com/caelis-labs/caelis/control/streamspool/file"
)

func TestInProcessAndHTTPSSEReceiveSameBrokerEnvelope(t *testing.T) {
	codec, err := appserver.NewCursorCodec(appserver.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	spool, err := streamspoolfile.New(t.Context(), streamspoolfile.Config{RootDir: t.TempDir(), GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = spool.Close() })
	registry, err := appserver.NewFeedRegistry(appserver.FeedRegistryConfig{CursorCodec: codec, Spool: spool})
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

	inProcess, err := feed.Subscribe(context.Background(), appserver.SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := receiveParityEnvelope(t, inProcess.Subscription.Deliveries())
	_ = inProcess.Subscription.Close()

	server, err := New(HandlerConfig{
		Services: testAppServerServices(parityService{feed: feed}, staticStatusService{}),
		Authenticator: AuthenticatorFunc(func(*http.Request) (appserver.Principal, error) {
			return appserver.Principal{ID: "owner"}, nil
		}),
		AllowedHosts: []string{"127.0.0.1"}, Heartbeat: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, "http://control.test/api/control/v1/sessions/session-1/reconnect", nil)
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
	data := readParitySSEData(t, reader, wirev1.FeedDeliveryEventName)
	var delivery wirev1.FeedDelivery
	if err := json.Unmarshal(data, &delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.NextCursor != want.Cursor || len(delivery.Events) != 1 {
		t.Fatalf("SSE delivery = %#v, want cursor %q and one Envelope", delivery, want.Cursor)
	}
	var got any
	if err := json.Unmarshal(delivery.Events[0], &got); err != nil {
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
	appserver.Service
	feed appserver.SessionFeed
}

func (s parityService) Reconnect(ctx context.Context, _ appserver.Principal, req appserver.ReconnectRequest) (appserver.ReconnectResult, error) {
	subscription, err := s.feed.Subscribe(ctx, appserver.SubscribeRequest(req))
	if err != nil {
		return appserver.ReconnectResult{}, err
	}
	return appserver.ReconnectResult{
		State: appserver.SessionState{
			ProtocolVersion: acpsdk.ProtocolVersionNumber,
			EnvelopeVersion: appserver.EnvelopeVersion,
			APIVersion:      appserver.HTTPAPIVersion,
			SessionID:       req.SessionID,
			BoundaryCursor:  subscription.BoundaryCursor,
		},
		Subscription: subscription.Subscription,
	}, nil
}

func receiveParityEnvelope(t *testing.T, deliveries <-chan appserver.FeedDelivery) eventstream.Envelope {
	t.Helper()
	assembler := appserver.FeedDeliveryAssembler{}
	for {
		select {
		case delivery, open := <-deliveries:
			if !open {
				t.Fatal("feed closed before an Envelope was delivered")
			}
			events, _, err := assembler.Accept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) > 0 {
				return events[0]
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for broker Envelope")
		}
	}
}

func readParitySSEData(t *testing.T, reader *bufio.Reader, wantEvent string) []byte {
	t.Helper()
	eventName := ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "event: "):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
		case strings.HasPrefix(line, "data: ") && eventName == wantEvent:
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data: ")))
		}
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
