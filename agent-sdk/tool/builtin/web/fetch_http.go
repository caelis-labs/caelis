package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"golang.org/x/net/html/charset"
)

type fetchResponseMeta struct {
	requestURL  string
	finalURL    string
	contentType string
	mimeType    string
	statusCode  int
}

func fetchHTTPClient(cfg FetchConfig) *http.Client {
	client := cfg.Client
	if client == nil {
		client = &http.Client{}
	}
	if cfg.AllowPrivateNetwork {
		return client
	}
	copied := *client
	copied.Transport = fetchHTTPTransport(client.Transport)
	existing := copied.CheckRedirect
	copied.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req != nil && req.URL != nil {
			if err := rejectPrivateFetchTarget(req.Context(), req.URL); err != nil {
				return err
			}
		}
		if existing != nil {
			return existing(req, via)
		}
		return defaultFetchCheckRedirect(req, via)
	}
	return &copied
}

func defaultFetchCheckRedirect(_ *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return nil
}

func fetchHTTPTransport(base http.RoundTripper) http.RoundTripper {
	var tr *http.Transport
	switch typed := base.(type) {
	case nil:
		if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
			tr = defaultTransport.Clone()
		}
	case *http.Transport:
		tr = typed.Clone()
	}
	if tr == nil {
		tr = &http.Transport{}
	}
	dialer := &net.Dialer{Timeout: defaultFetchTimeout, KeepAlive: defaultFetchTimeout}
	tr.Proxy = nil
	clearLegacyTransportDialers(tr)
	tr.DialTLSContext = nil
	tr.DialContext = func(ctx context.Context, network string, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("WebFetch: resolve host %q: %w", host, err)
		}
		for _, addr := range ips {
			if isBlockedFetchIP(addr.IP) {
				return nil, fmt.Errorf("WebFetch: refusing to fetch private or local network address %s", addr.IP.String())
			}
		}
		var lastErr error
		for _, addr := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("WebFetch: host %q resolved to no addresses", host)
	}
	return tr
}

func clearLegacyTransportDialers(tr *http.Transport) {
	if tr == nil {
		return
	}
	value := reflect.ValueOf(tr).Elem()
	for _, name := range []string{"Dial", "DialTLS"} {
		field := value.FieldByName(name)
		if field.IsValid() && field.CanSet() {
			field.SetZero()
		}
	}
}

func (t *FetchTool) fetch(ctx context.Context, u *url.URL, format string) (fetchResponseMeta, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fetchResponseMeta{}, nil, err
	}
	req.Header.Set("User-Agent", "caelis-web-fetch")
	req.Header.Set("Accept", acceptHeader(format))
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := t.client.Do(req)
	if err != nil {
		return fetchResponseMeta{}, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fetchResponseMeta{}, nil, fmt.Errorf("WebFetch: HTTP status %d", resp.StatusCode)
	}
	if resp.ContentLength > t.cfg.MaxResponseBytes {
		return fetchResponseMeta{}, nil, fmt.Errorf("WebFetch: response too large (%d bytes, limit %d)", resp.ContentLength, t.cfg.MaxResponseBytes)
	}
	limited := io.LimitReader(resp.Body, t.cfg.MaxResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return fetchResponseMeta{}, nil, err
	}
	if int64(len(raw)) > t.cfg.MaxResponseBytes {
		return fetchResponseMeta{}, nil, fmt.Errorf("WebFetch: response too large (limit %d bytes)", t.cfg.MaxResponseBytes)
	}
	contentType := resp.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(contentType)
	decoded, err := decodeBody(raw, contentType)
	if err != nil {
		return fetchResponseMeta{}, nil, err
	}
	finalURL := u.String()
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return fetchResponseMeta{
		requestURL:  u.String(),
		finalURL:    finalURL,
		contentType: contentType,
		mimeType:    strings.ToLower(strings.TrimSpace(mediaType)),
		statusCode:  resp.StatusCode,
	}, decoded, nil
}

func decodeBody(raw []byte, contentType string) ([]byte, error) {
	reader, err := charset.NewReader(bytes.NewReader(raw), contentType)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

func validateFetchURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("WebFetch: URL must start with http:// or https://")
	}
	if strings.TrimSpace(u.Hostname()) == "" {
		return nil, fmt.Errorf("WebFetch: URL host is required")
	}
	return u, nil
}

func rejectPrivateFetchTarget(ctx context.Context, u *url.URL) error {
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return fmt.Errorf("WebFetch: URL host is required")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("WebFetch: resolve host %q: %w", host, err)
	}
	for _, addr := range ips {
		if isBlockedFetchIP(addr.IP) {
			return fmt.Errorf("WebFetch: refusing to fetch private or local network address %s", addr.IP.String())
		}
	}
	return nil
}

func isBlockedFetchIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsUnspecified() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast()
}

func normalizeFetchFormat(format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "markdown":
		return "markdown", nil
	case "text", "html":
		return strings.ToLower(strings.TrimSpace(format)), nil
	default:
		return "", fmt.Errorf("WebFetch: format must be one of markdown, text, or html")
	}
}

func acceptHeader(format string) string {
	switch format {
	case "markdown":
		return "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1"
	case "text":
		return "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1"
	case "html":
		return "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1"
	default:
		return "*/*"
	}
}
