package providers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestDoStreamingRequestTimesOutWaitingForHeaders(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(request.Context())
		trace.GotConn(httptrace.GotConnInfo{Reused: true, WasIdle: true, IdleTime: time.Second})
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://provider.test/responses", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}

	response, err := doStreamingRequest(client, request, 20*time.Millisecond)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if response != nil {
		t.Fatalf("response = %#v, want nil", response)
	}
	if !errors.Is(err, errStreamResponseHeaderTimeout) {
		t.Fatalf("error = %v, want response header timeout", err)
	}
	if got := err.Error(); !strings.Contains(got, "got_conn=true") || !strings.Contains(got, "reused=true") ||
		!strings.Contains(got, "wrote_request=true") || !strings.Contains(got, "first_response_byte=false") {
		t.Fatalf("timeout diagnostics = %q", got)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
}

func TestDoStreamingRequestHonorsCallerCancellationBeforeHeaders(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.test/responses", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-started
		cancel()
	}()

	response, err := doStreamingRequest(client, request, time.Second)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if response != nil {
		t.Fatalf("response = %#v, want nil", response)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want caller cancellation", err)
	}
}

func TestNormalizeStreamResponseHeaderTimeoutDefault(t *testing.T) {
	t.Parallel()

	if got := normalizeStreamResponseHeaderTimeout(0); got != 5*time.Minute {
		t.Fatalf("normalizeStreamResponseHeaderTimeout(0) = %s, want 5m", got)
	}
	if got := normalizeStreamResponseHeaderTimeout(-1); got != 0 {
		t.Fatalf("normalizeStreamResponseHeaderTimeout(-1) = %s, want disabled zero", got)
	}
}

func TestMaintainedResponsesProvidersBoundHeaderAndIdleTimeoutRetries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		api  APIType
	}{
		{name: "codex", api: APIOpenAICodex},
		{name: "grok", api: APIXAIResponses},
	}
	for _, tt := range tests {
		t.Run(tt.name+" response headers", func(t *testing.T) {
			t.Parallel()
			transport := &streamTimeoutTestTransport{roundTrip: func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()
				return nil, request.Context().Err()
			}}
			llm := newTimeoutTestLLM(t, tt.api, transport, Config{
				StreamResponseHeaderTimeout: 20 * time.Millisecond,
				StreamFirstEventTimeout:     time.Second,
				StreamIdleTimeout:           time.Second,
			})

			resets, err := runTimeoutTestLLM(llm)
			if !errors.Is(err, errStreamResponseHeaderTimeout) {
				t.Fatalf("Generate() error = %v, want response header timeout", err)
			}
			assertBoundedTimeoutRetry(t, transport, resets)
		})

		t.Run(tt.name+" stream idle", func(t *testing.T) {
			t.Parallel()
			transport := &streamTimeoutTestTransport{roundTrip: func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       newBlockingSSEBody("data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n"),
					Request:    request,
				}, nil
			}}
			llm := newTimeoutTestLLM(t, tt.api, transport, Config{
				StreamResponseHeaderTimeout: time.Second,
				StreamFirstEventTimeout:     time.Second,
				StreamIdleTimeout:           20 * time.Millisecond,
			})

			resets, err := runTimeoutTestLLM(llm)
			if !errors.Is(err, errStreamIdleTimeout) {
				t.Fatalf("Generate() error = %v, want stream idle timeout", err)
			}
			assertBoundedTimeoutRetry(t, transport, resets)
		})
	}
}

func newTimeoutTestLLM(t *testing.T, api APIType, transport *streamTimeoutTestTransport, timeouts Config) model.LLM {
	t.Helper()
	factory := NewFactory()
	if err := factory.Register(Config{
		Alias:                       "timeout-test",
		Provider:                    "test",
		API:                         api,
		Model:                       "test-model",
		BaseURL:                     "https://provider.test",
		HTTPClient:                  &http.Client{Transport: transport},
		StreamResponseHeaderTimeout: timeouts.StreamResponseHeaderTimeout,
		StreamFirstEventTimeout:     timeouts.StreamFirstEventTimeout,
		StreamIdleTimeout:           timeouts.StreamIdleTimeout,
		Auth:                        AuthConfig{Type: AuthOAuthToken},
		Retry: model.RetryConfig{
			MaxRetries:          5,
			BaseDelay:           time.Nanosecond,
			MaxDelay:            time.Nanosecond,
			RateLimitMaxRetries: 5,
			RateLimitBaseDelay:  time.Nanosecond,
			RateLimitMaxDelay:   time.Nanosecond,
		},
	}); err != nil {
		t.Fatal(err)
	}
	llm, err := factory.NewByAlias("timeout-test")
	if err != nil {
		t.Fatal(err)
	}
	return llm
}

func runTimeoutTestLLM(llm model.LLM) (int, error) {
	resets := 0
	var generateErr error
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
		Stream:   true,
	}) {
		if event != nil && event.Type == model.StreamEventAttemptReset {
			resets++
		}
		if err != nil {
			generateErr = err
		}
	}
	return resets, generateErr
}

func assertBoundedTimeoutRetry(t *testing.T, transport *streamTimeoutTestTransport, resets int) {
	t.Helper()
	if got := transport.requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want initial attempt plus one retry", got)
	}
	if got := transport.closeIdle.Load(); got != 1 {
		t.Fatalf("CloseIdleConnections calls = %d, want one before retry", got)
	}
	if resets != 1 {
		t.Fatalf("attempt resets = %d, want one", resets)
	}
}

type streamTimeoutTestTransport struct {
	roundTrip func(*http.Request) (*http.Response, error)
	requests  atomic.Int32
	closeIdle atomic.Int32
}

func (t *streamTimeoutTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests.Add(1)
	return t.roundTrip(request)
}

func (t *streamTimeoutTestTransport) CloseIdleConnections() {
	t.closeIdle.Add(1)
}

type blockingSSEBody struct {
	prefix *strings.Reader
	closed chan struct{}
	once   sync.Once
}

func newBlockingSSEBody(prefix string) *blockingSSEBody {
	return &blockingSSEBody{prefix: strings.NewReader(prefix), closed: make(chan struct{})}
}

func (b *blockingSSEBody) Read(buffer []byte) (int, error) {
	if b.prefix.Len() > 0 {
		return b.prefix.Read(buffer)
	}
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingSSEBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}
