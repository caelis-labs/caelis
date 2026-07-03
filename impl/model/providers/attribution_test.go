package providers

import (
	"net/http"
	"testing"

	"github.com/caelis-labs/caelis/internal/version"
)

func TestApplyDefaultAttributionHeadersAddsCaelisIdentity(t *testing.T) {
	oldVersion := version.Version
	version.Version = "v1.2.3"
	t.Cleanup(func() { version.Version = oldVersion })

	req, err := http.NewRequest(http.MethodPost, "https://example.com/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}

	applyDefaultAttributionHeaders(req, APIOpenRouter)

	if got := req.Header.Get("User-Agent"); got != "caelis/1.2.3" {
		t.Fatalf("User-Agent = %q, want caelis/1.2.3", got)
	}
	if got := req.Header.Get("HTTP-Referer"); got != caelisOpenRouterReferer {
		t.Fatalf("HTTP-Referer = %q, want %q", got, caelisOpenRouterReferer)
	}
	if got := req.Header.Get("X-Title"); got != caelisOpenRouterTitle {
		t.Fatalf("X-Title = %q, want %q", got, caelisOpenRouterTitle)
	}
}

func TestApplyDefaultAttributionHeadersPreservesExistingIdentity(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "custom-client/9.9.9")
	req.Header.Set("HTTP-Referer", "https://example.com/app")
	req.Header.Set("X-Title", "Example App")

	applyDefaultAttributionHeaders(req, APIOpenRouter)

	if got := req.Header.Get("User-Agent"); got != "custom-client/9.9.9" {
		t.Fatalf("User-Agent = %q, want configured value", got)
	}
	if got := req.Header.Get("HTTP-Referer"); got != "https://example.com/app" {
		t.Fatalf("HTTP-Referer = %q, want configured value", got)
	}
	if got := req.Header.Get("X-Title"); got != "Example App" {
		t.Fatalf("X-Title = %q, want configured value", got)
	}
}
