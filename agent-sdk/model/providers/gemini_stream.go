package providers

import (
	"errors"
	"io"
	"net/http"
	"sync"
)

// The current GenAI SDK logs a streaming scanner read failure without yielding
// it. Retain that error at its HTTP boundary so a truncated response cannot be
// promoted to a completed model invocation. Remove when the SDK propagates it.
type geminiStreamReadError struct {
	mu    sync.Mutex
	cause error
}

func (s *geminiStreamReadError) err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cause
}

func (s *geminiStreamReadError) client(base *http.Client) *http.Client {
	cloned := *coalesceHTTPClient(base)
	transport := cloned.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	cloned.Transport = geminiStreamTransport{inner: transport, state: s}
	return &cloned
}

type geminiStreamTransport struct {
	inner http.RoundTripper
	state *geminiStreamReadError
}

func (t geminiStreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.inner.RoundTrip(req)
	if err == nil && response != nil && response.Body != nil {
		response.Body = &geminiStreamBody{ReadCloser: response.Body, state: t.state}
	}
	return response, err
}

type geminiStreamBody struct {
	io.ReadCloser
	state *geminiStreamReadError
}

func (b *geminiStreamBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		b.state.mu.Lock()
		b.state.cause = err
		b.state.mu.Unlock()
	}
	return n, err
}
