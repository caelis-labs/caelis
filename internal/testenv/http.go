package testenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

const defaultMemoryHTTPAddress = "127.0.0.1:1455"

// MemoryListener is a net.Pipe-backed listener for tests that need the real
// net/http server and transport semantics without binding a kernel socket.
type MemoryListener struct {
	connections chan net.Conn
	closed      chan struct{}
	closeOnce   sync.Once
	addr        memoryAddr
}

// NewMemoryListener constructs an in-memory listener. Address is descriptive
// only; clients created with NewMemoryHTTPClient always dial through net.Pipe.
func NewMemoryListener(address string) *MemoryListener {
	if address == "" {
		address = defaultMemoryHTTPAddress
	}
	return &MemoryListener{
		connections: make(chan net.Conn),
		closed:      make(chan struct{}),
		addr:        memoryAddr(address),
	}
}

// Accept implements net.Listener.
func (l *MemoryListener) Accept() (net.Conn, error) {
	if l == nil {
		return nil, net.ErrClosed
	}
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

// Close implements net.Listener.
func (l *MemoryListener) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

// Addr implements net.Listener.
func (l *MemoryListener) Addr() net.Addr {
	if l == nil {
		return memoryAddr(defaultMemoryHTTPAddress)
	}
	return l.addr
}

func (l *MemoryListener) dialContext(
	ctx context.Context,
	_ string,
	_ string,
) (net.Conn, error) {
	if l == nil {
		return nil, net.ErrClosed
	}
	server, client := net.Pipe()
	select {
	case l.connections <- server:
		return client, nil
	case <-l.closed:
		_ = server.Close()
		_ = client.Close()
		return nil, net.ErrClosed
	case <-ctx.Done():
		_ = server.Close()
		_ = client.Close()
		return nil, ctx.Err()
	}
}

// NewMemoryHTTPClient returns a real net/http client whose transport dials the
// supplied MemoryListener. Streaming, request cancellation, and server
// connection lifetime therefore remain covered without host-port permissions.
func NewMemoryHTTPClient(listener *MemoryListener) *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext:       listener.dialContext,
	}}
}

// HTTPServer is a handler-backed HTTP test endpoint. Its client invokes the
// handler through RoundTrip without binding or emulating a host socket.
type HTTPServer struct {
	URL string

	t         testing.TB
	transport *handlerTransport
	client    *http.Client
	closeOnce sync.Once
}

// NewHTTPServer exposes handler through an asynchronous RoundTripper. The
// transport commits on the first WriteHeader, Write, or Flush, so streaming
// and cancellation behavior remain covered without a listener or net.Pipe.
func NewHTTPServer(t testing.TB, handler http.Handler) *HTTPServer {
	t.Helper()
	transport := newHandlerTransport(handler)
	result := &HTTPServer{
		URL:       "http://" + defaultMemoryHTTPAddress,
		t:         t,
		transport: transport,
		client:    &http.Client{Transport: transport},
	}
	t.Cleanup(result.Close)
	return result
}

// Client returns the client paired with the server's in-memory listener.
func (s *HTTPServer) Client() *http.Client {
	if s == nil {
		return nil
	}
	return s.client
}

// Close cancels active handler requests and waits for them to return. It is
// safe to call more than once.
func (s *HTTPServer) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		if s.transport != nil && !s.transport.close(5*time.Second) {
			s.t.Error("handler-backed HTTP transport did not stop")
		}
	})
}

type handlerTransport struct {
	handler   http.Handler
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	closed    bool
	requests  sync.WaitGroup
	closeOnce sync.Once
}

func newHandlerTransport(handler http.Handler) *handlerTransport {
	if handler == nil {
		handler = http.NotFoundHandler()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &handlerTransport{handler: handler, ctx: ctx, cancel: cancel}
}

func (t *handlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errors.New("handler-backed HTTP transport: nil request")
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, net.ErrClosed
	}
	t.requests.Add(1)
	t.mu.Unlock()

	requestCtx, cancel := context.WithCancel(request.Context())
	stopTransportCancel := context.AfterFunc(t.ctx, cancel)
	request = request.Clone(requestCtx)
	reader, writer := io.Pipe()
	response := make(chan *http.Response, 1)
	responseWriter := &handlerResponseWriter{
		header:   make(http.Header),
		request:  request,
		reader:   reader,
		writer:   writer,
		response: response,
		cancel:   cancel,
	}

	go func() {
		defer t.requests.Done()
		defer stopTransportCancel()
		defer cancel()
		if request.Body != nil {
			defer request.Body.Close()
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				responseWriter.closeWithError(fmt.Errorf("handler panic: %v", recovered))
			}
		}()
		t.handler.ServeHTTP(responseWriter, request)
		responseWriter.closeWithError(nil)
	}()

	select {
	case result := <-response:
		return result, nil
	case <-requestCtx.Done():
		_ = reader.CloseWithError(requestCtx.Err())
		_ = writer.CloseWithError(requestCtx.Err())
		return nil, requestCtx.Err()
	}
}

func (t *handlerTransport) close(timeout time.Duration) bool {
	if t == nil {
		return true
	}
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		t.cancel()
		t.mu.Unlock()
	})
	done := make(chan struct{})
	go func() {
		t.requests.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

type handlerResponseWriter struct {
	mu        sync.Mutex
	header    http.Header
	status    int
	committed bool
	request   *http.Request
	reader    *io.PipeReader
	writer    *io.PipeWriter
	response  chan<- *http.Response
	cancel    context.CancelFunc
}

func (w *handlerResponseWriter) Header() http.Header {
	return w.header
}

func (w *handlerResponseWriter) WriteHeader(status int) {
	w.commit(status)
}

func (w *handlerResponseWriter) Write(payload []byte) (int, error) {
	w.commit(http.StatusOK)
	return w.writer.Write(payload)
}

func (w *handlerResponseWriter) Flush() {
	w.commit(http.StatusOK)
}

func (w *handlerResponseWriter) commit(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed {
		return
	}
	if status < 100 {
		status = http.StatusOK
	}
	w.status = status
	w.committed = true
	w.response <- &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:        w.header.Clone(),
		Body:          &cancelReadCloser{ReadCloser: w.reader, cancel: w.cancel},
		ContentLength: -1,
		Request:       w.request,
	}
}

func (w *handlerResponseWriter) closeWithError(err error) {
	w.commit(http.StatusOK)
	if err != nil {
		_ = w.writer.CloseWithError(err)
		return
	}
	_ = w.writer.Close()
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (r *cancelReadCloser) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(r.cancel)
	return r.ReadCloser.Close()
}

type memoryAddr string

func (memoryAddr) Network() string        { return "tcp" }
func (address memoryAddr) String() string { return string(address) }
