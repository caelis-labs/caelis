// Package retrytransport owns replaceable connection pools for authenticated
// model clients. It leaves in-flight streams on their original transport.
package retrytransport

import (
	"net/http"
	"sync"
)

// Wrap detaches a standard transport's pool from its caller. Custom transports
// retain their own connection policy and optional retry-reset capability.
func Wrap(base http.RoundTripper) http.RoundTripper {
	if transport, ok := base.(*http.Transport); ok {
		return &transportPool{current: transport.Clone()}
	}
	return base
}

type transportPool struct {
	mu      sync.Mutex
	current *http.Transport
}

func (p *transportPool) RoundTrip(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	current := p.current
	p.mu.Unlock()
	return current.RoundTrip(req)
}

func (p *transportPool) CloseIdleConnections() {
	p.mu.Lock()
	current := p.current
	p.mu.Unlock()
	current.CloseIdleConnections()
}

func (p *transportPool) ResetConnectionsForRetry(error) {
	p.mu.Lock()
	previous := p.current
	p.current = previous.Clone()
	p.mu.Unlock()
	// CloseIdleConnections cannot evict an HTTP/2 connection with active
	// streams. Retiring the whole pool keeps new requests off that connection
	// without canceling other streams that already own it.
	previous.CloseIdleConnections()
}
