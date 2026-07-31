package testenv

import (
	"context"
	"errors"
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

// HTTPServer is a net/http test server backed entirely by net.Pipe.
type HTTPServer struct {
	URL string

	t         testing.TB
	server    *http.Server
	listener  *MemoryListener
	client    *http.Client
	done      chan error
	closeOnce sync.Once
}

// NewHTTPServer starts handler behind a real net/http server without binding a
// host port. It is the environment-independent replacement for
// httptest.NewServer when streaming or cancellation behavior matters.
func NewHTTPServer(t testing.TB, handler http.Handler) *HTTPServer {
	t.Helper()
	listener := NewMemoryListener(defaultMemoryHTTPAddress)
	client := NewMemoryHTTPClient(listener)
	server := &http.Server{Handler: handler}
	done := make(chan error, 1)
	result := &HTTPServer{
		URL:      "http://" + listener.Addr().String(),
		t:        t,
		server:   server,
		listener: listener,
		client:   client,
		done:     done,
	}
	go func() {
		done <- server.Serve(listener)
	}()
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

// Close stops the server, active streams, listener, and idle client
// connections. It is safe to call more than once.
func (s *HTTPServer) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		if s.server != nil {
			_ = s.server.Close()
		}
		if s.listener != nil {
			_ = s.listener.Close()
		}
		if s.client != nil {
			s.client.CloseIdleConnections()
		}
		select {
		case err := <-s.done:
			if err != nil &&
				!errors.Is(err, http.ErrServerClosed) &&
				!errors.Is(err, net.ErrClosed) {
				s.t.Errorf("in-memory HTTP server: %v", err)
			}
		case <-time.After(5 * time.Second):
			s.t.Error("in-memory HTTP server did not stop")
		}
	})
}

type memoryAddr string

func (memoryAddr) Network() string        { return "tcp" }
func (address memoryAddr) String() string { return string(address) }
