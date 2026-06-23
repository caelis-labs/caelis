package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/tool"
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

	artifactDir := t.TempDir()
	fetch, err := NewFetch(FetchConfig{Client: server.Client(), AllowPrivateNetwork: true, ArtifactDir: artifactDir})
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
	artifactPath, _ := payload["artifact_path"].(string)
	if artifactPath == "" {
		t.Fatalf("artifact_path = %#v, want path", payload["artifact_path"])
	}
	if !strings.HasPrefix(artifactPath, artifactDir+string(os.PathSeparator)) {
		t.Fatalf("artifact_path = %q, want under %q", artifactPath, artifactDir)
	}
	raw, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", artifactPath, err)
	}
	if artifact := string(raw); !strings.Contains(artifact, "Article Title") || !strings.Contains(artifact, server.URL+"/docs") {
		t.Fatalf("artifact = %q, want complete cleaned markdown", artifact)
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

func TestFetchToolArtifactPathSurvivesGlobalTruncation(t *testing.T) {
	t.Parallel()

	fullContent := strings.Repeat("large fetched line with useful evidence\n", 3000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprint(w, fullContent)
	}))
	defer server.Close()

	artifactDir := t.TempDir()
	fetch, err := NewFetch(FetchConfig{
		Client:              server.Client(),
		AllowPrivateNetwork: true,
		ArtifactDir:         artifactDir,
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
	artifactPath, _ := payload["artifact_path"].(string)
	if artifactPath == "" {
		t.Fatalf("artifact_path = %#v, want path", payload["artifact_path"])
	}
	if !strings.HasPrefix(artifactPath, artifactDir+string(os.PathSeparator)) {
		t.Fatalf("artifact_path = %q, want under %q", artifactPath, artifactDir)
	}
	raw, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", artifactPath, err)
	}
	if string(raw) != fullContent {
		t.Fatalf("artifact content length = %d, want full content length %d", len(raw), len(fullContent))
	}

	canonical, info := tool.TruncateMap(payload, tool.DefaultTruncationPolicy())
	if !info.Truncated {
		t.Fatalf("global truncation info = %#v, want truncation for large web_fetch payload", info)
	}
	if got, _ := canonical["artifact_path"].(string); got != artifactPath {
		t.Fatalf("canonical artifact_path = %q, want %q", got, artifactPath)
	}
}

func TestFetchToolCleansOldArtifactsByFileLimit(t *testing.T) {
	t.Parallel()

	artifactDir := t.TempDir()
	fetch, err := NewFetch(FetchConfig{
		ArtifactDir:      artifactDir,
		ArtifactMaxFiles: 2,
		ArtifactMaxBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}
	resp := fetchResponseMeta{finalURL: "https://example.com/page"}
	var latest string
	for i := 0; i < 4; i++ {
		artifact, err := fetch.writeContentArtifact(resp, fmt.Sprintf("content-%d", i), "markdown")
		if err != nil {
			t.Fatalf("writeContentArtifact(%d) error = %v", i, err)
		}
		latest = artifact.Path
	}
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", artifactDir, err)
	}
	if got := len(entries); got > 2 {
		t.Fatalf("artifact count = %d, want <= 2", got)
	}
	if _, err := os.Stat(latest); err != nil {
		t.Fatalf("latest artifact %q missing after cleanup: %v", latest, err)
	}
}
