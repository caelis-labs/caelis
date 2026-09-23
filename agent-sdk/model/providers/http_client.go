package providers

import (
	"errors"
	"net"
	"net/http"
	"time"
)

func resetHTTPConnectionsForRetry(client *http.Client, cause error) {
	if client == nil {
		return
	}
	var timeout streamResponseHeaderTimeoutError
	if errors.As(cause, &timeout) {
		if resetter, ok := client.Transport.(interface{ ResetConnectionsForRetry(error) }); ok {
			resetter.ResetConnectionsForRetry(cause)
			return
		}
	}
	client.CloseIdleConnections()
}

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
