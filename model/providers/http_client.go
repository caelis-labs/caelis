package providers

import (
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	defaultProviderDialTimeout         = 10 * time.Second
	defaultProviderTLSHandshakeTimeout = 10 * time.Second
	defaultProviderIdleConnTimeout     = 90 * time.Second
)

func coalesceHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	dialer := &net.Dialer{Timeout: defaultProviderDialTimeout}
	return &http.Client{Transport: &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         dialer.DialContext,
		TLSHandshakeTimeout: defaultProviderTLSHandshakeTimeout,
		IdleConnTimeout:     defaultProviderIdleConnTimeout,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
	}}
}

func normalizeProviderBaseURL(raw string, fallback string) string {
	base := strings.TrimSpace(raw)
	if base == "" {
		base = strings.TrimSpace(fallback)
	}
	return strings.TrimRight(base, "/")
}
