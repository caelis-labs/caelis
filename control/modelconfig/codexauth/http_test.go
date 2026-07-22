package codexauth

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

const testIssuerURL = "https://codex-auth.test"

// newInMemoryHTTPClient exercises OAuth HTTP contracts without binding a host
// port. The callback path uses newMemoryCallbackHarness because serving the
// net/http handler, rather than a kernel TCP socket, is the behavior under test.
func newInMemoryHTTPClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: inMemoryHandlerTransport{handler: handler}}
}

type inMemoryHTTPServer struct {
	URL    string
	client *http.Client
}

func newInMemoryHTTPServer(handler http.Handler) *inMemoryHTTPServer {
	return &inMemoryHTTPServer{URL: testIssuerURL, client: newInMemoryHTTPClient(handler)}
}

func (s *inMemoryHTTPServer) Client() *http.Client {
	if s == nil {
		return nil
	}
	return s.client
}

func (*inMemoryHTTPServer) Close() {}

type inMemoryHandlerTransport struct {
	handler http.Handler
}

func (t inMemoryHandlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close()
	}
	serverRequest := request.Clone(request.Context())
	serverRequest.Header = request.Header.Clone()
	serverRequest.Host = request.URL.Host
	serverRequest.RequestURI = request.URL.RequestURI()
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, serverRequest)
	response := recorder.Result()
	response.Request = request
	return response, nil
}

// newMemoryCallbackHarness runs the real net/http callback server and client
// over net.Pipe. The callback transport remains covered without requiring the
// test environment to permit loopback binds.
func newMemoryCallbackHarness(t *testing.T) (net.Listener, *http.Client) {
	t.Helper()
	listener := &memoryListener{
		connections: make(chan net.Conn),
		closed:      make(chan struct{}),
		addr:        memoryAddr("127.0.0.1:1455"),
	}
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext:       listener.dialContext,
	}
	client := &http.Client{Transport: transport}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		_ = listener.Close()
	})
	return listener, client
}

type memoryListener struct {
	connections chan net.Conn
	closed      chan struct{}
	closeOnce   sync.Once
	addr        net.Addr
}

func (l *memoryListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *memoryListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *memoryListener) Addr() net.Addr {
	return l.addr
}

func (l *memoryListener) dialContext(ctx context.Context, _, _ string) (net.Conn, error) {
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

type memoryAddr string

func (memoryAddr) Network() string  { return "tcp" }
func (a memoryAddr) String() string { return string(a) }
