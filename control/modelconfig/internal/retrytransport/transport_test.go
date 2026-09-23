package retrytransport

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
)

func TestResetRetiresBusyPoolWithoutCancelingActiveStream(t *testing.T) {
	listener := &pipeListener{connections: make(chan net.Conn), closed: make(chan struct{})}
	base := &http.Transport{DialContext: listener.dial}
	base.DisableKeepAlives = false
	base.MaxConnsPerHost = 1
	finish := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(finish) }) })
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/active" {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-finish:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.WriteString(w, "complete")
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	pool := Wrap(base).(*transportPool)
	client := &http.Client{Transport: pool}
	t.Cleanup(client.CloseIdleConnections)
	active, err := client.Get("http://provider.test/active")
	if err != nil {
		t.Fatal(err)
	}
	defer active.Body.Close()
	old := pool.current
	pool.ResetConnectionsForRetry(nil)
	if pool.current == old || pool.current == base || pool.current.MaxConnsPerHost != 1 {
		t.Fatal("transport settings or pool ownership lost")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://provider.test/retry", nil)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer retried.Body.Close()
	if body, err := io.ReadAll(retried.Body); err != nil || string(body) != "complete" {
		t.Fatalf("retry=%q, %v", body, err)
	}
	once.Do(func() { close(finish) })
	if body, err := io.ReadAll(active.Body); err != nil || string(body) != "complete" {
		t.Fatalf("active stream=%q, %v", body, err)
	}
}

type pipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error { l.once.Do(func() { close(l.closed) }); return nil }
func (*pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 80} }
func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	server, client := net.Pipe()
	select {
	case l.connections <- server:
		return client, nil
	case <-ctx.Done():
		_ = server.Close()
		_ = client.Close()
		return nil, ctx.Err()
	case <-l.closed:
		_ = server.Close()
		_ = client.Close()
		return nil, net.ErrClosed
	}
}
