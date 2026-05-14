package providers

import (
	"net/http"
	"testing"
)

func TestCoalesceHTTPClientUsesLongRequestFriendlyTransport(t *testing.T) {
	client := coalesceHTTPClient(nil)
	if client == nil {
		t.Fatal("coalesceHTTPClient(nil) = nil")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client.Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != 0 {
		t.Fatalf("ResponseHeaderTimeout = %s, want no default header deadline", transport.ResponseHeaderTimeout)
	}
	if transport.TLSHandshakeTimeout <= 0 {
		t.Fatal("TLSHandshakeTimeout must be set")
	}
	if transport.IdleConnTimeout <= 0 {
		t.Fatal("IdleConnTimeout must be set")
	}
	if transport.DialContext == nil {
		t.Fatal("DialContext must be set")
	}
}
