package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestFetchToolReturnsCleanMarkdown(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html>
<html><head><title>Ignored Browser Title</title><script>alert("secret")</script></head>
<body><nav>Navigation</nav><article><h1>Article Title</h1><p>Main body with <a href="/docs">docs</a>.</p></article></body></html>`)
	}))
	defer server.Close()

	fetch, err := NewFetch(FetchConfig{Client: server.Client(), AllowPrivateNetwork: true})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}
	result, err := fetch.Call(context.Background(), tool.Call{
		Input: json.RawMessage(fmt.Sprintf(`{"url":%q}`, server.URL)),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := resultPayload(t, result)
	if got := payload["status"]; got != "completed" {
		t.Fatalf("status = %#v, want completed", got)
	}
	content, _ := payload["content"].(string)
	if !strings.Contains(content, "Article Title") || !strings.Contains(content, "Main body") {
		t.Fatalf("content = %q, want readable article text", content)
	}
	if strings.Contains(content, "alert(") {
		t.Fatalf("content contains script: %q", content)
	}
	if !strings.Contains(content, server.URL+"/docs") {
		t.Fatalf("content = %q, want relative link converted to absolute URL", content)
	}
	if _, ok := payload["artifact_path"]; ok {
		t.Fatalf("artifact_path = %#v, WebFetch must not own result artifacts", payload["artifact_path"])
	}
}

func TestFetchToolRejectsPrivateNetworkByDefault(t *testing.T) {
	t.Parallel()

	fetch, err := NewFetch(FetchConfig{})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}
	_, err = fetch.Call(context.Background(), tool.Call{
		Input: json.RawMessage(`{"url":"http://127.0.0.1/"}`),
	})
	if err == nil {
		t.Fatal("Call() error = nil, want private network rejection")
	}
	if !strings.Contains(err.Error(), "private or local network") {
		t.Fatalf("Call() error = %v, want private network rejection", err)
	}
}

func TestFetchToolRejectsPrivateRedirectByDefault(t *testing.T) {
	t.Parallel()

	fetch, err := NewFetch(FetchConfig{})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}
	if fetch.client.CheckRedirect == nil {
		t.Fatal("CheckRedirect = nil, want private network guard")
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1/", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	err = fetch.client.CheckRedirect(req, nil)
	if err == nil {
		t.Fatal("CheckRedirect() error = nil, want private redirect rejection")
	}
	if !strings.Contains(err.Error(), "private or local network") {
		t.Fatalf("CheckRedirect() error = %v, want private network rejection", err)
	}
}

func TestFetchToolPreservesDefaultRedirectLimit(t *testing.T) {
	t.Parallel()

	fetch, err := NewFetch(FetchConfig{})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://8.8.8.8/", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	via := make([]*http.Request, 10)
	err = fetch.client.CheckRedirect(req, via)
	if err == nil {
		t.Fatal("CheckRedirect() error = nil, want default redirect cap")
	}
	if !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Fatalf("CheckRedirect() error = %v, want default redirect cap", err)
	}
}

func TestFetchHTTPClientRejectsPrivateAddressAtDialTime(t *testing.T) {
	t.Parallel()

	client := fetchHTTPClient(FetchConfig{})
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.DialContext == nil {
		t.Fatalf("Transport = %T, want guarded *http.Transport with DialContext", client.Transport)
	}
	_, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil {
		t.Fatal("DialContext() error = nil, want private address rejection")
	}
	if !strings.Contains(err.Error(), "private or local network") {
		t.Fatalf("DialContext() error = %v, want private network rejection", err)
	}
}

func TestFetchToolRejectsInvalidFormat(t *testing.T) {
	t.Parallel()

	fetch, err := NewFetch(FetchConfig{})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}
	_, err = fetch.Call(context.Background(), tool.Call{
		Input: json.RawMessage(`{"url":"https://example.com/","format":"pdf"}`),
	})
	if err == nil {
		t.Fatal("Call() error = nil, want invalid format rejection")
	}
	if !strings.Contains(err.Error(), "format must be one of") {
		t.Fatalf("Call() error = %v, want invalid format rejection", err)
	}
}

func TestFetchToolAppliesTimeoutToDNSPreflight(t *testing.T) {
	original := net.DefaultResolver
	var sawDeadline atomic.Bool
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network string, address string) (net.Conn, error) {
			if _, ok := ctx.Deadline(); ok {
				sawDeadline.Store(true)
			}
			return nil, errors.New("resolver stopped")
		},
	}
	t.Cleanup(func() {
		net.DefaultResolver = original
	})

	fetch, err := NewFetch(FetchConfig{})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}
	_, err = fetch.Call(context.Background(), tool.Call{
		Input: json.RawMessage(`{"url":"https://example.invalid/","timeout":1}`),
	})
	if err == nil {
		t.Fatal("Call() error = nil, want DNS error")
	}
	if !sawDeadline.Load() {
		t.Fatal("DNS preflight context had no deadline")
	}
}

func TestFetchToolReturnsFullContentForRuntimeTruncation(t *testing.T) {
	t.Parallel()

	fullContent := strings.Repeat("large fetched line with useful evidence\n", 3000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprint(w, fullContent)
	}))
	defer server.Close()

	fetch, err := NewFetch(FetchConfig{
		Client:              server.Client(),
		AllowPrivateNetwork: true,
	})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}
	result, err := fetch.Call(context.Background(), tool.Call{
		Input: json.RawMessage(fmt.Sprintf(`{"url":%q}`, server.URL)),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := resultPayload(t, result)
	if got, _ := payload["content"].(string); got != fullContent {
		t.Fatalf("content length = %d, want untruncated full content length %d", len(got), len(fullContent))
	}
	if _, ok := payload["truncated"]; ok {
		t.Fatalf("payload has truncated field: %#v", payload["truncated"])
	}
	_, info := tool.TruncateMap(payload, tool.DefaultTruncationPolicy())
	if !info.Truncated {
		t.Fatalf("global truncation info = %#v, want truncation for large web_fetch payload", info)
	}
	if _, ok := payload["artifact_path"]; ok {
		t.Fatalf("artifact_path = %#v, WebFetch must defer artifacts to Runtime", payload["artifact_path"])
	}
}
