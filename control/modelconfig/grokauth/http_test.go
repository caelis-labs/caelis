package grokauth

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func newInMemoryHTTPClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: inMemoryHandlerTransport{handler: handler}}
}

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

func newMemoryCallbackHarness(t *testing.T) (net.Listener, *http.Client) {
	return newMemoryCallbackHarnessAt(t, callbackAddress)
}

func newMemoryCallbackHarnessAt(t *testing.T, address string) (net.Listener, *http.Client) {
	t.Helper()
	listener := &memoryListener{
		connections: make(chan net.Conn),
		closed:      make(chan struct{}),
		addr:        memoryAddr(address),
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
