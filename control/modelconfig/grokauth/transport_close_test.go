package grokauth

import (
	"net/http"
	"sync/atomic"
	"testing"
)

func TestAuthenticatedTransportForwardsCloseIdleConnections(t *testing.T) {
	t.Parallel()

	base := &closeIdleTransportProbe{}
	client := &http.Client{Transport: &authenticatedTransport{base: base}}
	client.CloseIdleConnections()
	if got := base.calls.Load(); got != 1 {
		t.Fatalf("CloseIdleConnections calls = %d, want 1", got)
	}
}

type closeIdleTransportProbe struct {
	calls atomic.Int32
}

func (p *closeIdleTransportProbe) RoundTrip(*http.Request) (*http.Response, error) {
	panic("RoundTrip must not be called")
}

func (p *closeIdleTransportProbe) CloseIdleConnections() {
	p.calls.Add(1)
}

func TestAuthenticatedTransportForwardsRetryPoolReset(t *testing.T) {
	base := &retryPoolProbe{}
	transport := &authenticatedTransport{base: base}
	transport.ResetConnectionsForRetry(nil)
	if base.resets != 1 || base.calls.Load() != 0 {
		t.Fatalf("resets=%d idle=%d", base.resets, base.calls.Load())
	}
}

type retryPoolProbe struct {
	closeIdleTransportProbe
	resets int
}

func (p *retryPoolProbe) ResetConnectionsForRetry(error) { p.resets++ }
